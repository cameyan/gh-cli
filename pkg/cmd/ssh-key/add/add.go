package add

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/ssh-key/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type AddOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HTTPClient func() (*http.Client, error)

	KeyFile string
	Title   string
	Type    string
}

func NewCmdAdd(f *cmdutil.Factory, runF func(*AddOptions) error) *cobra.Command {
	opts := &AddOptions{
		HTTPClient: f.HttpClient,
		Config:     f.Config,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "add [<key-file>]",
		Short: "向您的 GitHub 账户添加 SSH 密钥",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if opts.IO.IsStdoutTTY() && opts.IO.IsStdinTTY() {
					return cmdutil.FlagErrorf("缺少公钥文件")
				}
				opts.KeyFile = "-"
			} else {
				opts.KeyFile = args[0]
			}

			if runF != nil {
				return runF(opts)
			}
			return runAdd(opts)
		},
	}

	typeEnums := []string{shared.AuthenticationKey, shared.SigningKey}
	cmdutil.StringEnumFlag(cmd, &opts.Type, "type", "", shared.AuthenticationKey, typeEnums, "SSH 密钥的类型")
	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "新密钥的标题")
	return cmd
}

func runAdd(opts *AddOptions) error {
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

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	hostname, _ := cfg.Authentication().DefaultHost()

	var uploaded bool

	if opts.Type == shared.SigningKey {
		uploaded, err = SSHSigningKeyUpload(httpClient, hostname, keyReader, opts.Title)
	} else {
		uploaded, err = SSHKeyUpload(httpClient, hostname, keyReader, opts.Title)
	}

	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()

	if uploaded {
		fmt.Fprintf(opts.IO.ErrOut, "%s 公钥已添加到您的账户\n", cs.SuccessIcon())
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "%s 公钥已存在于您的账户中\n", cs.SuccessIcon())
	}

	return nil
}
