package view

import (
	"fmt"
	"io"
	"net/http"
	"sort"
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
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	Detector   fd.Detector

	IssueNumber int
	WebMode     bool
	Comments    bool
	Exporter    cmdutil.Exporter

	Now func() time.Time
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Now:        time.Now,
	}

	cmd := &cobra.Command{
		Use:   "view {<number> | <url>}",
		Short: "查看 issue",
		Long: heredoc.Docf(`
			显示 issue 的标题、正文及其他信息。

			使用 %[1]s--web%[1]s 选项时，改为在网页浏览器中打开该 issue。
		`, "`"),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.MutuallyExclusive("`--comments` 或 `--json` 中只能指定一个",
				opts.Comments, opts.Exporter != nil); err != nil {
				return err
			}

			issueNumber, baseRepo, err := issueShared.ParseIssueFromArg(args[0])
			if err != nil {
				return err
			}

			// 如果参数提供了基础仓库，则直接使用它。
			if baseRepo, present := baseRepo.Value(); present {
				opts.BaseRepo = func() (ghrepo.Interface, error) {
					return baseRepo, nil
				}
			} else {
				// 支持 `-R, --repo` 覆盖
				opts.BaseRepo = f.BaseRepo
			}

			opts.IssueNumber = issueNumber

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在浏览器中打开 issue")
	cmd.Flags().BoolVarP(&opts.Comments, "comments", "c", false, "查看 issue 评论")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.IssueFields)

	return cmd
}

var defaultFields = []string{
	"number", "url", "state", "createdAt", "title", "body", "author", "milestone",
	"assignees", "labels", "reactionGroups", "lastComment", "stateReason",
	"issueType", "parent", "subIssues", "subIssuesSummary",
}

func viewRun(opts *ViewOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	lookupFields := set.NewStringSet()
	if opts.Exporter != nil {
		lookupFields.AddValues(opts.Exporter.Fields())
	} else if opts.WebMode {
		lookupFields.Add("url")
	} else {
		lookupFields.AddValues(defaultFields)
		if opts.Comments {
			lookupFields.Add("comments")
			lookupFields.Remove("lastComment")
		}

		// TODO projectsV1Deprecation
		// 移除本节，因为我们不应再添加 projectCards
		if opts.Detector == nil {
			cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
			opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
		}

		lookupFields.Add("projectItems")
		projectsV1Support := opts.Detector.ProjectsV1()
		if projectsV1Support == gh.ProjectsV1Supported {
			lookupFields.Add("projectCards")
		}

		// TODO IssueRelationshipsCleanup
		issueFeatures, issueErr := opts.Detector.IssueFeatures()
		if issueErr == nil && issueFeatures.IssueRelationshipsSupported {
			lookupFields.AddValues([]string{"blockedBy", "blocking"})
		}
	}

	opts.IO.DetectTerminalTheme()

	opts.IO.StartProgressIndicator()
	defer opts.IO.StopProgressIndicator()

	lookupFields.Add("id")

	issue, err := issueShared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, lookupFields.ToSlice())
	if err != nil {
		return err
	}

	if lookupFields.Contains("comments") {
		err := preloadIssueComments(httpClient, baseRepo, issue)
		if err != nil {
			return err
		}
	}

	if lookupFields.Contains("closedByPullRequestsReferences") {
		err := preloadClosedByPullRequestsReferences(httpClient, baseRepo, issue)
		if err != nil {
			return err
		}
	}

	opts.IO.StopProgressIndicator()

	if opts.WebMode {
		openURL := issue.URL
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, issue)
	}

	if opts.IO.IsStdoutTTY() {
		return printHumanIssuePreview(opts, baseRepo, issue)
	}

	if opts.Comments {
		fmt.Fprint(opts.IO.Out, prShared.RawCommentList(issue.Comments, api.PullRequestReviews{}))
		return nil
	}

	return printRawIssuePreview(opts.IO.Out, issue)
}

