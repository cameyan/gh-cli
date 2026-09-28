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
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser

	Assignee     string
	Labels       []string
	State        string
	LimitResults int
	Author       string
	Mention      string
	Milestone    string
	Search       string
	IssueType    string
	WebMode      bool
	Exporter     cmdutil.Exporter

	Detector fd.Detector
	Now      func() time.Time
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		Browser:    f.Browser,
		Now:        time.Now,
	}

	var appAuthor string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出仓库中的议题",
		// TODO advancedIssueSearchCleanup
		// 更新链接，并在 GHES 3.17 版本中移除该提及。
		Long: heredoc.Docf(`
			列出 GitHub 仓库中的议题。默认情况下，仅列出打开状态的议题。

			搜索查询语法记录于此:
			<https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests>

			在受支持的 GitHub 主机上，可以在 %[1]s--search%[1]s 查询中
			使用高级议题搜索语法。有关高级议题搜索的更多信息，请参阅:
			<https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/filtering-and-searching-issues-and-pull-requests#building-advanced-filters-for-issues>
		`, "`"),
		Example: heredoc.Doc(`
			$ gh issue list --label "bug" --label "help wanted"
			$ gh issue list --author monalisa
			$ gh issue list --app dependabot
			$ gh issue list --assignee "@me"
			$ gh issue list --milestone "The big 1.0"
			$ gh issue list --search "error no:assignee sort:created-asc"
			$ gh issue list --state all
			$ gh issue list --type Bug
		`),
		Aliases: []string{"ls"},
		Args:    cmdutil.NoArgsQuoteReminder,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 支持 `-R, --repo` 覆盖
			opts.BaseRepo = f.BaseRepo

			if opts.LimitResults < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %v", opts.LimitResults)
			}

			if cmd.Flags().Changed("author") && cmd.Flags().Changed("app") {
				return cmdutil.FlagErrorf("`--author` 或 `--app` 中只能指定一个")
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

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在网页浏览器中列出议题")
	cmd.Flags().StringVarP(&opts.Assignee, "assignee", "a", "", "按负责人过滤")
	cmd.Flags().StringSliceVarP(&opts.Labels, "label", "l", nil, "按标签过滤")
	cmdutil.StringEnumFlag(cmd, &opts.State, "state", "s", "open", []string{"open", "closed", "all"}, "按状态过滤")
	cmd.Flags().IntVarP(&opts.LimitResults, "limit", "L", 30, "要获取的议题数量上限")
	cmd.Flags().StringVarP(&opts.Author, "author", "A", "", "按作者过滤(使用 --app 按 GitHub App 过滤)")
	cmd.Flags().StringVar(&appAuthor, "app", "", "按 GitHub App 作者过滤")
	cmd.Flags().StringVar(&opts.Mention, "mention", "", "按提及过滤")
	cmd.Flags().StringVarP(&opts.Milestone, "milestone", "m", "", "按里程碑编号或标题过滤")
	cmd.Flags().StringVarP(&opts.Search, "search", "S", "", "使用 `query` 搜索议题")
	cmd.Flags().StringVar(&opts.IssueType, "type", "", "按议题类型 `name` 过滤")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.IssueFields)

	return cmd
}

