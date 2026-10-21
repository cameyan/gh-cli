package label

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type iprompter interface {
	ConfirmDeletion(string) error
}

type deleteOptions struct {
	BaseRepo   func() (ghrepo.Interface, error)
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Prompter   iprompter

	Name      string
	Confirmed bool
}

func newCmdDelete(f *cmdutil.Factory, runF func(*deleteOptions) error) *cobra.Command {
	opts := deleteOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "从仓库中删除一个标签",
		Args:  cmdutil.ExactArgs(1, "无法删除标签: 需要提供 name 参数"),
		RunE: func(c *cobra.Command, args []string) error {
			// 支持 `-R, --repo` 覆盖
			opts.BaseRepo = f.BaseRepo
			opts.Name = args[0]

			if !opts.IO.CanPrompt() && !opts.Confirmed {
				return cmdutil.FlagErrorf("非交互模式下必须提供 --yes")
			}

			if runF != nil {
				return runF(&opts)
			}
			return deleteRun(&opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Confirmed, "confirm", false, "不提示直接确认删除")
	_ = cmd.Flags().MarkDeprecated("confirm", "请改用 `--yes`")
	cmd.Flags().BoolVar(&opts.Confirmed, "yes", false, "不提示直接确认删除")

	return cmd
}

func deleteRun(opts *deleteOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if !opts.Confirmed {
		if err := opts.Prompter.ConfirmDeletion(opts.Name); err != nil {
			return err
		}
	}

	opts.IO.StartProgressIndicator()
	err = deleteLabel(httpClient, baseRepo, opts.Name)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		successMsg := fmt.Sprintf("%s 已从 %s 中删除标签 %q\n", cs.SuccessIcon(), opts.Name, ghrepo.FullName(baseRepo))
		fmt.Fprint(opts.IO.Out, successMsg)
	}

	return nil
}

func deleteLabel(client *http.Client, repo ghrepo.Interface, name string) error {
	apiClient := api.NewClientFromHTTP(client)
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "labels", name)
	if err != nil {
		return err
	}

	return apiClient.REST(repo.RepoHost(), "DELETE", path.String(), nil, nil)
}
