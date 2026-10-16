package label

import (
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type listOptions struct {
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Exporter cmdutil.Exporter
	Query    listQueryOptions
	WebMode  bool
}

func newCmdList(f *cmdutil.Factory, runF func(*listOptions) error) *cobra.Command {
	opts := listOptions{
		Browser:    f.Browser,
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出仓库中的标签(label)",
		Long: heredoc.Docf(`
			显示 GitHub 仓库中的标签(label)。

			使用 %[1]s--search%[1]s flag 时，结果按与查询的最佳匹配度排序。
			该行为无法通过 %[1]s--order%[1]s 或 %[1]s--sort%[1]s flag 配置。
		`, "`"),
		Example: heredoc.Doc(`
			# 按名称排序标签
			$ gh label list --sort name

			# 查找名称或描述中含 "bug" 的标签
			$ gh label list --search bug
		`),
		Args:    cobra.NoArgs,
		Aliases: []string{"ls"},
		RunE: func(c *cobra.Command, args []string) error {
			// 支持 `-R, --repo` 覆盖
			opts.BaseRepo = f.BaseRepo

			if opts.Query.Limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %v", opts.Query.Limit)
			}

			if opts.Query.Query != "" &&
				(c.Flags().Changed("order") || c.Flags().Changed("sort")) {
				return cmdutil.FlagErrorf("`--order` 或 `--sort` 不能与 `--search` 同时指定")
			}

			if runF != nil {
				return runF(&opts)
			}
			return listRun(&opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 web 浏览器中列出标签(label)")
	cmd.Flags().IntVarP(&opts.Query.Limit, "limit", "L", 30, "最多获取的标签数量")
	cmd.Flags().StringVarP(&opts.Query.Query, "search", "S", "", "搜索标签名称与描述")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Order, "order", "", defaultOrder, []string{"asc", "desc"}, "返回标签的顺序")
	cmdutil.StringEnumFlag(cmd, &opts.Query.Sort, "sort", "", defaultSort, []string{"created", "name"}, "对获取的标签排序")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, labelFields)

	return cmd
}

func listRun(opts *listOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.WebMode {
		labelListURL := ghrepo.GenerateRepoURL(baseRepo, "labels")

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(labelListURL))
		}

		return opts.Browser.Browse(labelListURL)
	}

	if opts.Exporter != nil {
		opts.Query.fields = opts.Exporter.Fields()
	}

	opts.IO.StartProgressIndicator()
	labels, totalCount, err := listLabels(httpClient, baseRepo, opts.Query)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if len(labels) == 0 {
		if opts.Query.Query != "" {
			return cmdutil.NewNoResultsError(fmt.Sprintf("%s 中没有匹配您搜索的标签(label)", ghrepo.FullName(baseRepo)))
		}
		return cmdutil.NewNoResultsError(fmt.Sprintf("%s 中未找到任何标签(label)", ghrepo.FullName(baseRepo)))
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, labels)
	}

	if opts.IO.IsStdoutTTY() {
		title := listHeader(ghrepo.FullName(baseRepo), len(labels), totalCount)
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", title)
	}

	return printLabels(opts.IO, labels)
}

func printLabels(io *iostreams.IOStreams, labels []label) error {
	cs := io.ColorScheme()
	table := tableprinter.New(io, tableprinter.WithHeader("名称", "描述", "颜色"))

	for _, label := range labels {
		// 使用 tableprinter 的 WithColor 函数为标签着色，以处理非 TTY 的情况
		labelColor := tableprinter.WithColor(func(s string) string {
			return cs.Label(label.Color, s)
		})

		table.AddField(label.Name, labelColor)
		table.AddField(label.Description)
		table.AddField("#" + label.Color)

		table.EndRow()
	}

	return table.Render()
}

func listHeader(repoName string, count int, totalCount int) string {
	return fmt.Sprintf("显示 %s 中的 %d 条，共 %d 条标签(label)", repoName, count, totalCount)
}
