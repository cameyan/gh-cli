package shared

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/authflow"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/ssh-key/add"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/ssh"
)

const defaultSSHKeyTitle = "GitHub CLI"

type iconfig interface {
	Login(string, string, string, string, bool) (bool, error)
	UsersForHost(string) []string
}

type LoginOptions struct {
	IO               *iostreams.IOStreams
	Config           iconfig
	HTTPClient       *http.Client
	PlainHTTPClient  *http.Client
	Hostname         string
	Interactive      bool
	Web              bool
	Scopes           []string
	GitProtocol      string
	Prompter         Prompt
	Browser          browser.Browser
	CredentialFlow   *GitCredentialFlow
	SecureStorage    bool
	SkipSSHKeyPrompt bool
	CopyToClipboard  bool

	sshContext ssh.Context
}

// ShouldCopyToClipboard 先解析显式的标志值，然后回退到配置。
func ShouldCopyToClipboard(cfg gh.Config, flagValue *bool) bool {
	if flagValue != nil {
		return *flagValue
	}
	return cfg.Clipboard().Value == "enabled"
}

func Login(opts *LoginOptions) error {
	cfg := opts.Config
	hostname := opts.Hostname
	httpClient := opts.HTTPClient
	cs := opts.IO.ColorScheme()

	gitProtocol := strings.ToLower(opts.GitProtocol)
	if opts.Interactive && gitProtocol == "" {
		options := []string{
			"HTTPS",
			"SSH",
		}
		result, err := opts.Prompter.Select(
			"在此主机上进行 Git 操作时，您首选的协议是什么？",
			options[0],
			options)
		if err != nil {
			return err
		}
		proto := options[result]
		gitProtocol = strings.ToLower(proto)
	}

	var additionalScopes []string

	if opts.Interactive && gitProtocol == "https" {
		if err := opts.CredentialFlow.Prompt(hostname); err != nil {
			return err
		}
		additionalScopes = append(additionalScopes, opts.CredentialFlow.Scopes()...)
	}

	var keyToUpload string
	keyTitle := defaultSSHKeyTitle
	if opts.Interactive && !opts.SkipSSHKeyPrompt && gitProtocol == "ssh" {
		pubKeys, err := opts.sshContext.LocalPublicKeys()
		if err != nil {
			return err
		}

		if len(pubKeys) > 0 {
			options := append(pubKeys, "跳过")
			keyChoice, err := opts.Prompter.Select(
				"将您的 SSH 公钥上传到 GitHub 账户？",
				options[0],
				options)
			if err != nil {
				return err
			}
			if keyChoice < len(pubKeys) {
				keyToUpload = pubKeys[keyChoice]
			}
		} else if opts.sshContext.HasKeygen() {
			sshChoice, err := opts.Prompter.Confirm("生成新的 SSH 密钥以添加到您的 GitHub 账户？", true)
			if err != nil {
				return err
			}

			if sshChoice {
				passphrase, err := opts.Prompter.Password(
					"输入新 SSH 密钥的密码短语(可选): ")
				if err != nil {
					return err
				}
				keyPair, err := opts.sshContext.GenerateSSHKey("id_ed25519", passphrase)
				if err != nil {
					return err
				}
				keyToUpload = keyPair.PublicKeyPath
			}
		}

		if keyToUpload != "" {
			var err error
			keyTitle, err = opts.Prompter.Input(
				"SSH 密钥的标题:", defaultSSHKeyTitle)
			if err != nil {
				return err
			}

			additionalScopes = append(additionalScopes, "admin:public_key")
		}
	}

	var authMode int
	if opts.Web {
		authMode = 0
	} else if opts.Interactive {
		options := []string{"使用网页浏览器登录", "粘贴身份验证令牌"}
		var err error
		authMode, err = opts.Prompter.Select(
			"您希望如何向 GitHub CLI 进行身份验证？",
			options[0],
			options)
		if err != nil {
			return err
		}
	}

	var authToken string
	var username string

	if authMode == 0 {
		var err error
		authToken, username, err = authflow.AuthFlow(opts.PlainHTTPClient, hostname, opts.IO, "", append(opts.Scopes, additionalScopes...), opts.Interactive, opts.Browser, opts.CopyToClipboard)
		if err != nil {
			return fmt.Errorf("通过网页浏览器进行身份验证失败: %w", err)
		}
		fmt.Fprintf(opts.IO.ErrOut, "%s 身份验证完成。\n", cs.SuccessIcon())
	} else {
		minimumScopes := append([]string{"repo", "read:org"}, additionalScopes...)
		fmt.Fprint(opts.IO.ErrOut, heredoc.Docf(`
			提示: 您可以在此处生成个人访问令牌 https://%s/settings/tokens，
			所需的最低作用域为 %s。
		`, hostname, scopesSentence(minimumScopes)))

		var err error
		authToken, err = opts.Prompter.AuthToken()
		if err != nil {
			return err
		}

		if err := HasMinimumScopes(httpClient, hostname, authToken); err != nil {
			return fmt.Errorf("验证令牌失败: %w", err)
		}
	}

	if username == "" {
		var err error
		username, err = GetCurrentLogin(httpClient, hostname, authToken)
		if err != nil {
			return fmt.Errorf("获取当前用户失败: %w", err)
		}
	}

	// 在添加新用户之前获取这些用户，以便稍后
	// 检查该用户是否已经登录。
	//
	// 在这种情况下，若主机不存在则忽略错误，
	// 因为用户首次登录某台主机时可能会发生这种情况。
	usersForHost := cfg.UsersForHost(hostname)
	userWasAlreadyLoggedIn := slices.Contains(usersForHost, username)

	if gitProtocol != "" {
		fmt.Fprintf(opts.IO.ErrOut, "- gh config set -h %s git_protocol %s\n", hostname, gitProtocol)
		fmt.Fprintf(opts.IO.ErrOut, "%s 已配置 git 协议\n", cs.SuccessIcon())
	}

	insecureStorageUsed, err := cfg.Login(hostname, username, authToken, gitProtocol, opts.SecureStorage)
	if err != nil {
		return err
	}
	if insecureStorageUsed {
		fmt.Fprintf(opts.IO.ErrOut, "%s 身份验证凭据已保存为纯文本\n", cs.Yellow("!"))
	}

	if opts.CredentialFlow.ShouldSetup() {
		err := opts.CredentialFlow.Setup(hostname, username, authToken)
		if err != nil {
			return err
		}
	}

	if keyToUpload != "" {
		uploaded, err := sshKeyUpload(httpClient, hostname, keyToUpload, keyTitle)
		if err != nil {
			return err
		}

		if uploaded {
			fmt.Fprintf(opts.IO.ErrOut, "%s 已将 SSH 密钥上传到您的 GitHub 账户: %s\n", cs.SuccessIcon(), cs.Bold(keyToUpload))
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "%s SSH 密钥已存在于您的 GitHub 账户中: %s\n", cs.SuccessIcon(), cs.Bold(keyToUpload))
		}
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已以 %s 身份登录\n", cs.SuccessIcon(), cs.Bold(username))
	if userWasAlreadyLoggedIn {
		fmt.Fprintf(opts.IO.ErrOut, "%s 您已经登录过此账户\n", cs.WarningIcon())
	}

	return nil
}

func scopesSentence(scopes []string) string {
	quoted := make([]string, len(scopes))
	for i, s := range scopes {
		quoted[i] = fmt.Sprintf("'%s'", s)
	}
	return strings.Join(quoted, ", ")
}

func sshKeyUpload(httpClient *http.Client, hostname, keyFile string, title string) (bool, error) {
	f, err := os.Open(keyFile)
	if err != nil {
		return false, err
	}
	defer f.Close()

	return add.SSHKeyUpload(httpClient, hostname, f, title)
}

// GetCurrentLogin 返回令牌所属用户的登录名。
//
// 显式传入令牌是因为此函数在令牌存入配置之前运行，
// 因此 transport 没有可附加的内容。transport 仅在 Authorization
// 缺失时才设置它，因此此处设置的头会生效。
func GetCurrentLogin(httpClient *http.Client, hostname, authToken string) (string, error) {
	var result struct{ Viewer struct{ Login string } }

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err := api.NewClientFromHTTP(httpClient).GraphQL(hostname, `query UserCurrent{viewer{login}}`, nil, &result,
		api.WithHeader("Authorization", "token "+authToken))
	if err != nil {
		return "", err
	}
	return result.Viewer.Login, nil
}
