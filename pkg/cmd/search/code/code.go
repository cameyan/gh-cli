package code

import (
	"fmt"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/search/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/search"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

type CodeOptions struct {
	Browser  browser.Browser
	Config   func() (gh.Config, error)
	Exporter cmdutil.Exporter
	IO       *iostreams.IOStreams
	Query    search.Query
	Searcher search.Searcher
	WebMode  bool
}

func NewCmdCode(f *cmdutil.Factory, runF func(*CodeOptions) error) *cobra.Command {
	opts := &CodeOptions{
		Browser: f.Browser,
		Config:  f.Config,
		IO:      f.IOStreams,
		Query:   search.Query{Kind: search.KindCode},
	}

	cmd := &cobra.Command{
		Use:   "code <query>",
		Short: "在代码中搜索",
		Long: heredoc.Docf(`
			在 GitHub 仓库的代码中搜索。

			搜索语法文档见:
			<https://docs.github.com/search-github/searching-on-github/searching-code>

			注意: 这些搜索结果由现已属于遗留的 GitHub 代码搜索引擎提供。
			结果可能与 %[1]sgithub.com%[1]s 上看到的不一致，且正则搜索等新功能尚未通过 GitHub API 提供。

			有关处理包含连字符的搜索查询的更多信息，请运行 %[1]sgh search --help%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索同时匹配 "react" 和 "lifecycle" 的代码
			$ gh search code react lifecycle

			# 搜索匹配 "error handling" 的代码
			$ gh search code "error handling"

			# 以独立参数的形式使用原始搜索限定符搜索代码
			$ gh search code panic path:pkg language:go

			# 在 Python 文件中搜索匹配 "deque" 的代码
			$ gh search code deque --language=python

			# 在 microsoft 组织拥有的仓库中搜索匹配 "cli" 的代码
			$ gh search code cli --owner=microsoft

			# 在 GitHub CLI 仓库中搜索匹配 "panic" 的代码
			$ gh search code panic --repo cli/cli

			# 在 package.json 文件中搜索关键字 "lint"
			$ gh search code lint --filename package.json
		`),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && c.Flags().NFlag() == 0 {
				return cmdutil.FlagErrorf("请指定搜索关键字或 flag")
			}
			if opts.Query.Limit < 1 || opts.Query.Limit > shared.SearchMaxResults {
				return cmdutil.FlagErrorf("`--limit` 必须在 1 到 1000 之间")
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
			return codeRun(opts)
		},
	}

	// 输出 flags
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, search.CodeFields)
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中打开搜索查询")

	// 查询参数 flags
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "获取代码结果的最大数量")

	// 查询限定符(qualifier) flags
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Extension, "extension", "", "按文件扩展名过滤")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Filename, "filename", "", "按文件名过滤")
	cmdutil.StringSliceEnumFlag(cmd, &opts.Query.Qualifiers.In, "match", "", nil, []string{"file", "path"}, "将搜索限制在文件内容或文件路径")
	cmd.Flags().StringVarP(&opts.Query.Qualifiers.Language, "language", "", "", "按编程语言过滤结果")
	cmd.Flags().StringSliceVarP(&opts.Query.Qualifiers.Repo, "repo", "R", nil, "按仓库过滤，格式为 `OWNER/REPO`")
	cmd.Flags().StringVar(&opts.Query.Qualifiers.Size, "size", "", "按大小范围过滤，单位为千字节(KB)")
	cmd.Flags().StringSliceVar(&opts.Query.Qualifiers.User, "owner", nil, "按仓库所有者过滤")

	return cmd
}