func printRawIssuePreview(out io.Writer, issue *api.Issue) error {
	assignees := issueAssigneeList(*issue)
	labels := issueLabelList(issue, nil)
	projects := issueProjectList(*issue)

	// 为空值打印空字符串，使得在使用 head 和 grep
	// 处理大量issue时元数据行数保持一致。
	fmt.Fprintf(out, "title:\t%s\n", issue.Title)
	fmt.Fprintf(out, "state:\t%s\n", issue.State)
	fmt.Fprintf(out, "author:\t%s\n", issue.Author.DisplayName())
	fmt.Fprintf(out, "labels:\t%s\n", labels)
	fmt.Fprintf(out, "comments:\t%d\n", issue.Comments.TotalCount)
	fmt.Fprintf(out, "assignees:\t%s\n", assignees)
	fmt.Fprintf(out, "projects:\t%s\n", projects)
	var milestoneTitle string
	if issue.Milestone != nil {
		milestoneTitle = issue.Milestone.Title
	}
	fmt.Fprintf(out, "milestone:\t%s\n", milestoneTitle)
	var issueTypeName string
	if issue.IssueType != nil {
		issueTypeName = issue.IssueType.Name
	}
	fmt.Fprintf(out, "issue-type:\t%s\n", issueTypeName)
	var parentRef string
	if issue.Parent != nil {
		parentRef = formatLinkedIssueRef(issue.Parent)
	}
	fmt.Fprintf(out, "parent:\t%s\n", parentRef)
	fmt.Fprintf(out, "sub-issues:\t%s\n", formatLinkedIssueRefs(issue.SubIssues.Nodes))
	var subIssuesCompleted string
	if issue.SubIssuesSummary.Total > 0 {
		subIssuesCompleted = fmt.Sprintf("%d/%d", issue.SubIssuesSummary.Completed, issue.SubIssuesSummary.Total)
	}
	fmt.Fprintf(out, "sub-issues-completed:\t%s\n", subIssuesCompleted)
	fmt.Fprintf(out, "blocked-by:\t%s\n", formatLinkedIssueRefs(issue.BlockedBy.Nodes))
	fmt.Fprintf(out, "blocking:\t%s\n", formatLinkedIssueRefs(issue.Blocking.Nodes))
	fmt.Fprintf(out, "number:\t%d\n", issue.Number)
	fmt.Fprintln(out, "--")
	fmt.Fprintln(out, issue.Body)
	return nil
}

func printHumanIssuePreview(opts *ViewOptions, baseRepo ghrepo.Interface, issue *api.Issue) error {
	out := opts.IO.Out
	cs := opts.IO.ColorScheme()

	// 头部 (标题和状态)
	fmt.Fprintf(out, "%s %s#%d\n", cs.Bold(issue.Title), ghrepo.FullName(baseRepo), issue.Number)

	// 状态行 - 存在issue类型前缀时包含它
	stateLine := issueStateTitleWithColor(cs, issue)
	if issue.IssueType != nil {
		stateLine = cs.Muted(issue.IssueType.Name) + " · " + stateLine
	}
	fmt.Fprintf(out,
		"%s • %s 打开了 %s • %s\n",
		stateLine,
		issue.Author.DisplayName(),
		text.FuzzyAgo(opts.Now(), issue.CreatedAt),
		text.Pluralize(issue.Comments.TotalCount, "comment"),
	)

	// 反应
	if reactions := prShared.ReactionGroupList(issue.ReactionGroups); reactions != "" {
		fmt.Fprint(out, reactions)
		fmt.Fprintln(out)
	}

	// 元数据
	if assignees := issueAssigneeList(*issue); assignees != "" {
		fmt.Fprint(out, cs.Bold("负责人: "))
		fmt.Fprintln(out, assignees)
	}
	if labels := issueLabelList(issue, cs); labels != "" {
		fmt.Fprint(out, cs.Bold("标签: "))
		fmt.Fprintln(out, labels)
	}
	if issue.IssueType != nil {
		fmt.Fprint(out, cs.Bold("类型: "))
		fmt.Fprintln(out, issue.IssueType.Name)
	}
	if issue.Parent != nil {
		fmt.Fprint(out, cs.Bold("父 issue: "))
		fmt.Fprintln(out, formatLinkedIssueRef(issue.Parent)+" "+issue.Parent.Title)
	}
	if blockedBy := formatLinkedIssueListWithTitle(issue.BlockedBy.Nodes); blockedBy != "" {
		fmt.Fprint(out, cs.Bold("被阻塞于: "))
		fmt.Fprintln(out, blockedBy)
	}
	if blocking := formatLinkedIssueListWithTitle(issue.Blocking.Nodes); blocking != "" {
		fmt.Fprint(out, cs.Bold("阻塞: "))
		fmt.Fprintln(out, blocking)
	}
	if projects := issueProjectList(*issue); projects != "" {
		fmt.Fprint(out, cs.Bold("项目: "))
		fmt.Fprintln(out, projects)
	}
	if issue.Milestone != nil {
		fmt.Fprint(out, cs.Bold("里程碑: "))
		fmt.Fprintln(out, issue.Milestone.Title)
	}

	// 正文
	var md string
	var err error
	if issue.Body == "" {
		md = fmt.Sprintf("\n  %s\n\n", cs.Muted("未提供描述"))
	} else {
		md, err = markdown.Render(issue.Body,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithWrap(opts.IO.TerminalWidth()))
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "\n%s\n", md)

	// 子issue部分
	if issue.SubIssuesSummary.Total > 0 {
		fmt.Fprintf(out, "%s · %d/%d (%d%%)\n",
			cs.Bold("子 issue"),
			issue.SubIssuesSummary.Completed,
			issue.SubIssuesSummary.Total,
			int(issue.SubIssuesSummary.PercentCompleted),
		)
		for _, sub := range issue.SubIssues.Nodes {
			stateColor := cs.Green
			stateLabel := "Open"
			if sub.State == "CLOSED" {
				stateColor = cs.Magenta
				stateLabel = "Closed"
			}
			fmt.Fprintf(out, "%s %s %s\n",
				stateColor(stateLabel),
				formatLinkedIssueRef(&sub),
				sub.Title,
			)
		}
		fmt.Fprintln(out)
	}

	// 评论
	if issue.Comments.TotalCount > 0 {
		preview := !opts.Comments
		comments, err := prShared.CommentList(opts.IO, issue.Comments, api.PullRequestReviews{}, preview)
		if err != nil {
			return err
		}
		fmt.Fprint(out, comments)
	}

	// 页脚
	fmt.Fprintf(out, cs.Muted("在 GitHub 上查看此 issue: %s\n"), issue.URL)

	return nil
}

