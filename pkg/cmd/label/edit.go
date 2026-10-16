package label

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type editOptions struct {
	BaseRepo   func() (ghrepo.Interface, error)
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Color       string
	Description string
	Name        string
	NewName     string
}

func newCmdEdit(f *cmdutil.Factory, runF func(*editOptions) error) *cobra.Command {
	opts := editOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "编辑一个标签(label)",
		Long: heredoc.Docf(`
			更新 GitHub 上的标签(label)。

			可以使用 %[1]s--name%[1]s flag 重命名标签。

			标签颜色必须是 6 位十六进制值。
		`, "`"),
		Example: heredoc.Doc(`
			# 更新 bug 标签的颜色
			$ gh label edit bug --color FF0000

			# 重命名 bug 标签并编辑其描述
			$ gh label edit bug --name big-bug --description "Bigger than normal bug"
		`),
		Args: cmdutil.ExactArgs(1, "无法更新标签: 需要提供 name 参数"),
		RunE: func(c *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo
			opts.Name = args[0]
			opts.Color = strings.TrimPrefix(opts.Color, "#")
			if opts.Description == "" &&
				opts.Color == "" &&
				opts.NewName == "" {
				return cmdutil.FlagErrorf("至少指定 `--color`、`--description` 或 `--name` 之一")
			}
			if runF != nil {
				return runF(&opts)
			}
			return editRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Description, "description", "d", "", "标签的描述")
	cmd.Flags().StringVarP(&opts.Color, "color", "c", "", "标签的颜色")
	cmd.Flags().StringVarP(&opts.NewName, "name", "n", "", "标签的新名称")

	return cmd
}

func editRun(opts *editOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	err = updateLabel(apiClient, baseRepo, opts)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if errors.Is(err, errLabelAlreadyExists) {
			return fmt.Errorf("名为 %q 的标签已存在", opts.NewName)
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		successMsg := fmt.Sprintf("%s 已更新 %s 中的标签(label) %q\n", cs.SuccessIcon(), opts.Name, ghrepo.FullName(baseRepo))
		fmt.Fprint(opts.IO.Out, successMsg)
	}

	return nil
}
