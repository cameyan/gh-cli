package delete

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DeleteOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)

	KeyID     string
	Confirmed bool
	Prompter  prompter.Prompter
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		HttpClient: f.HttpClient,
		Config:     f.Config,
		IO:         f.IOStreams,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "从您的 GitHub 账户中删除 SSH 密钥",
		Args:  cmdutil.ExactArgs(1, "无法删除: 需要提供密钥 id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.KeyID = args[0]

			if !opts.IO.CanPrompt() && !opts.Confirmed {
				return cmdutil.FlagErrorf("非交互模式下必须提供 --yes")
			}

			if runF != nil {
				return runF(opts)
			}

			return deleteRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Confirmed, "confirm", false, "跳过确认提示")
	_ = cmd.Flags().MarkDeprecated("confirm", "请改用 `--yes`")
	cmd.Flags().BoolVarP(&opts.Confirmed, "yes", "y", false, "跳过确认提示")

	return cmd
}

func deleteRun(opts *DeleteOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()
	key, err := getSSHKey(httpClient, host, opts.KeyID)
	if err != nil {
		return err
	}

	if !opts.Confirmed {
		if err := opts.Prompter.ConfirmDeletion(key.Title); err != nil {
			return err
		}
	}

	err = deleteSSHKey(httpClient, host, opts.KeyID)
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "%s 已从您的账户中删除 SSH 密钥 %q (%s)\n", cs.SuccessIcon(), key.Title, opts.KeyID)
	}
	return nil
}
