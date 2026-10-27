package login

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/authflow"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared/gitcredentials"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

type LoginOptions struct {
	IO              *iostreams.IOStreams
	Config          func() (gh.Config, error)
	HttpClient      func() (*http.Client, error)
	PlainHttpClient func() (*http.Client, error)
	GitClient       *git.Client
	Prompter        shared.Prompt
	Browser         browser.Browser
	Telemetry       ghtelemetry.InvocationRecorder

	MainExecutable string

	Interactive bool

	Hostname         string
	Scopes           []string
	Token            string
	Web              bool
	GitProtocol      string
	InsecureStorage  bool
	SkipSSHKeyPrompt bool
	Expiring         *bool
	Clipboard        *bool

	// AuthFlow is indirected so tests can stub the interactive authorization. When nil, the shared login flow uses
	// the real authflow.AuthFlow.
	AuthFlow func(*http.Client, string, *iostreams.IOStreams, string, []string, bool, browser.Browser, bool, bool) (*authflow.AuthResult, error)

	// helperConfig overrides git credential helper inspection and configuration. It is nil in production, where
	// runLogin builds the real HelperConfig from GitClient; tests set a fake to avoid shelling out to git.
	helperConfig shared.HelperConfig
}

func NewCmdLogin(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*LoginOptions) error) *cobra.Command {
	opts := &LoginOptions{
		IO:              f.IOStreams,
		Config:          f.Config,
		HttpClient:      f.HttpClient,
		PlainHttpClient: f.PlainHttpClient,
		GitClient:       f.GitClient,
		Prompter:        f.Prompter,
		Browser:         f.Browser,
		Telemetry:       telemetry,
	}

	var tokenStdin bool

	cmd := &cobra.Command{
		Use:   "login",
		Args:  cobra.ExactArgs(0),
		Short: "登录 GitHub 账户",
		Long: heredoc.Docf(`
			向 GitHub 主机进行身份验证。

			默认主机名为 %[1]sgithub.com%[1]s，可以使用 %[1]s--hostname%[1]s 标志覆盖它。

			默认身份验证模式是基于网页的浏览器流程。完成后，身份验证令牌将被安全地存储在系统凭据
			存储中。如果找不到凭据存储或使用它时出现问题，gh 将回退为将令牌写入纯文本文件。请参阅
			%[1]sgh auth status%[1]s 了解其存储位置。

			或者，使用 %[1]s--with-token%[1]s 在标准输入上传入个人访问令牌(classic)。
			该令牌所需的最低作用域为: %[1]srepo%[1]s、%[1]sread:org%[1]s 和 %[1]sgist%[1]s。
			将细粒度个人访问令牌传入 %[1]s--with-token%[1]s 时请务必小心，因为其固有的资源限定
			作用域可能在与其它资源交互时导致令人困惑的行为。对于细粒度个人访问令牌的使用，建议改用
			%[1]sGH_TOKEN%[1]s。

			或者，gh 将使用在环境变量中找到的身份验证令牌。
			此方法最适合“无头环境”使用 gh，例如在自动化场景中。更多信息请参见 %[1]sgh help environment%[1]s。

			要在 GitHub Actions 中使用 gh，请在 %[1]senv%[1]s 中添加 %[1]sGH_TOKEN: ${{ github.token }}%[1]s。

			可以使用 %[1]s--git-protocol%[1]s 设置此主机上 git 操作使用的 git 协议，或在交互式
			提示期间设置。虽然登录针对主机上的单个账户，但设置 git 协议将对主机上的所有用户生效。

			为 git 协议指定 %[1]sssh%[1]s 将检测现有的 SSH 密钥以供上传，若未找到密钥则提示创建
			并上传新密钥。可以使用 %[1]s--skip-ssh-key%[1]s 标志跳过此步骤。

			Use %[1]s--expiring%[1]s to prefer expiring credentials that gh refreshes automatically.
			This is a preference, not a guarantee: whether expiring tokens are issued depends on the
			host and the OAuth app configuration. A host without support issues a non-expiring token
			instead, and a host configured for them may issue expiring tokens even without the flag.
			Enable the %[1]sexpiring_credentials%[1]s configuration to prefer expiring credentials
			without passing %[1]s--expiring%[1]s every time, and pass %[1]s--expiring=false%[1]s to request a
			non-expiring token even when that configuration is enabled.
			When using expiring credentials for git operations, git 2.46 or newer is recommended so
			gh can mark the token as non-cacheable; with older git a credential caching helper may store
			and reuse an expired token.

			For more information on OAuth scopes, see
			<https://docs.github.com/en/developers/apps/building-oauth-apps/scopes-for-oauth-apps/>.
		`, "`"),
		Example: heredoc.Doc(`
			# 启动交互式设置
			$ gh auth login

			# 打开浏览器进行身份验证，并将一次性 OAuth 码复制到剪贴板
			$ gh auth login --web --clipboard

			# 通过从文件读取令牌向 github.com 进行身份验证
			$ gh auth login --with-token < mytoken.txt

			# 向特定主机进行身份验证
			$ gh auth login --hostname enterprise.internal
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Telemetry != nil {
				opts.Telemetry.SetSampleRate(ghtelemetry.SAMPLE_ALL)
			}

			if tokenStdin && opts.Web {
				return cmdutil.FlagErrorf("只能指定 `--web` 或 `--with-token` 其中之一")
			}
			if tokenStdin && len(opts.Scopes) > 0 {
				return cmdutil.FlagErrorf("只能指定 `--scopes` 或 `--with-token` 其中之一")
			}
			if tokenStdin && opts.Expiring != nil && *opts.Expiring {
				return cmdutil.FlagErrorf("specify only one of `--expiring` or `--with-token`")
			}

			if tokenStdin {
				defer opts.IO.In.Close()
				token, err := io.ReadAll(opts.IO.In)
				if err != nil {
					return fmt.Errorf("从标准输入读取令牌失败: %w", err)
				}
				opts.Token = strings.TrimSpace(string(token))
			}

			if opts.IO.CanPrompt() && opts.Token == "" {
				opts.Interactive = true
			}

			if cmd.Flags().Changed("hostname") {
				if err := ghinstance.HostnameValidator(opts.Hostname); err != nil {
					return cmdutil.FlagErrorf("解析主机名失败: %w", err)
				}
			}

			if opts.Hostname == "" && (!opts.Interactive || opts.Web) {
				opts.Hostname, _ = ghauth.DefaultHost()
			}

			opts.MainExecutable = f.ExecutablePath
			if runF != nil {
				return runF(opts)
			}

			return loginRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "要进行身份验证的 GitHub 实例的主机名")
	cmd.Flags().StringSliceVarP(&opts.Scopes, "scopes", "s", nil, "要请求的额外身份验证作用域")
	cmd.Flags().BoolVar(&tokenStdin, "with-token", false, "从标准输入读取令牌")
	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "打开浏览器进行身份验证")
	cmdutil.NilBoolFlag(cmd, &opts.Clipboard, "clipboard", "c", "将一次性 OAuth 设备码复制到剪贴板")
	cmdutil.StringEnumFlag(cmd, &opts.GitProtocol, "git-protocol", "p", "", []string{"ssh", "https"}, "此主机上 git 操作使用的协议")

	// 安全存储(secure storage)于 2023/4/04 起成为默认；此标志保留为无操作(no-op)以保持向后兼容
	var secureStorage bool
	cmd.Flags().BoolVar(&secureStorage, "secure-storage", false, "将身份验证凭据保存在安全的凭据存储中")
	_ = cmd.Flags().MarkHidden("secure-storage")

	cmd.Flags().BoolVar(&opts.InsecureStorage, "insecure-storage", false, "Save authentication credentials in plain text instead of credential store")
	cmd.Flags().BoolVar(&opts.SkipSSHKeyPrompt, "skip-ssh-key", false, "Skip generate/upload SSH key prompt")
	cmdutil.NilBoolFlag(cmd, &opts.Expiring, "expiring", "", "Prefer expiring credentials that gh refreshes automatically, if the host supports them")

	return cmd
}

func loginRun(opts *LoginOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()
	copyToClipboard := shared.ShouldCopyToClipboard(cfg, opts.Clipboard)

	hostname := opts.Hostname
	if opts.Interactive && hostname == "" {
		var err error
		hostname, err = promptForHostname(opts)
		if err != nil {
			return err
		}
	}

	// go-gh 的 Config 对象目前不支持对主机名进行大小写不敏感的查找，
	// 因此在用它执行任何查找或持久化之前，先在此处将主机名归一化为小写。
	// https://github.com/cli/go-gh/pull/105
	hostname = strings.ToLower(hostname)

	if src, writeable := shared.AuthTokenWriteable(authCfg, hostname); !writeable {
		fmt.Fprintf(opts.IO.ErrOut, "环境变量 %s 的值正在用于身份验证\n", src)
		fmt.Fprint(opts.IO.ErrOut, "若要改为让 GitHub CLI 存储凭据，请先从环境中清除该值\n")
		return cmdutil.SilentError
	}

	plainHTTPClient, err := opts.PlainHttpClient()
	if err != nil {
		return err
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	if opts.Token != "" {
		if err := shared.HasMinimumScopes(httpClient, hostname, opts.Token); err != nil {
			return fmt.Errorf("验证令牌失败: %w", err)
		}
		username, err := shared.GetCurrentLogin(httpClient, hostname, opts.Token)
		if err != nil {
			return fmt.Errorf("获取当前用户失败: %w", err)
		}

		// 添加 user 键可确保非空的主机部分被写入配置文件。
		_, loginErr := authCfg.Login(hostname, username, opts.Token, opts.GitProtocol, !opts.InsecureStorage)
		return loginErr
	}

	// Nudge users towards expiring tokens unless they have already expressed a preference, either with the flag or
	// the config. Enterprise Server hosts are skipped because they may not support expiring tokens.
	if opts.IO.IsStderrTTY() && opts.Expiring == nil && !ghauth.IsEnterprise(hostname) && !shared.ShouldUseExpiring(cfg, nil, hostname) {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.ErrOut, "%s Did you know that expiring tokens can be requested with %s? Read more at %s\n",
			cs.WarningIcon(), cs.Bold("--expiring"), "https://gh.io/gh-expiring-tokens")
	}

	helperConfig := opts.helperConfig
	if helperConfig == nil {
		helperConfig = &gitcredentials.HelperConfig{
			SelfExecutablePath: opts.MainExecutable,
			GitClient:          opts.GitClient,
		}
	}

	return shared.Login(&shared.LoginOptions{
		IO:              opts.IO,
		Config:          authCfg,
		HTTPClient:      httpClient,
		PlainHTTPClient: plainHTTPClient,
		Hostname:        hostname,
		Interactive:     opts.Interactive,
		Web:             opts.Web,
		Scopes:          opts.Scopes,
		GitProtocol:     opts.GitProtocol,
		Prompter:        opts.Prompter,
		Browser:         opts.Browser,
		CredentialFlow: &shared.GitCredentialFlow{
			Prompter:     opts.Prompter,
			IO:           opts.IO,
			HelperConfig: helperConfig,
			Updater: &gitcredentials.Updater{
				GitClient: opts.GitClient,
			},
		},
		SecureStorage:               !opts.InsecureStorage,
		SkipSSHKeyPrompt:            opts.SkipSSHKeyPrompt,
		CopyToClipboard:             copyToClipboard,
		Expiring:                    shared.ShouldUseExpiring(cfg, opts.Expiring, hostname),
		ExpiringExplicitlyRequested: opts.Expiring != nil && *opts.Expiring,
		Telemetry:                   opts.Telemetry,
		AuthFlow:                    opts.AuthFlow,
	})
}

func promptForHostname(opts *LoginOptions) (string, error) {
	options := []string{"GitHub.com", "Other"}
	hostType, err := opts.Prompter.Select(
	"您在哪里使用 GitHub？",
		options[0],
		options)
	if err != nil {
		return "", err
	}

	isGitHubDotCom := hostType == 0
	if isGitHubDotCom {
		return ghinstance.Default(), nil
	}

	return opts.Prompter.InputHostname()
}
