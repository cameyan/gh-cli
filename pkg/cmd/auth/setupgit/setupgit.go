package setupgit

import (
	"fmt"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared/gitcredentials"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type gitCredentialsConfigurer interface {
	ConfigureOurs(hostname string) error
}

type SetupGitOptions struct {
	IO                      *iostreams.IOStreams
	Config                  func() (gh.Config, error)
	Hostname                string
	Force                   bool
	CredentialsHelperConfig gitCredentialsConfigurer
}

func NewCmdSetupGit(f *cmdutil.Factory, runF func(*SetupGitOptions) error) *cobra.Command {
	opts := &SetupGitOptions{
		IO:     f.IOStreams,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "setup-git",
		Short: "为 git 配置 GitHub CLI",
		Long: heredoc.Docf(`
			此命令将 %[1]sgit%[1]s 配置为使用 GitHub CLI 作为凭据助手。有关 git 凭据助手的更多信息，请参考:
			<https://git-scm.com/docs/gitcredentials>。

			默认情况下，GitHub CLI 将被设置为所有已通过身份验证主机的凭据助手。如果没有已通过身份验证
			的主机，该命令将报错失败。

			或者，使用 %[1]s--hostname%[1]s 标志指定要配置的单个主机。如果该主机未通过身份验证，
			该命令将报错失败。
		`, "`"),
		Example: heredoc.Doc(`
			# 将 git 配置为对所有已通过身份验证的主机使用 GitHub CLI 作为凭据助手
			$ gh auth setup-git

			# 将 git 配置为对 enterprise.internal 主机使用 GitHub CLI 作为凭据助手
			$ gh auth setup-git --hostname enterprise.internal
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.CredentialsHelperConfig = &gitcredentials.HelperConfig{
				SelfExecutablePath: f.ExecutablePath,
				GitClient:          f.GitClient,
			}
			if opts.Hostname == "" && opts.Force {
				return cmdutil.FlagErrorf("`--force` 必须与 `--hostname` 一起使用")
			}
			if runF != nil {
				return runF(opts)
			}
			return setupGitRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "要为其配置 git 的主机名")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "即使主机未知也强制配置。必须与 `--hostname` 一起使用")

	return cmd
}

func setupGitRun(opts *SetupGitOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()
	hostnames := authCfg.Hosts()

	stderr := opts.IO.ErrOut
	cs := opts.IO.ColorScheme()

	// 如果提供了主机名，则仅设置该主机
	if opts.Hostname != "" {
		if !opts.Force && !has(opts.Hostname, hostnames) {
			return fmt.Errorf("您尚未登录 GitHub 主机 %q。请运行 %s 进行身份验证，或提供 `--force`",
				opts.Hostname,
				cs.Bold(fmt.Sprintf("gh auth login -h %s", opts.Hostname)),
			)
		}

		if err := opts.CredentialsHelperConfig.ConfigureOurs(opts.Hostname); err != nil {
			return fmt.Errorf("设置 git 凭据助手失败: %s", err)
		}

		return nil
	}

	// 否则我们将设置所有已知主机
	if len(hostnames) == 0 {
		fmt.Fprintf(
			stderr,
			"您尚未登录任何 GitHub 主机。请运行 %s 进行身份验证\n",
			cs.Bold("gh auth login"),
		)

		return cmdutil.SilentError
	}

	for _, hostname := range hostnames {
		if err := opts.CredentialsHelperConfig.ConfigureOurs(hostname); err != nil {
			return fmt.Errorf("设置 git 凭据助手失败: %s", err)
		}
	}

	return nil
}

func has(needle string, haystack []string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
