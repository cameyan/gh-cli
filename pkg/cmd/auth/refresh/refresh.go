package refresh

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/authflow"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared/gitcredentials"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/spf13/cobra"
)

type token string
type username string

type RefreshOptions struct {
	IO              *iostreams.IOStreams
	Config          func() (gh.Config, error)
	PlainHttpClient func() (*http.Client, error)
	GitClient       *git.Client
	Prompter        shared.Prompt

	MainExecutable string

	Hostname     string
	Scopes       []string
	RemoveScopes []string
	ResetScopes  bool
	AuthFlow     func(*http.Client, *iostreams.IOStreams, string, []string, bool, bool) (token, username, error)

	Interactive     bool
	InsecureStorage bool
	Clipboard       *bool
}

func NewCmdRefresh(f *cmdutil.Factory, runF func(*RefreshOptions) error) *cobra.Command {
	opts := &RefreshOptions{
		IO:     f.IOStreams,
		Config: f.Config,
		AuthFlow: func(httpClient *http.Client, io *iostreams.IOStreams, hostname string, scopes []string, interactive bool, clipboard bool) (token, username, error) {
			t, u, err := authflow.AuthFlow(httpClient, hostname, io, "", scopes, interactive, f.Browser, clipboard)
			return token(t), username(u), err
		},
		PlainHttpClient: f.PlainHttpClient,
		GitClient:       f.GitClient,
		Prompter:        f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "refresh",
		Args:  cobra.ExactArgs(0),
		Short: "刷新已存储的身份验证凭据",
		Long: heredoc.Docf(`
			为活跃账户扩展或修复已存储凭据的权限作用域(scopes)。

			%[1]s--scopes%[1]s 标志接受以逗号分隔的作用域列表，即您希望
			gh 凭据拥有的作用域。若未提供任何作用域，该命令将
			保留之前添加的作用域。

			%[1]s--remove-scopes%[1]s 标志接受以逗号分隔的作用域列表，即您希望
			从 gh 凭据中移除的作用域。移除作用域是幂等的。
			最低作用域集合(%[1]srepo%[1]s、%[1]sread:org%[1]s 和 %[1]sgist%[1]s)无法被移除。

			%[1]s--reset-scopes%[1]s 标志将 gh 凭据的作用域重置为
			您身份验证流程的默认作用域集合。

			如果您在 %[1]sgh auth status%[1]s 中有多个账户，并希望刷新某个非活跃账户的凭据，
			必须先使用 %[1]sgh auth switch%[1]s 切换到该账户，然后再使用
			此命令，完成后切换回来。

			有关 OAuth 作用域(scopes)的更多信息，请参见
			<https://docs.github.com/en/developers/apps/building-oauth-apps/scopes-for-oauth-apps/>。
		`, "`"),
		Example: heredoc.Doc(`
			# 打开浏览器添加 write:org 和 read:public_key 作用域
			$ gh auth refresh --scopes write:org,read:public_key

			# 打开浏览器确保您的身份验证凭据具有正确的最低作用域
			$ gh auth refresh

			# 打开浏览器幂等地移除 delete_repo 作用域
			$ gh auth refresh --remove-scopes delete_repo

			# 打开浏览器使用默认最低作用域重新进行身份验证
			$ gh auth refresh --reset-scopes

			# 打开浏览器重新进行身份验证，并将一次性 OAuth 码复制到剪贴板
			$ gh auth refresh --clipboard
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Interactive = opts.IO.CanPrompt()

			if !opts.Interactive && opts.Hostname == "" {
				return cmdutil.FlagErrorf("非交互式运行时必须提供 --hostname 标志")
			}

			opts.MainExecutable = f.ExecutablePath
			if runF != nil {
				return runF(opts)
			}
			return refreshRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "用于身份验证的 GitHub 主机")
	cmd.Flags().StringSliceVarP(&opts.Scopes, "scopes", "s", nil, "gh 要拥有的额外身份验证作用域(scopes)")
	cmd.Flags().StringSliceVarP(&opts.RemoveScopes, "remove-scopes", "r", nil, "要从 gh 移除的身份验证作用域(scopes)")
	cmd.Flags().BoolVar(&opts.ResetScopes, "reset-scopes", false, "将身份验证作用域重置为默认的最低作用域集合")
	cmdutil.NilBoolFlag(cmd, &opts.Clipboard, "clipboard", "c", "将一次性 OAuth 设备码复制到剪贴板")
	// 安全存储(secure storage)于 2023/4/04 起成为默认；此标志保留为无操作(no-op)以保持向后兼容
	var secureStorage bool
	cmd.Flags().BoolVar(&secureStorage, "secure-storage", false, "将身份验证凭据保存在安全的凭据存储中")
	_ = cmd.Flags().MarkHidden("secure-storage")

	cmd.Flags().BoolVarP(&opts.InsecureStorage, "insecure-storage", "", false, "将身份验证凭据保存为纯文本而不是凭据存储")

	return cmd
}

func refreshRun(opts *RefreshOptions) error {
	plainHTTPClient, err := opts.PlainHttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()
	copyToClipboard := shared.ShouldCopyToClipboard(cfg, opts.Clipboard)

	candidates := authCfg.Hosts()
	if len(candidates) == 0 {
		return fmt.Errorf("未登录任何主机。请使用 'gh auth login' 向主机进行身份验证")
	}

	hostname := opts.Hostname
	if hostname == "" {
		if len(candidates) == 1 {
			hostname = candidates[0]
		} else {
			selected, err := opts.Prompter.Select("要刷新哪个账户的身份验证？", "", candidates)
			if err != nil {
				return fmt.Errorf("无法显示提示: %w", err)
			}
			hostname = candidates[selected]
		}
	} else if !slices.Contains(candidates, hostname) {
		return fmt.Errorf("未登录 %s。请使用 'gh auth login' 向此主机进行身份验证", hostname)
	}

	if src, writeable := shared.AuthTokenWriteable(authCfg, hostname); !writeable {
		fmt.Fprintf(opts.IO.ErrOut, "环境变量 %s 的值正在用于身份验证。\n", src)
		fmt.Fprint(opts.IO.ErrOut, "若要刷新 GitHub CLI 中存储的凭据，请先从环境中清除该值。\n")
		return cmdutil.SilentError
	}

	additionalScopes := set.NewStringSet()

	if !opts.ResetScopes {
		if oldToken, _ := authCfg.ActiveToken(hostname); oldToken != "" {
			if oldScopes, err := shared.GetScopes(plainHTTPClient, hostname, oldToken); err == nil {
				for s := range strings.SplitSeq(oldScopes, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						additionalScopes.Add(s)
					}
				}
			}
		}
	}

	credentialFlow := &shared.GitCredentialFlow{
		Prompter: opts.Prompter,
		HelperConfig: &gitcredentials.HelperConfig{
			SelfExecutablePath: opts.MainExecutable,
			GitClient:          opts.GitClient,
		},
		Updater: &gitcredentials.Updater{
			GitClient: opts.GitClient,
		},
	}
	gitProtocol := cfg.GitProtocol(hostname).Value
	if opts.Interactive && gitProtocol == "https" {
		if err := credentialFlow.Prompt(hostname); err != nil {
			return err
		}
		additionalScopes.AddValues(credentialFlow.Scopes())
	}

	additionalScopes.AddValues(opts.Scopes)

	additionalScopes.RemoveValues(opts.RemoveScopes)

	authedToken, authedUser, err := opts.AuthFlow(plainHTTPClient, opts.IO, hostname, additionalScopes.ToSlice(), opts.Interactive, copyToClipboard)
	if err != nil {
		return err
	}
	activeUser, _ := authCfg.ActiveUser(hostname)
	if activeUser != "" && username(activeUser) != authedUser {
		return fmt.Errorf("刷新 %s 的凭据时出错，但收到的是 %s 的凭据，您在浏览器中使用的是正确的账户吗？", activeUser, authedUser)
	}
	if _, err := authCfg.Login(hostname, string(authedUser), string(authedToken), "", !opts.InsecureStorage); err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.ErrOut, "%s 身份验证完成。\n", cs.SuccessIcon())

	if credentialFlow.ShouldSetup() {
		username, _ := authCfg.ActiveUser(hostname)
		password, _ := authCfg.ActiveToken(hostname)
		if err := credentialFlow.Setup(hostname, username, password); err != nil {
			return err
		}
	}

	return nil
}
