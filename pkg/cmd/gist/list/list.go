package list

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/gist/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)

	Limit          int
	Filter         *regexp.Regexp
	IncludeContent bool
	Visibility     string // all, secret, public
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
	}

	var flagPublic bool
	var flagSecret bool
	var flagFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出你的 gist",
		Long: heredoc.Docf(`
			列出你的用户账户下的 gist。

			可以使用 %[1]s--filter%[1]s 通过正则表达式筛选描述、文件名，
			甚至筛选 gist 中文件的内容。

			有关支持的正则表达式语法，请参见 <https://pkg.go.dev/regexp/syntax>。

			使用 %[1]s--include-content%[1]s 可包含文件内容，注意
			这会较慢并增加所用速率限制的消耗。此时不会打印表格，
			而是打印带高亮的代码，类似于 %[1]sgh search code%[1]s:

				{{gist ID}} {{file name}}
				    {{description}}
				        {{匹配的内容行}}

			当输出被重定向时，不打印高亮或其他颜色。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出你的用户账户下的所有私密 gist
			$ gh gist list --secret

			# 查找你的用户账户下任意位置提及 "octo" 的所有 gist
			$ gh gist list --filter octo --include-content
		`),
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %v", opts.Limit)
			}

			if flagFilter == "" {
				if opts.IncludeContent {
					return cmdutil.FlagErrorf("不能在没有 --filter 的情况下使用 --include-content")
				}
			} else {
				if filter, err := regexp.CompilePOSIX(flagFilter); err != nil {
					return err
				} else {
					opts.Filter = filter
				}
			}

			opts.Visibility = "all"
			if flagSecret {
				opts.Visibility = "secret"
			} else if flagPublic {
				opts.Visibility = "public"
			}

			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 10, "最多获取的 gist 数量")
	cmd.Flags().BoolVar(&flagPublic, "public", false, "仅显示公开 gist")
	cmd.Flags().BoolVar(&flagSecret, "secret", false, "仅显示私密 gist")
	cmd.Flags().StringVar(&flagFilter, "filter", "", "使用正则 `表达式` 筛选 gist")
	cmd.Flags().BoolVar(&opts.IncludeContent, "include-content", false, "筛选时包含 gist 的文件内容")

	return cmd
}

func listRun(opts *ListOptions) error {
	client, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	// Filtering can take a while so start the progress indicator. StopProgressIndicator will no-op if not running.
	if opts.Filter != nil {
		opts.IO.StartProgressIndicator()
	}
	gists, err := shared.ListGists(client, host, opts.Limit, opts.Filter, opts.IncludeContent, opts.Visibility)
	opts.IO.StopProgressIndicator()

	if err != nil {
		return err
	}

	if len(gists) == 0 {
		return cmdutil.NewNoResultsError("未找到 gist")
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	if opts.Filter != nil && opts.IncludeContent {
		return printContent(opts.IO, gists, opts.Filter)
	}

	return printTable(opts.IO, gists, opts.Filter)
}

func printTable(io *iostreams.IOStreams, gists []shared.Gist, filter *regexp.Regexp) error {
	cs := io.ColorScheme()
	tp := tableprinter.New(io, tableprinter.WithHeader("ID", "描述", "文件", "可见性", "更新时间"))

	// 打印表格时高亮描述中的筛选匹配项。
	highlightDescription := func(s string) string {
		if filter != nil {
			if str, err := highlightMatch(s, filter, nil, cs.Bold, cs.Highlight); err == nil {
				return str
			}
		}
		return cs.Bold(s)
	}

	// 当任一文件名与筛选条件匹配时高亮文件列。
	highlightFilesFunc := func(gist *shared.Gist) func(string) string {
		if filter != nil {
			for _, file := range gist.Files {
				if filter.MatchString(file.Filename) {
					return cs.Highlight
				}
			}
		}
		return normal
	}

	for _, gist := range gists {
		fileCount := len(gist.Files)

		visibility := "public"
		visColor := cs.Green
		if !gist.Public {
			visibility = "secret"
			visColor = cs.Red
		}

		description := gist.Description
		if description == "" {
			for filename := range gist.Files {
				if !strings.HasPrefix(filename, "gistfile") {
					description = filename
					break
				}
			}
		}

		tp.AddField(gist.ID)
		tp.AddField(
			text.RemoveExcessiveWhitespace(description),
			tableprinter.WithColor(highlightDescription),
		)
		tp.AddField(
			text.Pluralize(fileCount, "file"),
			tableprinter.WithColor(highlightFilesFunc(&gist)),
		)
		tp.AddField(visibility, tableprinter.WithColor(visColor))
		tp.AddTimeField(time.Now(), gist.UpdatedAt, cs.Muted)
		tp.EndRow()
	}

	return tp.Render()
}

// printContent 打印带可选描述和内容的 gist，类似于 `gh search code`，
// 包括高亮的匹配项，格式为:
//
//	{{gist ID}} {{file name}}
//	    {{description, if any}}
//	        {{content lines with matches, if any}}
//
// 若打印到非 TTY 流，格式相同但没有高亮。
func printContent(io *iostreams.IOStreams, gists []shared.Gist, filter *regexp.Regexp) error {
	const tab string = "    "
	cs := io.ColorScheme()

	out := &strings.Builder{}
	var filename, description string
	var err error
	split := func(r rune) bool {
		return r == '\n' || r == '\r'
	}

	for _, gist := range gists {
		for _, file := range gist.Files {
			matched := false
			out.Reset()

			if filename, err = highlightMatch(file.Filename, filter, &matched, cs.Green, cs.Highlight); err != nil {
				return err
			}
			fmt.Fprintln(out, cs.Blue(gist.ID), filename)

			if gist.Description != "" {
				if description, err = highlightMatch(gist.Description, filter, &matched, cs.Bold, cs.Highlight); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s%s\n", tab, description)
			}

			if file.Content != "" {
				for line := range strings.FieldsFuncSeq(file.Content, split) {
					if filter.MatchString(line) {
						if line, err = highlightMatch(line, filter, &matched, normal, cs.Highlight); err != nil {
							return err
						}
						fmt.Fprintf(out, "%[1]s%[1]s%[2]s\n", tab, line)
					}
				}
			}

			if matched {
				fmt.Fprintln(io.Out, out.String())
			}
		}
	}

	return nil
}

func highlightMatch(s string, filter *regexp.Regexp, matched *bool, color, highlight func(string) string) (string, error) {
	matches := filter.FindAllStringIndex(s, -1)
	if matches == nil {
		return color(s), nil
	}

	out := strings.Builder{}

	// 将首个匹配项之前的文本着色。若为空字符串，则不添加 ANSI 颜色序列。
	if _, err := out.WriteString(color(s[:matches[0][0]])); err != nil {
		return "", err
	}

	// 高亮每个匹配项，然后对剩余文本着色；若为空字符串，则不添加 ANSI 颜色序列。
	for i, match := range matches {
		if _, err := out.WriteString(highlight(s[match[0]:match[1]])); err != nil {
			return "", err
		}

		text := s[match[1]:]
		if i+1 < len(matches) {
			text = s[match[1]:matches[i+1][0]]
		}
		if _, err := out.WriteString(color(text)); err != nil {
			return "", err
		}
	}

	if matched != nil {
		*matched = *matched || true
	}

	return out.String(), nil
}

func normal(s string) string {
	return s
}
