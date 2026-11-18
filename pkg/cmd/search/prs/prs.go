package prs

import (
	"fmt"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmd/search/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/spf13/cobra"
)

func NewCmdPrs(f *cmdutil.Factory, runF func(*shared.IssuesOptions) error) *cobra.Command {
	var locked, merged bool
	var noAssignee, noLabel, noMilestone, noProject bool
	var order, sort string
	var appAuthor string
	var requestedReviewer string
	opts := &shared.IssuesOptions{
		Browser: f.Browser,
		Entity:  shared.PullRequests,
		IO:      f.IOStreams,
		Query: search.Query{Kind: search.KindIssues,
			Qualifiers: search.Qualifiers{Type: "pr"}},
	}

	cmd := &cobra.Command{
		Use:   "prs [<query>]",
		Short: "搜索 PR",
		// TODO advancedIssueSearchCleanup
		// 在 GHES 3.17 版本更新链接并移除此说明。
		Long: heredoc.Docf(`
			在 GitHub 上搜索 PR

			该命令支持使用 GitHub 搜索语法、参数与限定符 flags，或两者的组合来构造查询。

			GitHub 搜索语法文档见:
			<https://docs.github.com/search-github/searching-on-github/searching-issues-and-pull-requests>

			On supported GitHub hosts, advanced issue search syntax can be used in the
			search query. Advanced search syntax, like boolean operators %[1]sOR%[1]s/%[1]sAND%[1]s,
			and parentheses for nesting, must be passed as separate arguments, rather
			than within a quoted multi-word string. This syntax is not supported with
			%[1]s--web%[1]s. For more information about advanced issue search, see:
			<https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/filtering-and-searching-issues-and-pull-requests#building-advanced-filters-for-issues>

			有关处理包含连字符的搜索查询的更多信息，请运行 %[1]sgh search --help%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索匹配关键字集合 "fix" 和 "bug" 的 PR
			$ gh search prs fix bug

			# 搜索 cli 仓库中的草稿 PR
			$ gh search prs --repo=cli/cli --draft

			# 以独立参数的形式使用原始搜索限定符搜索 PR
			$ gh search prs is:merged author:monalisa

			# 搜索请求您审核的 open 状态 PR
			$ gh search prs --review-requested=@me --state=open

			# 搜索指派给自己的已合并 PR
			$ gh search prs --assignee=@me --merged

			# 搜索回应较多的 PR
			$ gh search prs --reactions=">100"

			# 搜索没有 "bug" 标签的 PR
			$ gh search prs -- -label:bug

			# 仅搜索未归档仓库中的 PR (默认搜索所有仓库)
			$ gh search prs --owner github --archived=false
		`),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && c.Flags().NFlag() == 0 {
				return cmdutil.FlagErrorf("请指定搜索关键字或 flag")
			}
			if opts.Query.Limit < 1 || opts.Query.Limit > shared.SearchMaxResults {
				return cmdutil.FlagErrorf("`--limit` 必须在 1 到 1000 之间")
			}
			if c.Flags().Changed("author") && c.Flags().Changed("app") {
				return cmdutil.FlagErrorf("只能指定 `--author` 或 `--app` 之一")
			}
			if c.Flags().Changed("app") {
				opts.Query.Qualifiers.Author = fmt.Sprintf("app/%s", appAuthor)
			}
			if c.Flags().Changed("order") {
				opts.Query.Order = order
			}
			if c.Flags().Changed("sort") {
				opts.Query.Sort = sort
			}
			if c.Flags().Changed("locked") && locked {
				if locked {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "locked")
				} else {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "unlocked")
				}
			}
			if c.Flags().Changed("merged") {
				if merged {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "merged")
				} else {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "unmerged")
				}
			}
			if c.Flags().Changed("no-assignee") && noAssignee {
				opts.Query.Qualifiers.No = append(opts.Query.Qualifiers.No, "assignee")
			}
			if c.Flags().Changed("no-label") && noLabel {
				opts.Query.Qualifiers.No = append(opts.Query.Qualifiers.No, "label")
			}
			if c.Flags().Changed("no-milestone") && noMilestone {
				opts.Query.Qualifiers.No = append(opts.Query.Qualifiers.No, "milestone")
			}
			if c.Flags().Changed("no-project") && noProject {
				opts.Query.Qualifiers.No = append(opts.Query.Qualifiers.No, "project")
			}
			if c.Flags().Changed("review-requested") {
				if strings.Contains(requestedReviewer, "/") {
					opts.Query.Qualifiers.TeamReviewRequested = requestedReviewer
				} else {
					opts.Query.Qualifiers.ReviewRequested = requestedReviewer
				}
			}
			opts.Query.Keywords = args
			if runF != nil {
				return runF(opts)
			}
			var err error
			opts.Searcher, err = shared.Searcher(f)
			if err != nil {
				return err
			}
			return shared.SearchIssues(opts)
		},
	}

	// 输出 flags
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, search.PullRequestFields)
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中打开搜索查询")

	// 查询参数 flags
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "获取结果的最大数量")
	cmdutil.StringEnumFlag(cmd, &order, "order", "", "desc", []string{"asc", "desc"}, "返回结果的排序方向，除非指定 '--sort' 选项，否则将被忽略")
	cmdutil.StringEnumFlag(cmd, &sort, "sort", "", "best-match",
		[]string{
			"comments",
			"reactions",
			"reactions-+1",
			"reactions--1",
			"reactions-smile",
			"reactions-thinking_face",
			"reactions-heart",
			"reactions-tada",
			"interactions",
			"created",
			"updated",
		}, "对获取的结果排序")

	// issue 查询限定符(qualifier) flags
	cmd.Flags().StringVar(&appAuthor, "app", "", "按 GitHub App 作者过滤")
	cmdutil.NilBoolFlag(cmd, &opts.Query.Qualifiers.Archived, "archived", "", "按仓库归档状态过滤 {true|false}")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Assignee, "assignee", "", "按被指派人过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Author, "author", "", "按作者过滤(使用 --app 按 GitHub App 过滤)")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Closed, "closed", "", "按关闭的 `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Commenter, "commenter", "", "按 `user` 的评论过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Comments, "comments", "", "按评论 `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Created, "created", "", "按创建的 `date` 过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.In, "match", "", nil, []string{"title", "body", "comments"}, "将搜索限制在 issue 的特定字段")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Interactions, "interactions", "", "按回复和评论的 `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Involves, "involves", "", "按 `user` 的参与过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.Is, "visibility", "", nil, []string{"public", "private", "internal"}, "按仓库可见性过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.Label, "label", nil, "按标签过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Language, "language", "", "按编程语言过滤")
	cmd.Flags().BoolVar(&locked, "locked", false, "按会话锁定状态过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Mentions, "mentions", "", "按 `user` 提及过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Milestone, "milestone", "", "按里程碑 `title` 过滤")
	cmd.Flags().BoolVar(&noAssignee, "no-assignee", false, "按缺少被指派人过滤")
	cmd.Flags().BoolVar(&noLabel, "no-label", false, "按缺少标签过滤")
	cmd.Flags().BoolVar(&noMilestone, "no-milestone", false, "按缺少里程碑过滤")
	cmd.Flags().BoolVar(&noProject, "no-project", false, "按缺少项目过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Project, "project", "", "按项目看板 `owner/number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Reactions, "reactions", "", "按回复的 `number` 过滤")
	cmd.Flags().StringSliceVarP(&opts.Query.Qualifiers.Repo, "repo", "R", nil, "按仓库过滤，格式为 `OWNER/REPO`")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Qualifiers.State, "state", "", "", []string{"open", "closed"}, "按状态过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Team, "team-mentions", "", "按团队提及过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Updated, "updated", "", "按最后更新的 `date` 过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.User, "owner", nil, "按仓库所有者过滤")

	// 拉取请求(PR)查询限定符(qualifier) flags
	cmd.Flags().StringVarP(&opts.Query.Qualifiers.Base, "base", "B", "", "按基准分支名称过滤")
	cmdutil.NilBoolFlag(cmd, &opts.Query.Qualifiers.Draft, "draft", "", "按草稿状态过滤")
	cmd.Flags().StringVarP(&opts.Query.Qualifiers.Head, "head", "H", "", "按源分支名称过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Merged, "merged-at", "", "按合并的 `date` 过滤")
	cmd.Flags().BoolVar(&merged, "merged", false, "按合并状态过滤")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Qualifiers.Review, "review", "", "", []string{"none", "required", "approved", "changes_requested"}, "按审核状态过滤")
	cmd.Flags().StringVar(&requestedReviewer, "review-requested", "", "按被请求审核的 `user` 或团队过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.ReviewedBy, "reviewed-by", "", "按进行审核的 `user` 过滤")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Qualifiers.Status, "checks", "", "", []string{"pending", "success", "failure"}, "按检查状态过滤")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "base", "head")

	return cmd
}
