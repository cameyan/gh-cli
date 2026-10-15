package commits

import (
	"fmt"
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

type CommitsOptions struct {
	Browser  browser.Browser
	Exporter cmdutil.Exporter
	IO       *iostreams.IOStreams
	Now      time.Time
	Query    search.Query
	Searcher search.Searcher
	WebMode  bool
}

func NewCmdCommits(f *cmdutil.Factory, runF func(*CommitsOptions) error) *cobra.Command {
	var order string
	var sort string
	opts := &CommitsOptions{
		Browser: f.Browser,
		IO:      f.IOStreams,
		Query:   search.Query{Kind: search.KindCommits},
	}

	cmd := &cobra.Command{
		Use:   "commits [<query>]",
		Short: "搜索提交(commit)",
		Long: heredoc.Docf(`
			在 GitHub 上搜索提交(commit)。

			该命令支持使用 GitHub 搜索语法、参数与限定符(qualifier) flags，或两者的组合来构造查询。

			GitHub 搜索语法文档见:
			<https://docs.github.com/search-github/searching-on-github/searching-commits>

			有关处理包含连字符的搜索查询的更多信息，请运行 %[1]sgh search --help%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索匹配关键字集合 "readme" 和 "typo" 的提交
			$ gh search commits readme typo

			# 搜索匹配短语 "bug fix" 的提交
			$ gh search commits "bug fix"

			# 以独立参数的形式使用原始搜索限定符搜索提交
			$ gh search commits fix author:monalisa merge:false

			# 搜索由用户 "monalisa" 提交(commit)的提交
			$ gh search commits --committer=monalisa

			# 搜索作者名为 "Jane Doe" 的用户的提交
			$ gh search commits --author-name="Jane Doe"

			# 搜索匹配哈希 "8dd03144ffdc6c0d486d6b705f9c7fba871ee7c3" 的提交
			$ gh search commits --hash=8dd03144ffdc6c0d486d6b705f9c7fba871ee7c3

			# 搜索 2022 年 2 月 1 日之前提交(authored)的提交
			$ gh search commits --author-date="<2022-02-01"
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
			return commitsRun(opts)
		},
	}

	// 输出 flags
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, search.CommitFields)
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中打开搜索查询")

	// 查询参数 flags
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "获取提交的最大数量")
	cmdutil.StringEnumFlag(cmd, &order, "order", "", "desc", []string{"asc", "desc"}, "返回提交的排序方向，除非指定 '--sort' flag，否则将被忽略")
	cmdutil.StringEnumFlag(cmd, &sort, "sort", "", "best-match", []string{"author-date", "committer-date"}, "对获取的提交排序")

	// 查询限定符(qualifier) flags
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Author, "author", "", "按作者过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.AuthorDate, "author-date", "", "按创作(author) `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.AuthorEmail, "author-email", "", "按作者邮箱过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.AuthorName, "author-name", "", "按作者姓名过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Committer, "committer", "", "按提交者(committer)过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.CommitterDate, "committer-date", "", "按提交(commit) `date` 过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.CommitterEmail, "committer-email", "", "按提交者邮箱过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.CommitterName, "committer-name", "", "按提交者姓名过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Hash, "hash", "", "按提交哈希过滤")
	cmdutil.NilBoolFlag(cmd, &opts.Query.Qualifiers.Merge, "merge", "", "按合并(merge)提交过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Parent, "parent", "", "按父提交哈希过滤")
	cmd.Flags().StringSliceVarP(&opts.Query.Qualifiers.Repo, "repo", "R", nil, "按仓库过滤，格式为 `OWNER/REPO`")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Tree, "tree", "", "按树(tree)哈希过滤")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.User, "owner", nil, "按仓库所有者过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.Is, "visibility", "", nil, []string{"public", "private", "internal"}, "按仓库可见性过滤")

	return cmd
}

func commitsRun(opts *CommitsOptions) error {
	io := opts.IO
	if opts.WebMode {
		url := opts.Searcher.URL(opts.Query)
		if io.IsStdoutTTY() {
			fmt.Fprintf(io.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(url))
		}
		return opts.Browser.Browse(url)
	}
	io.StartProgressIndicator()
	result, err := opts.Searcher.Commits(opts.Query)
	io.StopProgressIndicator()
	if err != nil {
		return err
	}
	if len(result.Items) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("没有匹配您搜索的提交(commit)")
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

func displayResults(io *iostreams.IOStreams, now time.Time, results search.CommitsResult) error {
	if now.IsZero() {
		now = time.Now()
	}
	cs := io.ColorScheme()
	tp := tableprinter.New(io, tableprinter.WithHeader("仓库", "SHA", "消息", "作者", "创建时间"))
	for _, commit := range results.Items {
		tp.AddField(commit.Repo.FullName)
		tp.AddField(commit.Sha)
		tp.AddField(text.RemoveExcessiveWhitespace(commit.Info.Message))
		tp.AddField(commit.Author.Login)
		tp.AddTimeField(now, commit.Info.Author.Date, cs.Muted)
		tp.EndRow()
	}
	if io.IsStdoutTTY() {
		header := fmt.Sprintf("显示 %d 条，共 %d 条提交(commit)\n\n", len(results.Items), results.Total)
		fmt.Fprintf(io.Out, "\n%s", header)
	}
	return tp.Render()
}
