package status

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type StatusOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Remotes    func() (ghContext.Remotes, error)
	Branch     func() (string, error)

	HasRepoOverride bool
	Exporter        cmdutil.Exporter
	ConflictStatus  bool

	Detector fd.Detector
}

func NewCmdStatus(f *cmdutil.Factory, runF func(*StatusOptions) error) *cobra.Command {
	opts := &StatusOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
		Remotes:    f.Remotes,
		Branch:     f.Branch,
	}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "显示相关拉取请求(PR)的状态",
		Long: heredoc.Docf(`
			显示相关拉取请求(PR)的状态。

			该状态会展示拉取请求的摘要，包含 PR 编号、标题、CI 检查(check)、
			审核(review)等信息。

			要查看 CI 检查的更多细节，请运行 %[1]sgh pr checks%[1]s。
		`, "`"),
		Args: cmdutil.NoArgsQuoteReminder,
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo
			opts.HasRepoOverride = cmd.Flags().Changed("repo")

			if runF != nil {
				return runF(opts)
			}
			return statusRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.ConflictStatus, "conflict-status", "c", false, "显示每个拉取请求(PR)的合并冲突(conflict)状态")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.PullRequestFields)

	return cmd
}

func statusRun(opts *StatusOptions) error {
	ctx := context.Background()
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRefRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	var currentBranchName string
	var currentPRNumber int
	var currentHeadRefBranchName string

	if !opts.HasRepoOverride {
		// 未使用覆盖时，我们必然处于某个仓库中
		currentBranchName, err = opts.Branch()
		if err != nil && !errors.Is(err, git.ErrNotOnAnyBranch) {
			return fmt.Errorf("查询当前分支的拉取请求(PR)失败: %w", err)
		}

		if !errors.Is(err, git.ErrNotOnAnyBranch) {
			branchConfig, err := opts.GitClient.ReadBranchConfig(ctx, currentBranchName)
			if err != nil {
				return err
			}
			// 判断该分支是否被配置为合并到特殊的 PR ref
			prHeadRE := regexp.MustCompile(`^refs/pull/(\d+)/head$`)
			if m := prHeadRE.FindStringSubmatch(branchConfig.MergeRef); m != nil {
				currentPRNumber, _ = strconv.Atoi(m[1])
			}

			if currentPRNumber == 0 {
				prRefsResolver := shared.NewPullRequestFindRefsResolver(
					// 已经请求过分支配置，因此缓存它
					shared.CachedBranchConfigGitConfigClient{
						CachedBranchConfig: branchConfig,
						GitConfigClient:    opts.GitClient,
					},
					opts.Remotes,
				)

				prRefs, err := prRefsResolver.ResolvePullRequestRefs(baseRefRepo, "", currentBranchName)
				if err != nil {
					return err
				}

				currentHeadRefBranchName = prRefs.QualifiedHeadRef()
			}
		}
	}

	options := requestOptions{
		Username:       "@me",
		CurrentPR:      currentPRNumber,
		HeadRef:        currentHeadRefBranchName,
		ConflictStatus: opts.ConflictStatus,
	}
	if opts.Exporter != nil {
		options.Fields = opts.Exporter.Fields()
	}

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRefRepo.RepoHost())
	}
	prFeatures, err := opts.Detector.PullRequestFeatures()
	if err != nil {
		return err
	}
	options.CheckRunAndStatusContextCountsSupported = prFeatures.CheckRunAndStatusContextCounts

	prPayload, err := pullRequestStatus(httpClient, baseRefRepo, options)
	if err != nil {
		return err
	}

	err = opts.IO.StartPager()
	if err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.Exporter != nil {
		data := map[string]any{
			"currentBranch": nil,
			"createdBy":     prPayload.ViewerCreated.PullRequests,
			"needsReview":   prPayload.ReviewRequested.PullRequests,
		}
		if prPayload.CurrentPR != nil {
			data["currentBranch"] = prPayload.CurrentPR
		}
		return opts.Exporter.Write(opts.IO, data)
	}

	out := opts.IO.Out
	cs := opts.IO.ColorScheme()

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "%s 中相关的拉取请求(PR)\n", ghrepo.FullName(baseRefRepo))
	fmt.Fprintln(out, "")

	if !opts.HasRepoOverride {
		shared.PrintHeader(opts.IO, "当前分支")
		currentPR := prPayload.CurrentPR
		if currentPR != nil && currentPR.State != "OPEN" && prPayload.DefaultBranch == currentBranchName {
			currentPR = nil
		}
		if currentPR != nil {
			printPrs(opts.IO, 1, *currentPR)
		} else if currentHeadRefBranchName == "" {
			shared.PrintMessage(opts.IO, "  当前没有任何分支")
		} else {
			shared.PrintMessage(opts.IO, fmt.Sprintf("  %s 没有关联的拉取请求(PR)", cs.Cyan("["+currentHeadRefBranchName+"]")))
		}
		fmt.Fprintln(out)
	}

	shared.PrintHeader(opts.IO, "由您创建")
	if prPayload.ViewerCreated.TotalCount > 0 {
		printPrs(opts.IO, prPayload.ViewerCreated.TotalCount, prPayload.ViewerCreated.PullRequests...)
	} else {
		shared.PrintMessage(opts.IO, "  您没有 open 状态的拉取请求(PR)")
	}
	fmt.Fprintln(out)

	shared.PrintHeader(opts.IO, "请求您进行代码审核")
	if prPayload.ReviewRequested.TotalCount > 0 {
		printPrs(opts.IO, prPayload.ReviewRequested.TotalCount, prPayload.ReviewRequested.PullRequests...)
	} else {
		shared.PrintMessage(opts.IO, "  您没有需要审核的拉取请求(PR)")
	}
	fmt.Fprintln(out)

	return nil
}

