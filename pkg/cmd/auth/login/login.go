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
			Authenticate with a GitHub host.

			The default hostname is %[1]sgithub.com%[1]s. This can be overridden using the %[1]s--hostname%[1]s
			flag.

			The default authentication mode is a web-based browser flow. After completion, an
			authentication token will be stored securely in the system credential store.
			If a credential store is not found or there is an issue using it gh will fallback
			to writing the token to a plain text file. See %[1]sgh auth status%[1]s for its
			stored location.

			Alternatively, use %[1]s--with-token%[1]s to pass in a personal access token (classic) on standard input.
			The minimum required scopes for the token are: %[1]srepo%[1]s, %[1]sread:org%[1]s, and %[1]sgist%[1]s.
			Take care when passing a fine-grained personal access token to %[1]s--with-token%[1]s
			as the inherent scoping to certain resources may cause confusing behaviour when interacting with other
			resources. Favour setting %[1]sGH_TOKEN%[1]s for fine-grained personal access token usage.

			Alternatively, gh will use the authentication token found in environment variables.
			This method is most suitable for "headless" use of gh such as in automation. See
			%[1]sgh help environment%[1]s for more info.

			To use gh in GitHub Actions, add %[1]sGH_TOKEN: ${{ github.token }}%[1]s to %[1]senv%[1]s.

			The git protocol to use for git operations on this host can be set with %[1]s--git-protocol%[1]s,
			or during the interactive prompting. Although login is for a single account on a host, setting
			the git protocol will take effect for all users on the host.

			Specifying %[1]sssh%[1]s for the git protocol will detect existing SSH keys to upload,
			prompting to create and upload a new key if one is not found. This can be skipped with
			%[1]s--skip-ssh-key%[1]s flag.

			Use %[1]s--expiring%[1]s to prefer expiring credentials that gh refreshes automatically.
			This is a preference, not a guarantee: whether expiring tokens are issued depends on the
			host and the OAuth app configuration. GitHub Enterprise Server requires version 3.23 or
			later; earlier versions receive a non-expiring token instead. A host configured for them
			may issue expiring tokens even without the flag.
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
				return cmdutil.FlagErrorf("只能指定 `--expiring` 或 `--with-token` 其中之一")
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

	cmd.Flags().BoolVar(&opts.InsecureStorage, "insecure-storage", false, "以明文形式保存身份验证凭据，而非使用凭据存储库")
	cmd.Flags().BoolVar(&opts.SkipSSHKeyPrompt, "skip-ssh-key", false, "跳过生成/上传 SSH 密钥提示")
	cmdutil.NilBoolFlag(cmd, &opts.Expiring, "expiring", "", "如果主机支持，优先使用 GitHub 自动刷新的含过期时间凭证")

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
		fmt.Fprintf(opts.IO.ErrOut, "%s 你知道吗？已过期的令牌可以通过 %s 重新申请？更多信息请访问 %s\n",
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
