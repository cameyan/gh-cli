package token

import (
	"errors"
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type TokenOptions struct {
	IO     *iostreams.IOStreams
	Config func() (gh.Config, error)

	Hostname      string
	Username      string
	SecureStorage bool
}

func NewCmdToken(f *cmdutil.Factory, runF func(*TokenOptions) error) *cobra.Command {
	opts := &TokenOptions{
		IO:     f.IOStreams,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "token",
		Short: "打印 gh 对指定主机名和账户使用的身份验证令牌",
		Long: heredoc.Docf(`
			此命令输出给定 GitHub 主机上某个账户的身份验证令牌。

			未使用 %[1]s--hostname%[1]s 标志时，将选择默认主机。

			未使用 %[1]s--user%[1]s 标志时，将选择该主机的活跃账户。
		`, "`"),
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}

			return tokenRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "已通过身份验证的 GitHub 实例的主机名")
	cmd.Flags().StringVarP(&opts.Username, "user", "u", "", "要输出其令牌的账户")
	cmd.Flags().BoolVarP(&opts.SecureStorage, "secure-storage", "", false, "仅在安全的凭据存储中搜索身份验证令牌")
	_ = cmd.Flags().MarkHidden("secure-storage")

	return cmd
}

func tokenRun(opts *TokenOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()

	hostname := opts.Hostname
	if hostname == "" {
		hostname, _ = authCfg.DefaultHost()
	}

	var val string
	// 如果这段条件逻辑最终在任何地方被重复，
	// 我们应该考虑编写一个返回正确行为的工厂函数。
	// 目前，保持全部内联是最简单的。
	if opts.SecureStorage {
		if opts.Username == "" {
			val, _ = authCfg.TokenFromKeyring(hostname)
		} else {
			val, _ = authCfg.TokenFromKeyringForUser(hostname, opts.Username)
		}
	} else {
		if opts.Username == "" {
			val, _ = authCfg.ActiveToken(hostname)
		} else {
			val, _, _ = authCfg.TokenForUser(hostname, opts.Username)
		}
	}

	if val == "" {
		errMsg := fmt.Sprintf("未找到 %s 的 OAuth 令牌", hostname)
		if opts.Username != "" {
			errMsg += fmt.Sprintf(" 的账户 %s", opts.Username)
		}
		return errors.New(errMsg)
	}

	if val != "" {
		fmt.Fprintf(opts.IO.Out, "%s\n", val)
	}

	return nil
}