func totalApprovals(pr *api.PullRequest) int {
	approvals := 0
	for _, review := range pr.LatestReviews.Nodes {
		if review.State == "APPROVED" {
			approvals++
		}
	}
	return approvals
}

func printPrs(io *iostreams.IOStreams, totalCount int, prs ...api.PullRequest) {
	w := io.Out
	cs := io.ColorScheme()

	for _, pr := range prs {
		prNumber := fmt.Sprintf("#%d", pr.Number)

		prStateColorFunc := cs.ColorFromString(shared.ColorForPRState(pr))

		fmt.Fprintf(w, "  %s  %s %s", prStateColorFunc(prNumber), text.Truncate(50, text.RemoveExcessiveWhitespace(pr.Title)), cs.Cyan("["+pr.HeadLabel()+"]"))

		checks := pr.ChecksStatus()
		reviews := pr.ReviewStatus()

		if pr.State == "OPEN" {
			reviewStatus := reviews.ChangesRequested || reviews.Approved || reviews.ReviewRequired
			if checks.Total > 0 || reviewStatus {
				// 将检查与审核各自单独显示一行
				fmt.Fprintf(w, "\n  ")
			}

			if checks.Total > 0 {
				summary := shared.PrCheckStatusSummaryWithColor(cs, checks)
				fmt.Fprint(w, summary)
			}

			if checks.Total > 0 && reviewStatus {
				// 在检查与审核之间加入间隔
				fmt.Fprint(w, " ")
			}

			if reviews.ChangesRequested {
				fmt.Fprint(w, cs.Red("+ 已请求变更(requested changes)"))
			} else if reviews.ReviewRequired {
				fmt.Fprint(w, cs.Yellow("- 需要审核(review)"))
			} else if reviews.Approved {
				numRequiredApprovals := pr.BaseRef.BranchProtectionRule.RequiredApprovingReviewCount
				gotApprovals := totalApprovals(&pr)
				s := fmt.Sprintf("%d", gotApprovals)
				if numRequiredApprovals > 0 {
					s = fmt.Sprintf("%d/%d", gotApprovals, numRequiredApprovals)
				}
				fmt.Fprint(w, cs.Green(fmt.Sprintf("✓ 已批准 %s", s)))
			}

			if pr.Mergeable == api.PullRequestMergeableMergeable {
				// 相比 "可合并(Mergeable)"，优先显示 "无合并冲突"，
				// 因为可合并性不只是 git 状态。即使 PR 在技术上可合并，
				// 缺失或失败的必需检查也会阻止合并，这常常令人困惑。
				fmt.Fprintf(w, " %s", cs.Green("✓ 无合并冲突"))
			} else if pr.Mergeable == api.PullRequestMergeableConflicting {
				fmt.Fprintf(w, " %s", cs.Red("× 存在合并冲突"))
			} else if pr.Mergeable == api.PullRequestMergeableUnknown {
				fmt.Fprintf(w, " %s", cs.Yellow("! 合并冲突状态未知"))
			}

			if pr.BaseRef.BranchProtectionRule.RequiresStrictStatusChecks {
				switch pr.MergeStateStatus {
				case "BEHIND":
					fmt.Fprintf(w, " %s", cs.Yellow("- 不是最新"))
				case "UNKNOWN", "DIRTY":
					// 不打印任何内容
				default:
					fmt.Fprintf(w, " %s", cs.Green("✓ 已是最新"))
				}
			}

			if pr.AutoMergeRequest != nil {
				fmt.Fprintf(w, " %s", cs.Green("✓ 自动合并(auto-merge)已启用"))
			}

		} else {
			fmt.Fprintf(w, " - %s", shared.StateTitleWithColor(cs, pr))
		}

		fmt.Fprint(w, "\n")
	}
	remaining := totalCount - len(prs)
	if remaining > 0 {
		fmt.Fprintln(w, cs.Mutedf("  以及另外 %d 条", remaining))
	}
}
