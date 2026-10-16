package checks

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const defaultInterval time.Duration = 10 * time.Second

var prCheckFields = []string{
	"name",
	"state",
	"startedAt",
	"completedAt",
	"link",
	"bucket",
	"event",
	"workflow",
	"description",
}

type ChecksOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Browser    browser.Browser
	Exporter   cmdutil.Exporter

	Finder   shared.PRFinder
	Detector fd.Detector

	SelectorArg string
	WebMode     bool
	Interval    time.Duration
	Watch       bool
	FailFast    bool
	Required    bool
}

func NewCmdChecks(f *cmdutil.Factory, runF func(*ChecksOptions) error) *cobra.Command {
	var interval int
	opts := &ChecksOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
		Browser:    f.Browser,
		Interval:   defaultInterval,
	}

	cmd := &cobra.Command{
		Use:   "checks [<number> | <url> | <branch>]",
		Short: "显示单个拉取请求(PR)的 CI 状态",
		Long: heredoc.Docf(`
			显示单个拉取请求(PR)的 CI 状态。

			若未提供参数，将选择属于当前分支的拉取请求(PR)。

			使用 %[1]s--json%[1]s flag 时，结果包含 %[1]sbucket%[1]s 字段，它将 %[1]sstate%[1]s 字段
			归类为 %[1]spass%[1]s、%[1]sfail%[1]s、%[1]spending%[1]s、%[1]sskipping%[1]s 或 %[1]scancel%[1]s。

			附加的退出码:
				8: 检查仍在等待中
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if opts.Exporter != nil && opts.Watch {
				return cmdutil.FlagErrorf("`--watch` 不能与 `--json` flag 同时使用")
			}

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 `--repo` flag 时必须提供位置参数")
			}

			if opts.FailFast && !opts.Watch {
				return cmdutil.FlagErrorf("`--fail-fast` flag 不能在没有 `--watch` flag 的情况下使用")
			}

			intervalChanged := cmd.Flags().Changed("interval")
			if !opts.Watch && intervalChanged {
				return cmdutil.FlagErrorf("`--interval` flag 不能在没有 `--watch` flag 的情况下使用")
			}

			if intervalChanged {
				var err error
				opts.Interval, err = time.ParseDuration(fmt.Sprintf("%ds", interval))
				if err != nil {
					return cmdutil.FlagErrorf("无法解析 `--interval` flag: %w", err)
				}
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if runF != nil {
				return runF(opts)
			}

			return checksRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "打开 web 浏览器查看检查的详细信息")
	cmd.Flags().BoolVarP(&opts.Watch, "watch", "", false, "持续监视检查直到它们完成")
	cmd.Flags().BoolVarP(&opts.FailFast, "fail-fast", "", false, "首次检查失败时退出监视模式")
	cmd.Flags().IntVarP(&interval, "interval", "i", 10, "监视模式下的刷新间隔(秒)")
	cmd.Flags().BoolVar(&opts.Required, "required", false, "只显示必须(required)的检查")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, prCheckFields)

	return cmd
}

func checksRunWebMode(opts *ChecksOptions) error {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"number"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	isTerminal := opts.IO.IsStdoutTTY()
	openURL := ghrepo.GenerateRepoURL(baseRepo, "pull/%d/checks", pr.Number)

	if isTerminal {
		fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
	}

	return opts.Browser.Browse(openURL)
}

func checksRun(opts *ChecksOptions) error {
	if opts.WebMode {
		return checksRunWebMode(opts)
	}

	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"number", "headRefName"},
	}

	var pr *api.PullRequest
	pr, repo, findErr := opts.Finder.Find(findOptions)
	if findErr != nil {
		return findErr
	}

	client, clientErr := opts.HttpClient()
	if clientErr != nil {
		return clientErr
	}

	var checks []check
	var counts checkCounts
	var err error
	var includeEvent bool

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(client, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, repo.RepoHost())
	}
	if features, featuresErr := opts.Detector.PullRequestFeatures(); featuresErr != nil {
		return featuresErr
	} else {
		includeEvent = features.CheckRunEvent
	}

	checks, counts, err = populateStatusChecks(client, repo, pr, opts.Required, includeEvent)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, checks)
	}

	if opts.Watch {
		opts.IO.StartAlternateScreenBuffer()
	} else {
		// 仅在非监视模式下启动分页器(pager)
		if err := opts.IO.StartPager(); err == nil {
			defer opts.IO.StopPager()
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
		}
	}

	// 在可以 StopAlternateScreenBuffer() 之前不要返回 err
	for {
		if counts.Pending != 0 && opts.Watch {
			opts.IO.RefreshScreen()
			cs := opts.IO.ColorScheme()
			fmt.Fprintln(opts.IO.Out, cs.Boldf("每 %v 秒刷新一次检查状态。按 Ctrl+C 退出。\n", opts.Interval.Seconds()))
		}

		printSummary(opts.IO, counts)
		err = printTable(opts.IO, checks)
		if err != nil {
			break
		}

		if counts.Pending == 0 || !opts.Watch {
			break
		}

		if opts.FailFast && counts.Failed > 0 {
			break
		}

		time.Sleep(opts.Interval)

		checks, counts, err = populateStatusChecks(client, repo, pr, opts.Required, includeEvent)
		if err != nil {
			break
		}
	}

	opts.IO.StopAlternateScreenBuffer()
	if err != nil {
		return err
	}

	if opts.Watch {
		// 将最终汇总打印到原始屏幕缓冲区
		printSummary(opts.IO, counts)
		err = printTable(opts.IO, checks)
		if err != nil {
			return err
		}
	}

	if counts.Failed > 0 {
		return cmdutil.SilentError
	} else if counts.Pending > 0 {
		return cmdutil.PendingError
	}

	return nil
}

func populateStatusChecks(client *http.Client, repo ghrepo.Interface, pr *api.PullRequest, requiredChecks bool, includeEvent bool) ([]check, checkCounts, error) {
	apiClient := api.NewClientFromHTTP(client)

	type response struct {
		Node *api.PullRequest
	}

	query := fmt.Sprintf(`
	query PullRequestStatusChecks($id: ID!, $endCursor: String) {
		node(id: $id) {
			...on PullRequest {
				%s
			}
		}
	}`, api.RequiredStatusCheckRollupGraphQL("$id", "$endCursor", includeEvent))

	variables := map[string]any{
		"id": pr.ID,
	}

	statusCheckRollup := api.CheckContexts{}

	for {
		var resp response
		err := apiClient.GraphQL(repo.RepoHost(), query, variables, &resp)
		if err != nil {
			return nil, checkCounts{}, err
		}

		if len(resp.Node.StatusCheckRollup.Nodes) == 0 {
			return nil, checkCounts{}, errors.New("拉取请求(PR)上未找到提交")
		}

		result := resp.Node.StatusCheckRollup.Nodes[0].Commit.StatusCheckRollup.Contexts
		statusCheckRollup.Nodes = append(
			statusCheckRollup.Nodes,
			result.Nodes...,
		)

		if !result.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = result.PageInfo.EndCursor
	}

	if len(statusCheckRollup.Nodes) == 0 {
		return nil, checkCounts{}, fmt.Errorf("'%s' 分支上没有任何检查报告", pr.HeadRefName)
	}

	checks, counts := aggregateChecks(statusCheckRollup.Nodes, requiredChecks)
	if len(checks) == 0 && requiredChecks {
		return checks, counts, fmt.Errorf("'%s' 分支上没有任何必须(required)检查报告", pr.HeadRefName)
	}
	return checks, counts, nil
}
