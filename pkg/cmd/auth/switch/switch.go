package authswitch

import (
	"errors"
	"fmt"
	"slices"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type SwitchOptions struct {
	IO       *iostreams.IOStreams
	Config   func() (gh.Config, error)
	Prompter shared.Prompt
	Hostname string
	Username string
}

func NewCmdSwitch(f *cmdutil.Factory, runF func(*SwitchOptions) error) *cobra.Command {
	opts := SwitchOptions{
		IO:       f.IOStreams,
		Config:   f.Config,
		Prompter: f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "switch",
		Args:  cobra.ExactArgs(0),
		Short: "切换活跃的 GitHub 账户",
		Long: heredoc.Docf(`
			切换 GitHub 主机的活跃账户。

			此命令更改在运行针对指定 GitHub 主机的命令时将使用的身份验证配置。

			如果指定的主机有两个账户，活跃账户将自动切换。如果账户多于两个，则需要通过
			%[1]s--user%[1]s 标志或交互式提示来消歧。

			要列出已通过身份验证的账户，可以运行 %[1]sgh auth status%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 通过提示选择要切换到的主机和账户
			$ gh auth switch

			# 将特定主机上的活跃账户切换为特定用户
			$ gh auth switch --hostname enterprise.internal --user monalisa
		`),
		RunE: func(c *cobra.Command, args []string) error {
			if runF != nil {
				return runF(&opts)
			}

			return switchRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "要切换账户的 GitHub 实例的主机名")
	cmd.Flags().StringVarP(&opts.Username, "user", "u", "", "要切换到的账户")

	return cmd
}

type hostUser struct {
	host   string
	user   string
	active bool
}

type candidates []hostUser

func switchRun(opts *SwitchOptions) error {
	hostname := opts.Hostname
	username := opts.Username

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()

	knownHosts := authCfg.Hosts()
	if len(knownHosts) == 0 {
		return fmt.Errorf("未登录任何主机")
	}

	if hostname != "" {
		if !slices.Contains(knownHosts, hostname) {
			return fmt.Errorf("未登录 %s", hostname)
		}

		if username != "" {
			knownUsers := cfg.Authentication().UsersForHost(hostname)
			if !slices.Contains(knownUsers, username) {
				return fmt.Errorf("未登录 %s 的账户 %s", hostname, username)
			}
		}
	}

	var candidates candidates

	for _, host := range knownHosts {
		if hostname != "" && host != hostname {
			continue
		}
		hostActiveUser, err := authCfg.ActiveUser(host)
		if err != nil {
			return err
		}
		knownUsers := cfg.Authentication().UsersForHost(host)
		for _, user := range knownUsers {
			if username != "" && user != username {
				continue
			}
			candidates = append(candidates, hostUser{host: host, user: user, active: user == hostActiveUser})
		}
	}

	if len(candidates) == 0 {
		return errors.New("没有账户符合该条件")
	} else if len(candidates) == 1 {
		hostname = candidates[0].host
		username = candidates[0].user
	} else if len(candidates) == 2 &&
		candidates[0].host == candidates[1].host {
		// 如果只有一个主机拥有两个用户，则自动切换到
		// 非活跃用户，无需提示。
		hostname = candidates[0].host
		username = candidates[0].user
		if candidates[0].active {
			username = candidates[1].user
		}
	} else if !opts.IO.CanPrompt() {
		return errors.New("无法确定要切换到哪个账户，请指定 `--hostname` 和 `--user`")
	} else {
		prompts := make([]string, len(candidates))
		for i, c := range candidates {
			prompt := fmt.Sprintf("%s (%s)", c.user, c.host)
			if c.active {
				prompt += " - 活跃"
			}
			prompts[i] = prompt
		}
		selected, err := opts.Prompter.Select(
			"要切换到哪个账户？", "", prompts)
		if err != nil {
			return fmt.Errorf("无法显示提示: %w", err)
		}
		hostname = candidates[selected].host
		username = candidates[selected].user
	}

	if src, writeable := shared.AuthTokenWriteable(authCfg, hostname); !writeable {
		fmt.Fprintf(opts.IO.ErrOut, "环境变量 %s 的值正在用于身份验证\n", src)
		fmt.Fprint(opts.IO.ErrOut, "若要改为让 GitHub CLI 管理凭据，请先从环境中清除该值\n")
		return cmdutil.SilentError
	}

	cs := opts.IO.ColorScheme()

	if err := authCfg.SwitchUser(hostname, username); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "%s 将 %s 的账户切换为 %s 失败\n",
			cs.FailureIcon(), hostname, cs.Bold(username))

		return err
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已将 %s 的活跃账户切换为 %s\n",
		cs.SuccessIcon(), hostname, cs.Bold(username))

	return nil
}
