package issues

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmd/search/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/spf13/cobra"
)

func NewCmdIssues(f *cmdutil.Factory, runF func(*shared.IssuesOptions) error) *cobra.Command {
	var locked, includePrs bool
	var noAssignee, noLabel, noMilestone, noProject bool
	var order, sort string
	var appAuthor string
	var searchType string
	opts := &shared.IssuesOptions{
		Browser: f.Browser,
		Entity:  shared.Issues,
		IO:      f.IOStreams,
		Query: search.Query{Kind: search.KindIssues,
			Qualifiers: search.Qualifiers{Type: "issue"}},
	}

	cmd := &cobra.Command{
		Use:   "issues [<query>]",
		Short: "搜索 issue",
		// TODO advancedIssueSearchCleanup
		// 在 GHES 3.17 版本更新链接并移除此说明。
		Long: heredoc.Docf(`
			在 GitHub 上搜索 issue。

			该命令支持使用 GitHub 搜索语法、参数与限定符(qualifier) flags，或两者的组合来构造查询。

			GitHub 搜索语法文档见:
			<https://docs.github.com/search-github/searching-on-github/searching-issues-and-pull-requests>

			在受支持的 GitHub 主机上，可在 %[1]s--search%[1]s 查询中使用高级 issue 搜索语法。
			高级搜索语法，如布尔运算符 %[1]sOR%[1]s/%[1]sAND%[1]s 以及用于嵌套的括号，
			必须作为独立参数传递，而不能放在带引号的多词字符串中。
			%[1]s--web%[1]s 不支持该语法。有关高级 issue 搜索的更多信息，请参见:
			<https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/filtering-and-searching-issues-and-pull-requests#building-advanced-filters-for-issues>

			使用 %[1]s--search-type%[1]s 可选择语义(semantic)或混合(hybrid, 关键字 + 语义)排序，
			而非默认的词法(lexical)搜索。语义与混合搜索仅限于 issue，按相关性排序
			(因此不能使用 %[1]s--sort%[1]s 和 %[1]s--order%[1]s)，仅返回单页结果，
			且在 GitHub Enterprise Server 上不可用。

			有关处理包含连字符的搜索查询的更多信息，请运行 %[1]sgh search --help%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索匹配关键字集合 "readme" 和 "typo" 的 issue
			$ gh search issues readme typo

			# 搜索匹配短语 "broken feature" 的 issue
			$ gh search issues "broken feature"

			# 以独立参数的形式使用原始搜索限定符搜索 issue
			$ gh search issues label:bug author:monalisa state:open

			# 搜索 cli 组织中的 issue 和拉取请求(PR)
			$ gh search issues --include-prs --owner=cli

			# 搜索指派给自己的 open 状态 issue
			$ gh search issues --assignee=@me --state=open

			# 搜索评论较多的 issue
			$ gh search issues --comments=">100"

			# 搜索没有 "bug" 标签的 issue
			$ gh search issues -- -label:bug

			# 仅搜索未归档仓库中的 issue (默认搜索所有仓库)
			$ gh search issues --owner github --archived=false

			# 使用语义(自然语言)排序搜索 issue
			$ gh search issues "feature broken on web" --search-type semantic

			# 使用混合(关键字 + 语义)排序搜索 issue
			$ gh search issues "feature broken" --search-type hybrid
		`),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && c.Flags().NFlag() == 0 {
				return cmdutil.FlagErrorf("请指定搜索关键字或 flag")
			}
			if opts.Query.Limit < 1 || opts.Query.Limit > shared.SearchMaxResults {
				return cmdutil.FlagErrorf("`--limit` must be between 1 and 1000")
			}
			if c.Flags().Changed("author") && c.Flags().Changed("app") {
				return cmdutil.FlagErrorf("只能指定 `--author` 或 `--app` 之一")
			}
			semanticSearch := searchType == "semantic" || searchType == "hybrid"
			if semanticSearch && opts.WebMode {
				return cmdutil.FlagErrorf("`--web` 不支持与 %s 搜索同时使用", searchType)
			}
			if semanticSearch && includePrs {
				return cmdutil.FlagErrorf("%s 搜索仅限于 issue，不能与 `--include-prs` 组合使用", searchType)
			}
			if semanticSearch && (c.Flags().Changed("sort") || c.Flags().Changed("order")) {
				return cmdutil.FlagErrorf("`--sort` 和 `--order` 不支持与 %s 搜索同时使用", searchType)
			}
			if c.Flags().Changed("app") {
				opts.Query.Qualifiers.Author = fmt.Sprintf("app/%s", appAuthor)
			}
			if includePrs {
				opts.Entity = shared.Both
				opts.Query.Qualifiers.Type = ""
			}
			if searchType != "lexical" {
				// 我们不提交 `lexical` 作为搜索类型，因为它是 API 的默认行为。
				opts.Query.IssueSearchType = searchType
			}
			if c.Flags().Changed("order") {
				opts.Query.Order = order
			}
			if c.Flags().Changed("sort") {
				opts.Query.Sort = sort
			}
			if c.Flags().Changed("locked") {
				if locked {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "locked")
				} else {
					opts.Query.Qualifiers.Is = append(opts.Query.Qualifiers.Is, "unlocked")
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
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, search.IssueFields)
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中打开搜索查询")

	// 查询参数 flags
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "获取结果的最大数量")
	cmdutil.StringEnumFlag(cmd, &order, "order", "", "desc", []string{"asc", "desc"}, "返回结果的排序方向，除非指定 '--sort' flag，否则将被忽略")
	cmdutil.StringEnumFlag(cmd, &sort, "sort", "", "best-match",
		[]string{
			"comments",
			"created",
			"interactions",
			"reactions",
			"reactions-+1",
			"reactions--1",
			"reactions-heart",
			"reactions-smile",
			"reactions-tada",
			"reactions-thinking_face",
			"updated",
		}, "对获取的结果排序")

	cmdutil.StringEnumFlag(cmd, &searchType, "search-type", "", "lexical", []string{"lexical", "semantic", "hybrid"}, "要执行的 issue 搜索类型")

	// 查询限定符(qualifier) flags
	cmd.Flags().BoolVar(&includePrs, "include-prs", false, "在结果中包含拉取请求(PR)")
	cmd.Flags().StringVar(&appAuthor, "app", "", "按 GitHub App 作者过滤")
	cmdutil.NilBoolFlag(cmd, &opts.Query.Qualifiers.Archived, "archived", "", "按仓库归档状态过滤 {true|false}")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Assignee, "assignee", "", "按被指派人(assignee)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Author, "author", "", "按作者过滤(使用 --app 按 GitHub App 过滤)")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Closed, "closed", "", "按关闭的 `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Commenter, "commenter", "", "按 `user` 的评论过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Comments, "comments", "", "按评论 `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Created, "created", "", "按创建的 `date` 过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.In, "match", "", nil, []string{"title", "body", "comments"}, "将搜索限制在 issue 的特定字段")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Interactions, "interactions", "", "按回应(reaction)和评论的 `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Involves, "involves", "", "按 `user` 的参与过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.Is, "visibility", "", nil, []string{"public", "private", "internal"}, "按仓库可见性过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.Label, "label", nil, "按标签(label)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Language, "language", "", "按编程语言过滤")
	cmd.Flags().BoolVar(&locked, "locked", false, "按会话锁定状态过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Mentions, "mentions", "", "按 `user` 提及(mention)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Milestone, "milestone", "", "按里程碑(milestone) `title` 过滤")
	cmd.Flags().BoolVar(&noAssignee, "no-assignee", false, "按缺少被指派人过滤")
	cmd.Flags().BoolVar(&noLabel, "no-label", false, "按缺少标签过滤")
	cmd.Flags().BoolVar(&noMilestone, "no-milestone", false, "按缺少里程碑过滤")
	cmd.Flags().BoolVar(&noProject, "no-project", false, "按缺少项目(project)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Project, "project", "", "按项目看板 `owner/number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Reactions, "reactions", "", "按回应(reaction)的 `number` 过滤")
	cmd.Flags().StringSliceVarP(&opts.Query.Qualifiers.Repo, "repo", "R", nil, "按仓库过滤，格式为 `OWNER/REPO`")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Qualifiers.State, "state", "", "", []string{"open", "closed"}, "按状态过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Team, "team-mentions", "", "按团队提及(mention)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Updated, "updated", "", "按最后更新的 `date` 过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.User, "owner", nil, "按仓库所有者过滤")

	return cmd
}
