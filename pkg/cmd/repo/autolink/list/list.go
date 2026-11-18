package list

import (
	"fmt"
	"strconv"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type listOptions struct {
	BaseRepo       func() (ghrepo.Interface, error)
	Browser        browser.Browser
	AutolinkClient AutolinkListClient
	IO             *iostreams.IOStreams

	Exporter cmdutil.Exporter
	WebMode  bool
}

type AutolinkListClient interface {
	List(repo ghrepo.Interface) ([]shared.Autolink, error)
}

func NewCmdList(f *cmdutil.Factory, runF func(*listOptions) error) *cobra.Command {
	opts := &listOptions{
		Browser: f.Browser,
		IO:      f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出 GitHub 仓库的自动链接引用",
		Long: heredoc.Doc(`
			获取为仓库配置的所有自动链接引用。

			自动链接信息仅对仓库管理员可见。
		`),
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			httpClient, err := f.HttpClient()
			if err != nil {
				return err
			}
			opts.AutolinkClient = &AutolinkLister{HTTPClient: httpClient}

			if runF != nil {
				return runF(opts)
			}

			return listRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在网页浏览器中列出自动链接引用")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, shared.AutolinkFields)

	return cmd
}

func listRun(opts *listOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.WebMode {
		autolinksListURL := ghrepo.GenerateRepoURL(repo, "settings/key_links")

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", autolinksListURL)
		}

		return opts.Browser.Browse(autolinksListURL)
	}

	autolinks, err := opts.AutolinkClient.List(repo)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()

	if len(autolinks) == 0 {
		return cmdutil.NewNoResultsError(
			fmt.Sprintf(
				"%s 中未找到自动链接",
				cs.Bold(ghrepo.FullName(repo))),
		)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, autolinks)
	}

	if opts.IO.IsStdoutTTY() {
		title := fmt.Sprintf(
			"正在显示仓库 %s 中的 %d 个自动链接引用",
	        cs.Bold(ghrepo.FullName(repo)),
	        len(autolinks),
		)
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", title)
	}

	tp := tableprinter.New(opts.IO, tableprinter.WithHeader("ID", "键前缀", "URL 模板", "字母数字"))

	for _, autolink := range autolinks {
		tp.AddField(cs.Cyanf("%d", autolink.ID))
		tp.AddField(autolink.KeyPrefix)
		tp.AddField(autolink.URLTemplate)
		tp.AddField(strconv.FormatBool(autolink.IsAlphanumeric))
		tp.EndRow()
	}

	return tp.Render()
}
