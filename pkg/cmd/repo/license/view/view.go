package view

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	IO         *iostreams.IOStreams
	HTTPClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	License    string
	Web        bool
	Browser    browser.Browser
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		HTTPClient: f.HttpClient,
		Config:     f.Config,
		Browser:    f.Browser,
	}

	cmd := &cobra.Command{
		Use:   "view {<license-key> | <spdx-id>}",
		Short: "查看特定仓库许可证",
		Long: heredoc.Docf(`
			通过许可证密钥或 SPDX ID 查看特定仓库的许可证。

			运行 %[1]sgh repo license list%[1]s 查看可用的常用许可证。如需更多许可证，请访问:
			<https://choosealicense.com/appendix>
		`, "`"),
		Example: heredoc.Doc(`
			# 查看来自 SPDX ID 的 MIT 许可证
			$ gh repo license view MIT

			# 查看来自许可证密钥的 MIT 许可证
			$ gh repo license view mit

			# 查看来自 SPDX ID 的 GNU AGPL-3.0 许可证
			$ gh repo license view AGPL-3.0

			# 查看来自许可证密钥的 GNU AGPL-3.0 许可证
			$ gh repo license view agpl-3.0

			# 创建一个包含 MIT 许可证的 LICENSE.md 文件
			$ gh repo license view MIT > LICENSE.md
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.License = args[0]
			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开 https://choosealicense.com/")

	return cmd
}

func viewRun(opts *ViewOptions) error {
	if opts.License == "" {
		return errors.New("未提供许可证")
	}

	client, err := opts.HTTPClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	hostname, _ := cfg.Authentication().DefaultHost()
	license, err := api.RepoLicense(client, hostname, opts.License)
	if err != nil {
		if httpErr, ok := errors.AsType[api.HTTPError](err); ok {
			if httpErr.StatusCode == 404 {
				return fmt.Errorf("'%s' 不是有效的许可证名称或 SPDX 标识符。\n\n运行 `gh repo license list` 查看可用的常用许可证。如需更多许可证，请访问 %s", opts.License, text.DisplayURL("https://choosealicense.com/appendix"))
			}
		}
		return err
	}

	if opts.Web {
		url := fmt.Sprintf("https://choosealicense.com/licenses/%s", license.Key)
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s\n", text.DisplayURL(url))
		}
		return opts.Browser.Browse(url)
	}

	return renderLicense(license, opts)
}

func renderLicense(license *api.License, opts *ViewOptions) error {
	cs := opts.IO.ColorScheme()
	var out strings.Builder
	if opts.IO.IsStdoutTTY() {
		out.WriteString(fmt.Sprintf("\n%s\n", cs.Muted(license.Description)))
		out.WriteString(fmt.Sprintf("\n%s\n", cs.Mutedf("待实现: %s", license.Implementation)))
		out.WriteString(fmt.Sprintf("\n%s\n\n", cs.Mutedf("如需更多信息，请参阅: %s", license.HTMLURL)))
	}
	out.WriteString(license.Body)
	_, err := opts.IO.Out.Write([]byte(out.String()))
	if err != nil {
		return err
	}
	return nil
}
