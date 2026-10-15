package repos

import (
	"fmt"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/search/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/spf13/cobra"
)

type ReposOptions struct {
	Browser  browser.Browser
	Exporter cmdutil.Exporter
	IO       *iostreams.IOStreams
	Now      time.Time
	Query    search.Query
	Searcher search.Searcher
	WebMode  bool
}

func NewCmdRepos(f *cmdutil.Factory, runF func(*ReposOptions) error) *cobra.Command {
	var order string
	var sort string
	opts := &ReposOptions{
		Browser: f.Browser,
		IO:      f.IOStreams,
		Query:   search.Query{Kind: search.KindRepositories},
	}

	cmd := &cobra.Command{
		Use:   "repos [<query>]",
		Short: "搜索仓库",
		Long: heredoc.Docf(`
			在 GitHub 上搜索仓库。

			该命令支持使用 GitHub 搜索语法、参数与限定符(qualifier) flags，或两者的组合来构造查询。

			GitHub 搜索语法文档见:
			<https://docs.github.com/search-github/searching-on-github/searching-for-repositories>

			有关处理包含连字符的搜索查询的更多信息，请运行 %[1]sgh search --help%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索匹配关键字集合 "cli" 和 "shell" 的仓库
			$ gh search repos cli shell

			# 搜索匹配短语 "vim plugin" 的仓库
			$ gh search repos "vim plugin"

			# 以独立参数的形式使用原始搜索限定符搜索仓库
			$ gh search repos topic:github 'stars:>5000'

			# 搜索 microsoft 组织中的公开(public)仓库
			$ gh search repos --owner=microsoft --visibility=public

			# 搜索具有一组主题(topic)的仓库
			$ gh search repos --topic=unix,terminal

			# 按编程语言和 "good first issue" 数量搜索仓库
			$ gh search repos --language=go --good-first-issues=">=10"

			# 搜索没有 "linux" 主题(topic)的仓库
			$ gh search repos -- -topic:linux

			# 搜索时排除已归档的仓库
			$ gh search repos --archived=false
		`),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && c.Flags().NFlag() == 0 {
				return cmdutil.FlagErrorf("请指定搜索关键字或 flag")
			}
			if opts.Query.Limit < 1 || opts.Query.Limit > shared.SearchMaxResults {
				return cmdutil.FlagErrorf("`--limit` must be between 1 and 1000")
			}
			if c.Flags().Changed("order") {
				opts.Query.Order = order
			}
			if c.Flags().Changed("sort") {
				opts.Query.Sort = sort
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
			return reposRun(opts)
		},
	}

	// 输出 flags
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, search.RepositoryFields)
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中打开搜索查询")

	// 查询参数 flags
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "获取仓库的最大数量")
	cmdutil.StringEnumFlag(cmd, &order, "order", "", "desc", []string{"asc", "desc"}, "返回仓库的排序方向，除非指定 '--sort' flag，否则将被忽略")
	cmdutil.StringEnumFlag(cmd, &sort, "sort", "", "best-match", []string{"forks", "help-wanted-issues", "stars", "updated"}, "对获取的仓库排序")

	// 查询限定符(qualifier) flags
	cmdutil.NilBoolFlag(cmd, &opts.Query.Qualifiers.Archived, "archived", "", "按仓库归档状态过滤 {true|false}")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Created, "created", "", "按创建的 `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Followers, "followers", "", "按关注者(follower) `number` 过滤")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Qualifiers.Fork, "include-forks", "", "", []string{"false", "true", "only"}, "在获取的仓库中包含 fork 仓库")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Forks, "forks", "", "按 fork 的 `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.GoodFirstIssues, "good-first-issues", "", "按带有 'good first issue' 标签的 issue `number` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.HelpWantedIssues, "help-wanted-issues", "", "按带有 'help wanted' 标签的 issue `number` 过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.In, "match", "", nil, []string{"name", "description", "readme"}, "将搜索限制在仓库的特定字段")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.Is, "visibility", "", nil, []string{"public", "private", "internal"}, "按可见性过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Language, "language", "", "按编程语言过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.License, "license", nil, "按许可证(license)类型过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Pushed, "updated", "", "按最后更新的 `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Size, "size", "", "按大小范围过滤，单位为千字节(KB)")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Stars, "stars", "", "按 star 的 `number` 过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.Topic, "topic", nil, "按主题(topic)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Topics, "number-topics", "", "按主题(topic)的 `number` 过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.User, "owner", nil, "按所有者(owner)过滤")

	return cmd
}

func reposRun(opts *ReposOptions) error {
	io := opts.IO
	if opts.WebMode {
		url := opts.Searcher.URL(opts.Query)
		if io.IsStdoutTTY() {
			fmt.Fprintf(io.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(url))
		}
		return opts.Browser.Browse(url)
	}
	io.StartProgressIndicator()
	result, err := opts.Searcher.Repositories(opts.Query)
	io.StopProgressIndicator()
	if err != nil {
		return err
	}
	if len(result.Items) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("没有匹配您搜索的仓库")
	}
	if err := io.StartPager(); err == nil {
		defer io.StopPager()
	} else {
		fmt.Fprintf(io.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}
	if opts.Exporter != nil {
		return opts.Exporter.Write(io, result.Items)
	}

	return displayResults(io, opts.Now, result)
}

func displayResults(io *iostreams.IOStreams, now time.Time, results search.RepositoriesResult) error {
	if now.IsZero() {
		now = time.Now()
	}
	cs := io.ColorScheme()
	tp := tableprinter.New(io, tableprinter.WithHeader("名称", "描述", "可见性", "更新时间"))
	for _, repo := range results.Items {
		tags := []string{visibilityLabel(repo)}
		if repo.IsFork {
			tags = append(tags, "fork")
		}
		if repo.IsArchived {
			tags = append(tags, "已归档")
		}
		info := strings.Join(tags, ", ")
		infoColor := cs.Muted
		if repo.IsPrivate {
			infoColor = cs.Yellow
		}
		tp.AddField(repo.FullName, tableprinter.WithColor(cs.Bold))
		tp.AddField(text.RemoveExcessiveWhitespace(repo.Description))
		tp.AddField(info, tableprinter.WithColor(infoColor))
		tp.AddTimeField(now, repo.UpdatedAt, cs.Muted)
		tp.EndRow()
	}
	if io.IsStdoutTTY() {
		header := fmt.Sprintf("显示 %d 条，共 %d 条仓库\n\n", len(results.Items), results.Total)
		fmt.Fprintf(io.Out, "\n%s", header)
	}
	return tp.Render()
}

func visibilityLabel(repo search.Repository) string {
	if repo.Visibility != "" {
		return repo.Visibility
	} else if repo.IsPrivate {
		return "private"
	}
	return "public"
}
