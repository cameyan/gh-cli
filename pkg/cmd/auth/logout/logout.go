package logout

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

type LogoutOptions struct {
	IO       *iostreams.IOStreams
	Config   func() (gh.Config, error)
	Prompter shared.Prompt
	Hostname string
	Username string
}

func NewCmdLogout(f *cmdutil.Factory, runF func(*LogoutOptions) error) *cobra.Command {
	opts := &LogoutOptions{
		IO:       f.IOStreams,
		Config:   f.Config,
		Prompter: f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "logout",
		Args:  cobra.ExactArgs(0),
		Short: "登出 GitHub 账户",
		Long: heredoc.Doc(`
			移除 GitHub 账户的身份验证。

			此命令移除某个账户已存储的身份验证配置，身份验证配置仅在本地移除。

			此命令不会撤销身份验证令牌。

			要撤销 GitHub CLI 生成的所有身份验证令牌:

			1. 访问 <https://github.com/settings/applications>
			2. 选择 "GitHub CLI" 应用
			3. 选择 "Revoke Access"(撤销访问)
			4. 选择 "I understand, revoke access"(我了解，撤销访问)

			注意: 此操作将撤销 GitHub CLI 在您所有设备上生成的所有身份验证令牌。

			有关撤销 OAuth 应用令牌的更多信息，请参见:
			<https://docs.github.com/en/apps/oauth-apps/using-oauth-apps/reviewing-your-authorized-oauth-apps>
		`),
		Example: heredoc.Doc(`
			# 通过提示选择要登出的主机和账户
			$ gh auth logout

			# 登出特定主机上的特定账户
			$ gh auth logout --hostname enterprise.internal --user monalisa
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}

			return logoutRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "要登出的 GitHub 实例的主机名")
	cmd.Flags().StringVarP(&opts.Username, "user", "u", "", "要登出的账户")

	return cmd
}

func logoutRun(opts *LogoutOptions) error {
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

	type hostUser struct {
		host string
		user string
	}
	var candidates []hostUser

	for _, host := range knownHosts {
		if hostname != "" && host != hostname {
			continue
		}
		knownUsers := cfg.Authentication().UsersForHost(host)
		for _, user := range knownUsers {
			if username != "" && user != username {
				continue
			}
			candidates = append(candidates, hostUser{host: host, user: user})
		}
	}

	if len(candidates) == 0 {
		return errors.New("没有账户符合该条件")
	} else if len(candidates) == 1 {
		hostname = candidates[0].host
		username = candidates[0].user
	} else if !opts.IO.CanPrompt() {
		return errors.New("无法确定要登出哪个账户，请指定 `--hostname` 和 `--user`")
	} else {
		prompts := make([]string, len(candidates))
		for i, c := range candidates {
			prompts[i] = fmt.Sprintf("%s (%s)", c.user, c.host)
		}
		selected, err := opts.Prompter.Select(
			"要登出哪个账户？", "", prompts)
		if err != nil {
			return fmt.Errorf("无法显示提示: %w", err)
		}
		hostname = candidates[selected].host
		username = candidates[selected].user
	}

	if src, writeable := shared.AuthTokenWriteable(authCfg, hostname); !writeable {
		fmt.Fprintf(opts.IO.ErrOut, "环境变量 %s 的值正在用于身份验证\n", src)
		fmt.Fprint(opts.IO.ErrOut, "若要擦除 GitHub CLI 中存储的凭据，请先从环境中清除该值\n")
		return cmdutil.SilentError
	}

	// 此处可以忽略错误，因为主机必须始终有一个活跃用户
	preLogoutActiveUser, _ := authCfg.ActiveUser(hostname)

	if err := authCfg.Logout(hostname, username); err != nil {
		return err
	}

	postLogoutActiveUser, _ := authCfg.ActiveUser(hostname)
	hasSwitchedToNewUser := preLogoutActiveUser != postLogoutActiveUser &&
		postLogoutActiveUser != ""

	cs := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.ErrOut, "%s 已登出 %s 的账户 %s\n",
		cs.SuccessIcon(), hostname, cs.Bold(username))

	if hasSwitchedToNewUser {
		fmt.Fprintf(opts.IO.ErrOut, "%s 已将 %s 的活跃账户切换为 %s\n",
			cs.SuccessIcon(), hostname, cs.Bold(postLogoutActiveUser))
	}

	return nil
}
