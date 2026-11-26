package list

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	Detector   fd.Detector

	WebMode      bool
	LimitResults int
	Exporter     cmdutil.Exporter

	State      string
	BaseBranch string
	HeadBranch string
	Labels     []string
	Author     string
	Assignee   string
	Search     string
	Draft      *bool

	Now func() time.Time
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Now:        time.Now,
	}

	var appAuthor string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出仓库中的拉取请求(PR)",
		// TODO advancedIssueSearchCleanup
		// 在 GHES 3.17 版本更新链接并移除此说明。
		Long: heredoc.Docf(`
			列出 GitHub 仓库中的拉取请求(PR)。默认只列出 open 状态的 PR。

			搜索查询语法文档见:
			<https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests>

			在受支持的 GitHub 主机上，可在 %[1]s--search%[1]s 查询中使用高级 issue 搜索语法。
			有关高级 issue 搜索的更多信息，请参见:
			<https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/filtering-and-searching-issues-and-pull-requests#building-advanced-filters-for-issues>
		`, "`"),
		Example: heredoc.Doc(`
			# 列出由您创建的 PR
			$ gh pr list --author "@me"

			# 列出由 GitHub App(如 Dependabot)打开的 PR
			$ gh pr list --app dependabot

			# 列出具有指定源分支(head branch)名的 PR
			$ gh pr list --head "typo"

			# 只列出拥有全部给定标签(label)的 PR
			$ gh pr list --label bug --label "priority 1"

			# 使用搜索语法过滤 PR
			$ gh pr list --search "status:success review:required"

			# 查找引入了某个给定提交(commit)的 PR
			$ gh pr list --search "<SHA>" --state merged
		`),
		Aliases: []string{"ls"},
		Args:    cmdutil.NoArgsQuoteReminder,
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if opts.LimitResults < 1 {
				return cmdutil.FlagErrorf("--limit 的取值无效: %v", opts.LimitResults)
			}

			if cmd.Flags().Changed("author") && cmd.Flags().Changed("app") {
				return cmdutil.FlagErrorf("只能指定 `--author` 或 `--app` 之一")
			}

			if cmd.Flags().Changed("app") {
				opts.Author = fmt.Sprintf("app/%s", appAuthor)
			}

			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中列出拉取请求(PR)")
	cmd.Flags().IntVarP(&opts.LimitResults, "limit", "L", 30, "获取条目的最大数量")
	cmdutil.StringEnumFlag(cmd, &opts.State, "state", "s", "open", []string{"open", "closed", "merged", "all"}, "按状态过滤")
	cmd.Flags().StringVarP(&opts.BaseBranch, "base", "B", "", "按基础分支(base branch)过滤")
	cmd.Flags().StringVarP(&opts.HeadBranch, "head", "H", "", `按源分支(head branch)过滤 (不支持 "<owner>:<branch>" 语法)`)
	cmd.Flags().StringSliceVarP(&opts.Labels, "label", "l", nil, "按标签(label)过滤")
	cmd.Flags().StringVarP(&opts.Author, "author", "A", "", "按作者过滤(使用 --app 按 GitHub App 过滤)")
	cmd.Flags().StringVar(&appAuthor, "app", "", "按 GitHub App 作者过滤")
	cmd.Flags().StringVarP(&opts.Assignee, "assignee", "a", "", "按被指派人(assignee)过滤")
	cmd.Flags().StringVarP(&opts.Search, "search", "S", "", "使用 `query` 搜索拉取请求(PR)")
	cmdutil.NilBoolFlag(cmd, &opts.Draft, "draft", "d", "按草稿(draft)状态过滤")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.PullRequestFields)

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "base", "head")

	return cmd
}

var defaultFields = []string{
	"number",
	"title",
	"state",
	"url",
	"headRefName",
	"headRepositoryOwner",
	"isCrossRepository",
	"isDraft",
	"createdAt",
}

func listRun(opts *ListOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}

	prState := strings.ToLower(opts.State)
	if prState == "open" && shared.QueryHasStateClause(opts.Search) {
		prState = ""
	}

	filters := shared.FilterOptions{
		Entity:     "pr",
		State:      prState,
		Author:     opts.Author,
		Assignee:   opts.Assignee,
		Labels:     opts.Labels,
		BaseBranch: opts.BaseBranch,
		HeadBranch: opts.HeadBranch,
		Search:     opts.Search,
		Draft:      opts.Draft,
		Fields:     defaultFields,
	}
	if opts.Exporter != nil {
		filters.Fields = opts.Exporter.Fields()
	}
	if opts.WebMode {
		prListURL := ghrepo.GenerateRepoURL(baseRepo, "pulls")

		// TODO advancedSearchFuture
		// 截至 2025 年 8 月，仓库的 Pull Requests 标签页尚不支持高级 issue 搜索语法。
		// 待其受支持后，可将此处参数改为 true。
		openURL, err := shared.ListURLWithQuery(prListURL, filters, false)
		if err != nil {
			return err
		}

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	listResult, err := listPullRequests(httpClient, opts.Detector, baseRepo, filters, opts.LimitResults)
	if err != nil {
		return err
	}
	if len(listResult.PullRequests) == 0 && opts.Exporter == nil {
		return shared.ListNoResults(ghrepo.FullName(baseRepo), "pull request", !filters.IsDefault())
	}

	err = opts.IO.StartPager()
	if err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)出错: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, listResult.PullRequests)
	}

	if listResult.SearchCapped {
		fmt.Fprintln(opts.IO.ErrOut, "警告: 该查询使用 Search API，结果上限最多 1000 条")
	}
	if opts.IO.IsStdoutTTY() {
		title := shared.ListHeader(ghrepo.FullName(baseRepo), "pull request", len(listResult.PullRequests), listResult.TotalCount, !filters.IsDefault())
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", title)
	}

	cs := opts.IO.ColorScheme()
	isTTY := opts.IO.IsStdoutTTY()

	headers := []string{
		"ID",
		"标题",
		"分支(branch)",
	}
	if !isTTY {
		headers = append(headers, "状态")
	}
	headers = append(headers, "创建时间")

	table := tableprinter.New(opts.IO, tableprinter.WithHeader(headers...))
	for _, pr := range listResult.PullRequests {
		prNum := strconv.Itoa(pr.Number)
		if isTTY {
			prNum = "#" + prNum
		}

		table.AddField(prNum, tableprinter.WithColor(cs.ColorFromString(shared.ColorForPRState(pr))))
		table.AddField(text.RemoveExcessiveWhitespace(pr.Title))
		table.AddField(pr.HeadLabel(), tableprinter.WithColor(cs.Cyan))
		if !isTTY {
			table.AddField(shared.PrStateWithDraft(&pr))
		}
		table.AddTimeField(opts.Now(), pr.CreatedAt, cs.Muted)
		table.EndRow()
	}
	err = table.Render()
	if err != nil {
		return err
	}

	return nil
}