// formatLinkedIssueRef 将issue引用格式化为 owner/repo#N。
func formatLinkedIssueRef(issue *api.LinkedIssue) string {
	return fmt.Sprintf("%s#%d", issue.Repository.NameWithOwner, issue.Number)
}

// formatLinkedIssueRefs 格式化一个以逗号分隔的关联issue
// 引用列表，不含标题。
func formatLinkedIssueRefs(issues []api.LinkedIssue) string {
	return joinLinkedIssues(issues, false)
}

// formatLinkedIssueListWithTitle 格式化一个以逗号分隔的关联
// issue引用列表，每个标题追加在引用之后。
func formatLinkedIssueListWithTitle(issues []api.LinkedIssue) string {
	return joinLinkedIssues(issues, true)
}

func joinLinkedIssues(issues []api.LinkedIssue, withTitle bool) string {
	if len(issues) == 0 {
		return ""
	}
	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = formatLinkedIssueRef(&issue)
		if withTitle {
			parts[i] += " " + issue.Title
		}
	}
	return strings.Join(parts, ", ")
}

func issueStateTitleWithColor(cs *iostreams.ColorScheme, issue *api.Issue) string {
	colorFunc := cs.ColorFromString(prShared.ColorForIssueState(*issue))
	state := "Open"
	if issue.State == "CLOSED" {
		state = "Closed"
	}
	return colorFunc(state)
}

func issueAssigneeList(issue api.Issue) string {
	if len(issue.Assignees.Nodes) == 0 {
		return ""
	}

	AssigneeNames := make([]string, 0, len(issue.Assignees.Nodes))
	for _, assignee := range issue.Assignees.Nodes {
		AssigneeNames = append(AssigneeNames, assignee.DisplayName())
	}

	list := strings.Join(AssigneeNames, ", ")
	if issue.Assignees.TotalCount > len(issue.Assignees.Nodes) {
		list += ", …"
	}
	return list
}

func issueProjectList(issue api.Issue) string {
	totalCount := issue.ProjectCards.TotalCount + issue.ProjectItems.TotalCount
	count := len(issue.ProjectCards.Nodes) + len(issue.ProjectItems.Nodes)

	if count == 0 {
		return ""
	}

	projectNames := make([]string, 0, count)

	for _, project := range issue.ProjectItems.Nodes {
		colName := project.Status.Name
		if colName == "" {
			colName = "无状态"
		}
		projectNames = append(projectNames, fmt.Sprintf("%s (%s)", project.Project.Title, colName))
	}

	// TODO: 在完全弃用后移除 v1 经典项目逻辑
	for _, project := range issue.ProjectCards.Nodes {
		colName := project.Column.Name
		if colName == "" {
			colName = "待分类"
		}
		projectNames = append(projectNames, fmt.Sprintf("%s (%s)", project.Project.Name, colName))
	}

	list := strings.Join(projectNames, ", ")
	if totalCount > count {
		list += ", …"
	}
	return list
}

func issueLabelList(issue *api.Issue, cs *iostreams.ColorScheme) string {
	if len(issue.Labels.Nodes) == 0 {
		return ""
	}

	// 忽略大小写排序
	sort.SliceStable(issue.Labels.Nodes, func(i, j int) bool {
		return strings.ToLower(issue.Labels.Nodes[i].Name) < strings.ToLower(issue.Labels.Nodes[j].Name)
	})

	labelNames := make([]string, len(issue.Labels.Nodes))
	for i, label := range issue.Labels.Nodes {
		if cs == nil {
			labelNames[i] = label.Name
		} else {
			labelNames[i] = cs.Label(label.Color, label.Name)
		}
	}

	return strings.Join(labelNames, ", ")
}