var defaultFields = []string{
	"number",
	"title",
	"url",
	"state",
	"updatedAt",
	"labels",
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

	issueState := strings.ToLower(opts.State)
	if issueState == "open" && prShared.QueryHasStateClause(opts.Search) {
		issueState = ""
	}

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}
	fields := append(defaultFields, "stateReason")

	filterOptions := prShared.FilterOptions{
		Entity:    "issue",
		State:     issueState,
		Assignee:  opts.Assignee,
		Labels:    opts.Labels,
		Author:    opts.Author,
		Mention:   opts.Mention,
		Milestone: opts.Milestone,
		Search:    opts.Search,
		IssueType: opts.IssueType,
		Fields:    fields,
	}

	isTerminal := opts.IO.IsStdoutTTY()

	if opts.WebMode {
		// TODO advancedIssueSearchCleanup
		// 当 GHES 3.17 支持终止后，我们将不再需要功能检测，因为
		// 高级议题搜索是议题唯一可用的搜索后端，并且 GUI
		// (即仓库的 Issues 标签页) 已支持该高级语法。
		searchFeatures, err := opts.Detector.SearchFeatures()
		if err != nil {
			return err
		}

		issueListURL := ghrepo.GenerateRepoURL(baseRepo, "issues")

		// 请注意，如果高级议题搜索 API 可用，则 Issues 标签页也
		// 支持该语法。
		openURL, err := prShared.ListURLWithQuery(issueListURL, filterOptions, searchFeatures.AdvancedIssueSearchAPI)
		if err != nil {
			return err
		}

		if isTerminal {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	if opts.Exporter != nil {
		filterOptions.Fields = opts.Exporter.Fields()
	}

	listResult, err := issueList(httpClient, opts.Detector, baseRepo, filterOptions, opts.LimitResults)
	if err != nil {
		return err
	}
	if len(listResult.Issues) == 0 && opts.Exporter == nil {
		return prShared.ListNoResults(ghrepo.FullName(baseRepo), "issue", !filterOptions.IsDefault())
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, listResult.Issues)
	}

	if listResult.SearchCapped {
		fmt.Fprintln(opts.IO.ErrOut, "警告: 此查询使用 Search API，该 API 最多返回 1000 条结果")
	}
	if isTerminal {
		title := prShared.ListHeader(ghrepo.FullName(baseRepo), "issue", len(listResult.Issues), listResult.TotalCount, !filterOptions.IsDefault())
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", title)
	}

	issueShared.PrintIssues(opts.IO, opts.Now(), "", len(listResult.Issues), listResult.Issues)

	return nil
}

func issueList(client *http.Client, detector fd.Detector, repo ghrepo.Interface, filters prShared.FilterOptions, limit int) (*api.IssuesAndTotalCount, error) {
	apiClient := api.NewClientFromHTTP(client)

	if filters.Search != "" || len(filters.Labels) > 0 || filters.Milestone != "" || filters.IssueType != "" {
		if milestoneNumber, err := strconv.ParseInt(filters.Milestone, 10, 32); err == nil {
			milestone, err := milestoneByNumber(client, repo, int32(milestoneNumber))
			if err != nil {
				return nil, err
			}
			filters.Milestone = milestone.Title
		}

		return searchIssues(apiClient, detector, repo, filters, limit)
	}

	var err error
	meReplacer := prShared.NewMeReplacer(apiClient, repo.RepoHost())
	filters.Assignee, err = meReplacer.Replace(filters.Assignee)
	if err != nil {
		return nil, err
	}
	filters.Author, err = meReplacer.Replace(filters.Author)
	if err != nil {
		return nil, err
	}
	filters.Mention, err = meReplacer.Replace(filters.Mention)
	if err != nil {
		return nil, err
	}

	return listIssues(apiClient, repo, filters, limit)
}

func milestoneByNumber(client *http.Client, repo ghrepo.Interface, number int32) (*api.RepoMilestone, error) {
	var query struct {
		Repository struct {
			Milestone *api.RepoMilestone `graphql:"milestone(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"owner":  githubv4.String(repo.RepoOwner()),
		"name":   githubv4.String(repo.RepoName()),
		"number": githubv4.Int(number),
	}

	gql := api.NewClientFromHTTP(client)
	if err := gql.Query(repo.RepoHost(), "RepositoryMilestoneByNumber", &query, variables); err != nil {
		return nil, err
	}
	if query.Repository.Milestone == nil {
		return nil, fmt.Errorf("未找到编号为 '%d' 的里程碑", number)
	}

	return query.Repository.Milestone, nil
}
