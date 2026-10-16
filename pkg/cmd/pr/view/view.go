package view

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	IO      *iostreams.IOStreams
	Browser browser.Browser
	// TODO projectsV1Deprecation
	// 该探测器(detector)仅用于测试校验，请移除它。
	Detector fd.Detector

	Finder   shared.PRFinder
	Exporter cmdutil.Exporter

	SelectorArg string
	BrowserMode bool
	Comments    bool

	Now func() time.Time
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:      f.IOStreams,
		Browser: f.Browser,
		Now:     time.Now,
	}

	cmd := &cobra.Command{
		Use:   "view [<number> | <url> | <branch>]",
		Short: "查看拉取请求(PR)",
		Long: heredoc.Docf(`
			显示拉取请求(PR)的标题、正文及其他信息。

			不带参数时，将显示属于当前分支的拉取请求。

			使用 %[1]s--web%[1]s flag 时，改为在 web 浏览器中打开该拉取请求。
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.MutuallyExclusive("只能指定 --comments 或 --json 之一",
				opts.Comments, opts.Exporter != nil); err != nil {
				return err
			}

			opts.Finder = shared.NewFinder(f)

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 --repo flag 时必须提供位置参数")
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.BrowserMode, "web", "w", false, "在浏览器中打开拉取请求(PR)")
	cmd.Flags().BoolVarP(&opts.Comments, "comments", "c", false, "查看拉取请求(PR)的评论")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.PullRequestFields)

	return cmd
}

var defaultFields = []string{
	"url", "number", "title", "state", "body", "author", "autoMergeRequest",
	"isDraft", "maintainerCanModify", "mergeable", "additions", "deletions", "commitsCount",
	"baseRefName", "headRefName", "headRepositoryOwner", "headRepository", "isCrossRepository",
	"reviewRequests", "reviews", "assignees", "labels", "projectCards", "projectItems", "milestone",
	"comments", "reactionGroups", "createdAt", "statusCheckRollup",
}

func viewRun(opts *ViewOptions) error {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   defaultFields,
		Detector: opts.Detector,
	}
	if opts.BrowserMode {
		findOptions.Fields = []string{"url"}
	} else if opts.Exporter != nil {
		findOptions.Fields = opts.Exporter.Fields()
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	connectedToTerminal := opts.IO.IsStdoutTTY()

	if opts.BrowserMode {
		openURL := pr.URL
		if connectedToTerminal {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, pr)
	}

	if connectedToTerminal {
		return printHumanPrPreview(opts, baseRepo, pr)
	}

	if opts.Comments {
		fmt.Fprint(opts.IO.Out, shared.RawCommentList(pr.Comments, pr.DisplayableReviews()))
		return nil
	}

	return printRawPrPreview(opts.IO, pr)
}

func printRawPrPreview(io *iostreams.IOStreams, pr *api.PullRequest) error {
	out := io.Out
	cs := io.ColorScheme()

	reviewers := prReviewerList(*pr, cs)
	assignees := prAssigneeList(*pr)
	labels := prLabelList(*pr, cs)
	projects := prProjectList(*pr)

	fmt.Fprintf(out, "title:\t%s\n", pr.Title)
	fmt.Fprintf(out, "state:\t%s\n", prStateWithDraft(pr))
	fmt.Fprintf(out, "author:\t%s\n", pr.Author.DisplayName())
	fmt.Fprintf(out, "labels:\t%s\n", labels)
	fmt.Fprintf(out, "assignees:\t%s\n", assignees)
	fmt.Fprintf(out, "reviewers:\t%s\n", reviewers)
	fmt.Fprintf(out, "projects:\t%s\n", projects)
	var milestoneTitle string
	if pr.Milestone != nil {
		milestoneTitle = pr.Milestone.Title
	}
	fmt.Fprintf(out, "milestone:\t%s\n", milestoneTitle)
	fmt.Fprintf(out, "number:\t%d\n", pr.Number)
	fmt.Fprintf(out, "url:\t%s\n", pr.URL)
	fmt.Fprintf(out, "additions:\t%s\n", cs.Green(strconv.Itoa(pr.Additions)))
	fmt.Fprintf(out, "deletions:\t%s\n", cs.Red(strconv.Itoa(pr.Deletions)))
	var autoMerge string
	if pr.AutoMergeRequest == nil {
		autoMerge = "已禁用"
	} else {
		autoMerge = fmt.Sprintf("已启用\t%s\t%s",
			pr.AutoMergeRequest.EnabledBy.Login,
			strings.ToLower(pr.AutoMergeRequest.MergeMethod))
	}
	fmt.Fprintf(out, "auto-merge:\t%s\n", autoMerge)

	fmt.Fprintln(out, "--")
	fmt.Fprintln(out, pr.Body)

	return nil
}

func printHumanPrPreview(opts *ViewOptions, baseRepo ghrepo.Interface, pr *api.PullRequest) error {
	out := opts.IO.Out
	cs := opts.IO.ColorScheme()

	// 头部(标题与状态)
	fmt.Fprintf(out, "%s %s#%d\n", cs.Bold(pr.Title), ghrepo.FullName(baseRepo), pr.Number)
	fmt.Fprintf(out,
		"%s • %s 想将 %s 合并到 %s，来源分支为 %s • %s\n",
		shared.StateTitleWithColor(cs, *pr),
		pr.Author.DisplayName(),
		text.Pluralize(pr.Commits.TotalCount, "个提交"),
		pr.BaseRefName,
		pr.HeadRefName,
		text.FuzzyAgo(opts.Now(), pr.CreatedAt),
	)

	// 新增/删除
	fmt.Fprintf(out,
		"%s %s",
		cs.Green("+"+strconv.Itoa(pr.Additions)),
		cs.Red("-"+strconv.Itoa(pr.Deletions)),
	)

	// 检查
	checks := pr.ChecksStatus()
	if summary := shared.PrCheckStatusSummaryWithColor(cs, checks); summary != "" {
		fmt.Fprintf(out, " • %s\n", summary)
	} else {
		fmt.Fprintln(out)
	}

	// 回应(reaction)
	if reactions := shared.ReactionGroupList(pr.ReactionGroups); reactions != "" {
		fmt.Fprint(out, reactions)
		fmt.Fprintln(out)
	}

	// 元数据
	if reviewers := prReviewerList(*pr, cs); reviewers != "" {
		fmt.Fprint(out, cs.Bold("审核者: "))
		fmt.Fprintln(out, reviewers)
	}
	if assignees := prAssigneeList(*pr); assignees != "" {
		fmt.Fprint(out, cs.Bold("被指派人: "))
		fmt.Fprintln(out, assignees)
	}
	if labels := prLabelList(*pr, cs); labels != "" {
		fmt.Fprint(out, cs.Bold("标签: "))
		fmt.Fprintln(out, labels)
	}
	if projects := prProjectList(*pr); projects != "" {
		fmt.Fprint(out, cs.Bold("项目: "))
		fmt.Fprintln(out, projects)
	}
	if pr.Milestone != nil {
		fmt.Fprint(out, cs.Bold("里程碑: "))
		fmt.Fprintln(out, pr.Milestone.Title)
	}

	// 自动合并(auto-merge)状态
	autoMerge := pr.AutoMergeRequest
	if autoMerge != nil {
		var mergeMethod string
		switch autoMerge.MergeMethod {
		case "MERGE":
			mergeMethod = "合并提交(merge commit)"
		case "REBASE":
			mergeMethod = "变基并合并(rebase and merge)"
		case "SQUASH":
			mergeMethod = "压缩并合并(squash and merge)"
		default:
			mergeMethod = fmt.Sprintf("未知的合并方式(%s)", autoMerge.MergeMethod)
		}
		fmt.Fprintf(out,
			"%s %s，由 %s 操作，使用 %s\n",
			cs.Bold("自动合并:"),
			cs.Green("已启用"),
			autoMerge.EnabledBy.Login,
			mergeMethod,
		)
	}

	// 正文
	var md string
	var err error
	if pr.Body == "" {
		md = fmt.Sprintf("\n  %s\n\n", cs.Muted("未提供描述"))
	} else {
		md, err = markdown.Render(pr.Body,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithWrap(opts.IO.TerminalWidth()))
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "\n%s\n", md)

	// 审核与评论
	if pr.Comments.TotalCount > 0 || pr.Reviews.TotalCount > 0 {
		preview := !opts.Comments
		comments, err := shared.CommentList(opts.IO, pr.Comments, pr.DisplayableReviews(), preview)
		if err != nil {
			return err
		}
		fmt.Fprint(out, comments)
	}

	// 页脚
	fmt.Fprintf(out, cs.Muted("在 GitHub 上查看此拉取请求(PR): %s\n"), pr.URL)

	return nil
}

const (
	requestedReviewState        = "REQUESTED" // 这是我们为审核请求自定义的状态
	approvedReviewState         = "APPROVED"
	changesRequestedReviewState = "CHANGES_REQUESTED"
	commentedReviewState        = "COMMENTED"
	dismissedReviewState        = "DISMISSED"
	pendingReviewState          = "PENDING"
)

type reviewerState struct {
	Name  string
	State string
}

// formattedReviewerState 用状态对应的颜色格式化 reviewerState
func formattedReviewerState(cs *iostreams.ColorScheme, reviewer *reviewerState) string {
	var displayState string
	switch reviewer.State {
	case requestedReviewState:
		displayState = cs.Yellow("已请求(Requested)")
	case approvedReviewState:
		displayState = cs.Green("已批准(Approved)")
	case changesRequestedReviewState:
		displayState = cs.Red("已请求变更(Changes requested)")
	case commentedReviewState, dismissedReviewState:
		// 将 "DISMISSED" 的审核显示为 "COMMENTED"，因为 "已驳回(dismissed)"
		// 只在事件时间线中有意义，不适合出现在最终统计里。
		displayState = "已评论(Commented)"
	default:
		displayState = text.Title(reviewer.State)
	}

	return fmt.Sprintf("%s (%s)", reviewer.Name, displayState)
}

// prReviewerList 生成带各自最后状态的审核者(reviewer)列表
func prReviewerList(pr api.PullRequest, cs *iostreams.ColorScheme) string {
	reviewerStates := parseReviewers(pr)
	reviewers := make([]string, 0, len(reviewerStates))

	sortReviewerStates(reviewerStates)

	for _, reviewer := range reviewerStates {
		reviewers = append(reviewers, formattedReviewerState(cs, reviewer))
	}

	reviewerList := strings.Join(reviewers, ", ")

	return reviewerList
}

const ghostName = "ghost"

// parseReviewers 解析给定的审核(review)与审核请求
func parseReviewers(pr api.PullRequest) []*reviewerState {
	reviewerStates := make(map[string]*reviewerState)

	for _, review := range pr.Reviews.Nodes {
		if review.Author.Login != pr.Author.Login {
			name := review.AuthorLogin()
			if name == "" {
				name = ghostName
			}
			reviewerStates[name] = &reviewerState{
				Name:  name,
				State: review.State,
			}
		}
	}

	// 若同一审核者已存在审核请求，则覆盖其状态。
	for _, reviewRequest := range pr.ReviewRequests.Nodes {
		name := reviewRequest.RequestedReviewer.DisplayName()
		reviewerStates[name] = &reviewerState{
			Name:  name,
			State: requestedReviewState,
		}
	}

	// 将 map 转换为切片以便排序
	result := make([]*reviewerState, 0, len(reviewerStates))
	for _, reviewer := range reviewerStates {
		if reviewer.State == pendingReviewState {
			continue
		}
		result = append(result, reviewer)
	}

	return result
}

// sortReviewerStates 将已完成的审核排在审核请求之前，并按名称的字母顺序排序
func sortReviewerStates(reviewerStates []*reviewerState) {
	sort.Slice(reviewerStates, func(i, j int) bool {
		if reviewerStates[i].State == requestedReviewState &&
			reviewerStates[j].State != requestedReviewState {
			return false
		}
		if reviewerStates[j].State == requestedReviewState &&
			reviewerStates[i].State != requestedReviewState {
			return true
		}

		return reviewerStates[i].Name < reviewerStates[j].Name
	})
}

func prAssigneeList(pr api.PullRequest) string {
	if len(pr.Assignees.Nodes) == 0 {
		return ""
	}

	AssigneeNames := make([]string, 0, len(pr.Assignees.Nodes))
	for _, assignee := range pr.Assignees.Nodes {
		AssigneeNames = append(AssigneeNames, assignee.DisplayName())
	}

	list := strings.Join(AssigneeNames, ", ")
	if pr.Assignees.TotalCount > len(pr.Assignees.Nodes) {
		list += ", …"
	}
	return list
}

func prLabelList(pr api.PullRequest, cs *iostreams.ColorScheme) string {
	if len(pr.Labels.Nodes) == 0 {
		return ""
	}

	// 忽略大小写排序
	sort.SliceStable(pr.Labels.Nodes, func(i, j int) bool {
		return strings.ToLower(pr.Labels.Nodes[i].Name) < strings.ToLower(pr.Labels.Nodes[j].Name)
	})

	labelNames := make([]string, 0, len(pr.Labels.Nodes))
	for _, label := range pr.Labels.Nodes {
		labelNames = append(labelNames, cs.Label(label.Color, label.Name))
	}

	list := strings.Join(labelNames, ", ")
	if pr.Labels.TotalCount > len(pr.Labels.Nodes) {
		list += ", …"
	}
	return list
}

func prProjectList(pr api.PullRequest) string {
	totalCount := pr.ProjectCards.TotalCount + pr.ProjectItems.TotalCount
	count := len(pr.ProjectCards.Nodes) + len(pr.ProjectItems.Nodes)

	if count == 0 {
		return ""
	}

	projectNames := make([]string, 0, len(pr.ProjectCards.Nodes))

	for _, project := range pr.ProjectItems.Nodes {
		colName := project.Status.Name
		if colName == "" {
			colName = "无状态"
		}
		projectNames = append(projectNames, fmt.Sprintf("%s (%s)", project.Project.Title, colName))
	}

	for _, project := range pr.ProjectCards.Nodes {
		if project == nil {
			continue
		}
		colName := project.Column.Name
		if colName == "" {
			colName = "待分类(triage)"
		}
		projectNames = append(projectNames, fmt.Sprintf("%s (%s)", project.Project.Name, colName))
	}

	list := strings.Join(projectNames, ", ")
	if totalCount > count {
		list += ", …"
	}
	return list
}

func prStateWithDraft(pr *api.PullRequest) string {
	if pr.IsDraft && pr.State == "OPEN" {
		return "DRAFT"
	}

	return pr.State
}