func codeRun(opts *CodeOptions) error {
	io := opts.IO
	if opts.WebMode {
		// 将 `filename` 和 `extension` 遗留搜索限定符转换为新代码搜索的 `path`
		// 限定符，因为它们与 `--web` 不兼容。
		if opts.Query.Qualifiers.Filename != "" || opts.Query.Qualifiers.Extension != "" {
			cfg, err := opts.Config()
			if err != nil {
				return err
			}
			host, _ := cfg.Authentication().DefaultHost()
			// FIXME: 一旦 GHES 支持新的 `path` 搜索限定符，就移除此检查。
			if !ghauth.IsEnterprise(host) {
				filename := opts.Query.Qualifiers.Filename
				extension := opts.Query.Qualifiers.Extension
				if extension != "" && !strings.HasPrefix(extension, ".") {
					extension = "." + extension
				}
				opts.Query.Qualifiers.Filename = ""
				opts.Query.Qualifiers.Extension = ""
				opts.Query.Qualifiers.Path = fmt.Sprintf("%s%s", filename, extension)
			}
		}
		url := opts.Searcher.URL(opts.Query)
		if io.IsStdoutTTY() {
			fmt.Fprintf(io.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(url))
		}
		return opts.Browser.Browse(url)
	}
	io.StartProgressIndicator()
	results, err := opts.Searcher.Code(opts.Query)
	io.StopProgressIndicator()
	if err != nil {
		return err
	}
	if len(results.Items) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("没有匹配您搜索的代码结果")
	}
	if err := io.StartPager(); err == nil {
		defer io.StopPager()
	} else {
		fmt.Fprintf(io.ErrOut, "启动分页器失败: %v\n", err)
	}
	if opts.Exporter != nil {
		return opts.Exporter.Write(io, results.Items)
	}

	return displayResults(io, results)
}

func displayResults(io *iostreams.IOStreams, results search.CodeResult) error {
	cs := io.ColorScheme()
	if io.IsStdoutTTY() {
		fmt.Fprintf(io.Out, "\n显示 %d 条，共 %d 条结果\n\n", len(results.Items), results.Total)
		for i, code := range results.Items {
			if i > 0 {
				fmt.Fprint(io.Out, "\n")
			}
			fmt.Fprintf(io.Out, "%s %s\n", cs.Blue(code.Repository.FullName), cs.GreenBold(code.Path))
			for _, match := range code.TextMatches {
				lines := formatMatch(match.Fragment, match.Matches, io)
				for _, line := range lines {
					fmt.Fprintf(io.Out, "\t%s\n", strings.TrimSpace(line))
				}
			}
		}
		return nil
	}
	for _, code := range results.Items {
		for _, match := range code.TextMatches {
			lines := formatMatch(match.Fragment, match.Matches, io)
			for _, line := range lines {
				fmt.Fprintf(io.Out, "%s:%s: %s\n", cs.Blue(code.Repository.FullName), cs.GreenBold(code.Path), strings.TrimSpace(line))
			}
		}
	}
	return nil
}

func formatMatch(t string, matches []search.Match, io *iostreams.IOStreams) []string {
	cs := io.ColorScheme()

	startIndices := map[int]struct{}{}
	endIndices := map[int]struct{}{}
	for _, m := range matches {
		if len(m.Indices) < 2 {
			continue
		}
		startIndices[m.Indices[0]] = struct{}{}
		endIndices[m.Indices[1]] = struct{}{}
	}

	var lines []string
	var b strings.Builder
	var found, highlighting bool
	// A match can end at a newline or at the end of the fragment, where no
	// character carries the end index, so close any open highlight here.
	endLine := func() {
		if highlighting {
			b.WriteString(cs.Reset())
			highlighting = false
		}
		if found {
			lines = append(lines, b.String())
		}
		found = false
		b.Reset()
	}
	for i, c := range t {
		if c == '\n' {
			endLine()
			continue
		}
		if _, ok := startIndices[i]; ok {
			b.WriteString(cs.HighlightStart())
			found = true
			highlighting = true
		} else if _, ok := endIndices[i]; ok {
			b.WriteString(cs.Reset())
			highlighting = false
		}
		b.WriteRune(c)
	}
	endLine()
	return lines
}
