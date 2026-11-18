package get

import (
	"errors"
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type GetOptions struct {
	IO     *iostreams.IOStreams
	Config gh.Config

	Hostname string
	Key      string
}

func NewCmdConfigGet(f *cmdutil.Factory, runF func(*GetOptions) error) *cobra.Command {
	opts := &GetOptions{
		IO: f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "打印给定配置键的值",
		Example: heredoc.Doc(`
			$ gh config get git_protocol
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := f.Config()
			if err != nil {
				return err
			}
			opts.Config = config
			opts.Key = args[0]

			if runF != nil {
				return runF(opts)
			}

			return getRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "host", "h", "", "获取按主机划分的配置项")

	return cmd
}

func getRun(opts *GetOptions) error {
	// 获取 `oauth_token` 值时搜索密钥环(keyring)存储
	if opts.Hostname != "" && opts.Key == "oauth_token" {
		token := opts.Config.Authentication().ActiveToken(opts.Hostname).Token
		if token == "" {
			return errors.New(`找不到键 "oauth_token"`)
		}
		fmt.Fprintf(opts.IO.Out, "%s\n", token)
		return nil
	}

	optionalEntry := opts.Config.GetOrDefault(opts.Hostname, opts.Key)
	if optionalEntry.IsNone() {
		return nonExistentKeyError{key: opts.Key}
	}

	val := optionalEntry.Unwrap().Value
	if val != "" {
		fmt.Fprintf(opts.IO.Out, "%s\n", val)
	}
	return nil
}

type nonExistentKeyError struct {
	key string
}

func (e nonExistentKeyError) Error() string {
	return fmt.Sprintf("找不到键 \"%s\"", e.key)
}
