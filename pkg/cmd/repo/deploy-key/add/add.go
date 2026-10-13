package add

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type AddOptions struct {
	IO         *iostreams.IOStreams
	HTTPClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)

	KeyFile    string
	Title      string
	AllowWrite bool
}

func NewCmdAdd(f *cmdutil.Factory, runF func(*AddOptions) error) *cobra.Command {
	opts := &AddOptions{
		HTTPClient: f.HttpClient,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "add <key-file>",
		Short: "向 GitHub 仓库添加部署密钥(deploy key)",
		Long: heredoc.Doc(`
			向 GitHub 仓库添加部署密钥(deploy key)。

			注意: 由 gh 添加的密钥将与当前身份验证令牌(token)关联。
			如果您从账户中取消授权 GitHub CLI 应用或身份验证令牌，
			由 GitHub CLI 添加的部署密钥也将被一并移除。
		`),
		Example: heredoc.Doc(`
			# 生成一个无密码的 SSH 密钥，并将其作为部署密钥添加到仓库
			$ ssh-keygen -t ed25519 -C "my description" -N "" -f ~/.ssh/gh-test
			$ gh repo deploy-key add ~/.ssh/gh-test.pub
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo
			opts.KeyFile = args[0]

			if runF != nil {
				return runF(opts)
			}
			return addRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "新密钥的标题")
	cmd.Flags().BoolVarP(&opts.AllowWrite, "allow-write", "w", false, "允许该密钥进行写访问")
	return cmd
}

func addRun(opts *AddOptions) error {
	httpClient, err := opts.HTTPClient()
	if err != nil {
		return err
	}

	var keyReader io.Reader
	if opts.KeyFile == "-" {
		keyReader = opts.IO.In
		defer opts.IO.In.Close()
	} else {
		f, err := os.Open(opts.KeyFile)
		if err != nil {
			return err
		}
		defer f.Close()
		keyReader = f
	}

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if err := uploadDeployKey(httpClient, repo, keyReader, opts.Title, opts.AllowWrite); err != nil {
		return err
	}

	if !opts.IO.IsStdoutTTY() {
		return nil
	}

	cs := opts.IO.ColorScheme()
	_, err = fmt.Fprintf(opts.IO.Out, "%s 已向 %s 添加部署密钥\n", cs.SuccessIcon(), cs.Bold(ghrepo.FullName(repo)))
	return err
}
