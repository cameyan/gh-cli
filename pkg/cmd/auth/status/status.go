package status

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type authEntryState string

const (
	authEntryStateSuccess        = "success"
	authEntryStateTimeout        = "timeout"
	authEntryStateError          = "error"
	authEntryStateRefreshExpired = "refresh_expired"
)

type authEntry struct {
	State                 authEntryState `json:"state"`
	Error                 string         `json:"error,omitempty"`
	Active                bool           `json:"active"`
	Host                  string         `json:"host"`
	Login                 string         `json:"login"`
	TokenSource           string         `json:"tokenSource"`
	Token                 string         `json:"token,omitempty"`
	Scopes                string         `json:"scopes,omitempty"`
	GitProtocol           string         `json:"gitProtocol"`
	Refreshable           bool           `json:"refreshable,omitempty"`
	TokenExpiresAt        *time.Time     `json:"tokenExpiresAt,omitempty"`
	RefreshTokenExpiresAt *time.Time     `json:"refreshTokenExpiresAt,omitempty"`
	// justRefreshed reports whether gh renewed this account's expiring token during this run. It drives a
	// display-only notice and is never serialized.
	justRefreshed bool
	// expiredWithoutRefresh reports whether the token check failed because the expiring token had already expired
	// and --no-refresh kept gh from renewing it. It drives a display-only hint and is never serialized.
	expiredWithoutRefresh bool
}

type authStatus struct {
	Hosts map[string][]authEntry `json:"hosts"`
}

func newAuthStatus() *authStatus {
	return &authStatus{
		Hosts: make(map[string][]authEntry),
	}
}

var authStatusFields = []string{
	"hosts",
}

func (a authStatus) ExportData(fields []string) map[string]any {
	return cmdutil.StructExportData(a, fields)
}

// timeNow is the clock used to render relative token-expiry times. It is a package variable so tests can pin it for
// deterministic output.
var timeNow = time.Now

// relativeToNow renders a fuzzy, human readable description of when t occurs relative to the current time, for
// annotating the absolute expiry timestamp. Future times read as "in about 7 hours" and already-elapsed times as
// "expired about 2 days ago".
func relativeToNow(t time.Time) string {
	now := timeNow()
	if t.After(now) {
		return text.FuzzyIn(now, t)
	}
	return "expired " + text.FuzzyAgo(now, t)
}

func (e authEntry) String(cs *iostreams.ColorScheme) string {
	var sb strings.Builder

	switch e.State {
	case authEntryStateSuccess:
		sb.WriteString(
			fmt.Sprintf("  %s 已登录 %s 的账户 %s (%s)\n", cs.SuccessIcon(), e.Host, cs.Bold(e.Login), e.TokenSource),
		)
		activeStr := fmt.Sprintf("%v", e.Active)
		sb.WriteString(fmt.Sprintf("  - 活跃账户: %s\n", cs.Bold(activeStr)))
		sb.WriteString(fmt.Sprintf("  - Git 操作协议: %s\n", cs.Bold(e.GitProtocol)))
		sb.WriteString(fmt.Sprintf("  - 令牌: %s\n", cs.Bold(e.Token)))

		if e.Refreshable {
			e.writeExpiry(&sb, cs)
			if e.justRefreshed {
				sb.WriteString("  - Token refreshed just now\n")
			}
		}

		if expectScopes(e.Token) {
			sb.WriteString(fmt.Sprintf("  - 令牌作用域: %s\n", cs.Bold(displayScopes(e.Scopes))))
			if err := shared.HeaderHasMinimumScopes(e.Scopes); err != nil {
				if missingScopesError, ok := errors.AsType[*shared.MissingScopesError](err); ok {
					missingScopes := strings.Join(missingScopesError.MissingScopes, ",")
					sb.WriteString(fmt.Sprintf("  %s 缺少所需的令牌作用域: %s\n",
						cs.WarningIcon(),
						cs.Bold(displayScopes(missingScopes))))
					refreshInstructions := fmt.Sprintf("gh auth refresh -h %s", e.Host)
					sb.WriteString(fmt.Sprintf("  - 若要请求缺失的作用域，请运行: %s\n", cs.Bold(refreshInstructions)))
				}
			}
		}

	case authEntryStateError:
		if e.expiredWithoutRefresh {
			sb.WriteString(fmt.Sprintf("  %s Token for %s account %s (%s) has expired\n", cs.Red("X"), e.Host, cs.Bold(e.Login), e.TokenSource))
			activeStr := fmt.Sprintf("%v", e.Active)
			sb.WriteString(fmt.Sprintf("  - Active account: %s\n", cs.Bold(activeStr)))
			e.writeExpiry(&sb, cs)
			if e.RefreshTokenExpiresAt != nil && !e.RefreshTokenExpiresAt.After(timeNow()) {
				loginInstructions := fmt.Sprintf("gh auth login -h %s", e.Host)
				sb.WriteString(fmt.Sprintf("  - To re-authenticate, run: %s\n", cs.Bold(loginInstructions)))
			} else {
				sb.WriteString(fmt.Sprintf("  - To refresh it, run %s (without %s)\n", cs.Bold("gh auth status"), cs.Bold("--no-refresh")))
			}
			break
		}
		if e.Login != "" {
			sb.WriteString(fmt.Sprintf("  %s 登录 %s 的账户 %s 失败 (%s)\n", cs.Red("X"), e.Host, cs.Bold(e.Login), e.TokenSource))
		} else {
			sb.WriteString(fmt.Sprintf("  %s 使用令牌登录 %s 失败 (%s)\n", cs.Red("X"), e.Host, e.TokenSource))
		}
		activeStr := fmt.Sprintf("%v", e.Active)
		sb.WriteString(fmt.Sprintf("  - 活跃账户: %s\n", cs.Bold(activeStr)))
		sb.WriteString(fmt.Sprintf("  - %s 中的令牌无效。\n", e.TokenSource))
		if authTokenWriteable(e.TokenSource) {
			loginInstructions := fmt.Sprintf("gh auth login -h %s", e.Host)
			if shared.AuthTokenRefreshable(e.Token, e.TokenSource) {
				loginInstructions = fmt.Sprintf("gh auth refresh -h %s", e.Host)
			}
			logoutInstructions := fmt.Sprintf("gh auth logout -h %s -u %s", e.Host, e.Login)
			sb.WriteString(fmt.Sprintf("  - 若要重新进行身份验证，请运行: %s\n", cs.Bold(loginInstructions)))
			sb.WriteString(fmt.Sprintf("  - 若要忘记此账户，请运行: %s\n", cs.Bold(logoutInstructions)))
		}

	case authEntryStateTimeout:
		if e.Login != "" {
			sb.WriteString(fmt.Sprintf("  %s 登录 %s 的账户 %s 时超时 (%s)\n", cs.Red("X"), e.Host, cs.Bold(e.Login), e.TokenSource))
		} else {
			sb.WriteString(fmt.Sprintf("  %s 使用令牌登录 %s 时超时 (%s)\n", cs.Red("X"), e.Host, e.TokenSource))
		}
		activeStr := fmt.Sprintf("%v", e.Active)
		sb.WriteString(fmt.Sprintf("  - Active account: %s\n", cs.Bold(activeStr)))

	case authEntryStateRefreshExpired:
		if e.Login != "" {
			sb.WriteString(fmt.Sprintf("  %s Refresh token for %s account %s was rejected\n", cs.Red("X"), e.Host, cs.Bold(e.Login)))
		} else {
			sb.WriteString(fmt.Sprintf("  %s Refresh token for %s was rejected\n", cs.Red("X"), e.Host))
		}
		activeStr := fmt.Sprintf("%v", e.Active)
		sb.WriteString(fmt.Sprintf("  - Active account: %s\n", cs.Bold(activeStr)))
		sb.WriteString("  - The stored expiring credential was removed because its refresh token is no longer valid.\n")
		loginInstructions := fmt.Sprintf("gh auth login -h %s", e.Host)
		sb.WriteString(fmt.Sprintf("  - To re-authenticate, run: %s\n", cs.Bold(loginInstructions)))
	}

	if e.Error != "" {
		sb.WriteString(fmt.Sprintf("  - Error: %s\n", e.Error))
	}

	return sb.String()
}

func (e authEntry) writeExpiry(sb *strings.Builder, cs *iostreams.ColorScheme) {
	sb.WriteString("  - Expiring token that gh refreshes automatically\n")
	if e.TokenExpiresAt != nil {
		expiresAt := e.TokenExpiresAt.UTC()
		sb.WriteString(fmt.Sprintf("  - Token expires: %s (%s)\n",
			cs.Bold(expiresAt.Format(time.RFC3339)), relativeToNow(expiresAt)))
	}
	if e.RefreshTokenExpiresAt != nil {
		refreshExpiresAt := e.RefreshTokenExpiresAt.UTC()
		sb.WriteString(fmt.Sprintf("  - Refresh token expires: %s (%s)\n",
			cs.Bold(refreshExpiresAt.Format(time.RFC3339)), relativeToNow(refreshExpiresAt)))
	}
}

type StatusOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	Exporter   cmdutil.Exporter

	Hostname  string
	ShowToken bool
	Active    bool
	NoRefresh bool
}

func NewCmdStatus(f *cmdutil.Factory, runF func(*StatusOptions) error) *cobra.Command {
	opts := &StatusOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "status",
		Args:  cobra.ExactArgs(0),
		Short: "显示每个已知 GitHub 主机上的活跃账户和身份验证状态",
		Long: heredoc.Docf(`
			显示每个已知 GitHub 主机上的活跃账户和身份验证状态。

			对于每个主机，将测试每个已知账户的身份验证状态，任何问题都会包含在输出中。
			每个主机部分将指示活跃账户，访问该主机时将使用该账户。

			如果任何主机上的账户(或仅 %[1]s--hostname%[1]s 指定的主机上的账户)存在身份验证问题，
			该命令将以退出码 1 退出并将结果输出到 stderr。注意，使用 %[1]s--json%[1]s 选项时，该命令
			将始终以零退出，除非发生致命错误，与身份验证问题无关。

			Any expired or near-expiry token that supports refreshing is refreshed automatically, unless %[1]s--no-refresh%[1]s is given.

			To change the active account for a host, see %[1]sgh auth switch%[1]s.
		`, "`"),
		Example: heredoc.Doc(`
			# 显示所有主机上所有账户的身份验证状态
			$ gh auth status

			# 显示特定主机上活跃账户的身份验证状态
			$ gh auth status --active --hostname github.example.com

			# 以纯文本显示令牌
			$ gh auth status --show-token

			# 将身份验证状态格式化为 JSON
			$ gh auth status --json hosts

			# 在 JSON 输出中包含纯文本令牌
			$ gh auth status --json hosts --show-token

			# 将 hosts 格式化为扁平的 JSON 数组
			$ gh auth status --json hosts --jq '.hosts | add'
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}

			return statusRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "Check only a specific hostname's auth status")
	cmd.Flags().BoolVarP(&opts.ShowToken, "show-token", "t", false, "Display the auth token")
	cmd.Flags().BoolVarP(&opts.Active, "active", "a", false, "Display the active account only")
	cmd.Flags().BoolVar(&opts.NoRefresh, "no-refresh", false, "Do not automatically refresh expired tokens")

	// json 标志刻意不设置简写，以避免与 -t/--show-token 冲突
	cmdutil.AddJSONFlagsWithoutShorthand(cmd, &opts.Exporter, authStatusFields)

	return cmd
}

func statusRun(opts *StatusOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	authCfg := cfg.Authentication()

	stderr := opts.IO.ErrOut
	stdout := opts.IO.Out
	cs := opts.IO.ColorScheme()

	hostnames := authCfg.Hosts()
	if len(hostnames) == 0 {
		fmt.Fprintf(stderr,
			"您尚未登录任何 GitHub 主机。若要登录，请运行: %s\n", cs.Bold("gh auth login"))
		if opts.Exporter != nil {
			// 在机器友好模式下，我们始终以无错误状态退出。
			opts.Exporter.Write(opts.IO, newAuthStatus())
			return nil
		}
		return cmdutil.SilentError
	}

	if opts.Hostname != "" && !slices.Contains(hostnames, opts.Hostname) {
		fmt.Fprintf(stderr,
			"您尚未在 %s 上登录任何账户\n", opts.Hostname)
		if opts.Exporter != nil {
			// 在机器友好模式下，我们始终以无错误状态退出。
			opts.Exporter.Write(opts.IO, newAuthStatus())
			return nil
		}
		return cmdutil.SilentError
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	var finalErr error
	statuses := newAuthStatus()

	for _, hostname := range hostnames {
		if opts.Hostname != "" && opts.Hostname != hostname {
			continue
		}

		var activeUser string
		gitProtocol := cfg.GitProtocol(hostname).Value
		activeCred, activeRefreshStatus := resolveCredential(authCfg, hostname, "", opts.NoRefresh)
		if authTokenWriteable(activeCred.Source) {
			activeUser, _ = authCfg.ActiveUser(hostname)
		}
		entry := buildEntry(httpClient, buildEntryOptions{
			active:        true,
			gitProtocol:   gitProtocol,
			hostname:      hostname,
			credential:    activeCred,
			refreshStatus: activeRefreshStatus,
			noRefresh:     opts.NoRefresh,
			username:      activeUser,
		})
		statuses.Hosts[hostname] = append(statuses.Hosts[hostname], entry)

		if finalErr == nil && entry.State != authEntryStateSuccess {
			finalErr = cmdutil.SilentError
		}

		if opts.Active {
			continue
		}

		users := authCfg.UsersForHost(hostname)
		for _, username := range users {
			if username == activeUser {
				continue
			}
			cred, refreshStatus := resolveCredential(authCfg, hostname, username, opts.NoRefresh)
			entry := buildEntry(httpClient, buildEntryOptions{
				active:        false,
				gitProtocol:   gitProtocol,
				hostname:      hostname,
				credential:    cred,
				refreshStatus: refreshStatus,
				noRefresh:     opts.NoRefresh,
				username:      username,
			})
			statuses.Hosts[hostname] = append(statuses.Hosts[hostname], entry)

			if finalErr == nil && entry.State != authEntryStateSuccess {
				finalErr = cmdutil.SilentError
			}
		}
	}

	if !opts.ShowToken {
		for _, host := range statuses.Hosts {
			for i := range host {
				if opts.Exporter != nil {
					// 在机器可读模式中，我们直接丢弃令牌
					host[i].Token = ""
				} else {
					host[i].Token = maskToken(host[i].Token)
				}
			}
		}
	}

	if opts.Exporter != nil {
		// 在机器友好模式下，我们始终以无错误状态退出。
		opts.Exporter.Write(opts.IO, statuses)
		return nil
	}

	prevEntry := false
	for _, hostname := range hostnames {
		entries, ok := statuses.Hosts[hostname]
		if !ok {
			continue
		}

		stream := stdout
		if finalErr != nil {
			stream = stderr
		}

		if prevEntry {
			fmt.Fprint(stream, "\n")
		}
		prevEntry = true
		fmt.Fprintf(stream, "%s\n", cs.Bold(hostname))
		for i, entry := range entries {
			fmt.Fprintf(stream, "%s", entry.String(cs))
			if i < len(entries)-1 {
				fmt.Fprint(stream, "\n")
			}
		}
	}

	return finalErr
}

// knownTokenPrefixes 包含 GitHub 的令牌格式前缀。
// 参见 [GitHub token formats]。
//
// TODO: gh.TokenTypes 现在带有相同的前缀，因此此列表与之重复。
// 改用那个。
//
// [GitHub token formats]: https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-authentication-to-github#githubs-token-formats
var knownTokenPrefixes = []string{"github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_"}

func maskToken(token string) string {
	for _, prefix := range knownTokenPrefixes {
		if strings.HasPrefix(token, prefix) {
			return prefix + strings.Repeat("*", len(token)-len(prefix))
		}
	}
	return strings.Repeat("*", len(token))
}

func displayScopes(scopes string) string {
	if scopes == "" {
		return "无"
	}
	list := strings.Split(scopes, ",")
	for i, s := range list {
		list[i] = fmt.Sprintf("'%s'", strings.TrimSpace(s))
	}
	return strings.Join(list, ", ")
}

// TODO: 它自行匹配令牌前缀。本可以改问
// gh.AuthConfig.ActiveTokenType。
func expectScopes(token string) bool {
	return strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_")
}

type buildEntryOptions struct {
	active        bool
	gitProtocol   string
	hostname      string
	credential    gh.Credential
	refreshStatus gh.RefreshStatus
	noRefresh     bool
	username      string
}

func buildEntry(httpClient *http.Client, opts buildEntryOptions) authEntry {
	tokenSource := opts.credential.Source
	if tokenSource == gh.TokenSourceOAuthToken || tokenSource == gh.TokenSourceRefreshableOAuthToken {
		// The go-gh function TokenForHost returns this value as source for tokens read from the
		// config file, but we want the file path instead. This attempts to reconstruct it.
		tokenSource = filepath.Join(config.ConfigDir(), "hosts.yml")
	}
	entry := authEntry{
		Active:                opts.active,
		Host:                  opts.hostname,
		Login:                 opts.username,
		TokenSource:           tokenSource,
		Token:                 opts.credential.Token,
		GitProtocol:           opts.gitProtocol,
		Refreshable:           opts.credential.IsRefreshable(),
		TokenExpiresAt:        opts.credential.ExpiresAt,
		RefreshTokenExpiresAt: opts.credential.RefreshTokenExpiresAt,
		justRefreshed:         opts.refreshStatus == gh.RefreshStatusDone,
	}

	// A rejected refresh token means the stored credential was already removed during resolution, leaving an empty
	// token behind. Sending a scope request with it would produce a misleading generic failure, so surface the real
	// reason and stop here. The token source is left empty on purpose: the credential is gone, so where it used to
	// live is of no use to the user now.
	if opts.refreshStatus == gh.RefreshStatusExpired {
		entry.State = authEntryStateRefreshExpired
		entry.Error = fmt.Sprintf("refresh token for %s was rejected; the stored credential was removed", opts.hostname)
		return entry
	}

	// 如果令牌不可写，则它来自环境变量，
	// 我们需要获取用户名，因为它不会存储在配置中。
	if !authTokenWriteable(tokenSource) {
		// httpClient 会自动使用正确的令牌，因为
		// 来自环境变量的令牌具有最高优先级。
		apiClient := api.NewClientFromHTTP(httpClient)
		var err error
		entry.Login, err = api.CurrentLoginName(apiClient, opts.hostname)
		if err != nil {
			entry.State = authEntryStateError
			entry.Error = err.Error()
			return entry
		}
	}

	// Get scopes for token.
	scopesHeader, err := shared.GetScopes(httpClient, opts.hostname, opts.credential.Token)
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			entry.State = authEntryStateTimeout
			entry.Error = err.Error()
			return entry
		}

		entry.State = authEntryStateError
		entry.Error = err.Error()
		// Without a refresh, an expiring token past its expiry time is bound to be rejected with HTTP 401. Flag it so
		// the output can explain the failure instead of reporting the token as invalid. The expiry check runs only
		// after a rejection, so a server that still accepts the token (for example, under clock skew) reports success,
		// and unrelated failures (for example, HTTP 503) keep the generic error output.
		httpErr, isHTTPErr := errors.AsType[api.HTTPError](err)
		entry.expiredWithoutRefresh = opts.noRefresh && entry.Refreshable &&
			isHTTPErr && httpErr.StatusCode == http.StatusUnauthorized &&
			entry.TokenExpiresAt != nil && !entry.TokenExpiresAt.After(timeNow())
		return entry
	}
	entry.Scopes = scopesHeader

	entry.State = authEntryStateSuccess
	return entry
}

func authTokenWriteable(src string) bool {
	return !strings.HasSuffix(src, "_TOKEN")
}

// resolveCredential returns the credential gh should present for the host and optional user (an empty username means the
// active account), refreshing an expiring token unless noRefresh is set. Refresh is best effort: a failed
// refresh yields the last stored credential, which the caller still validates via the scope check. The returned status
// reports whether a refresh happened.
func resolveCredential(authCfg gh.AuthConfig, hostname, username string, noRefresh bool) (gh.Credential, gh.RefreshStatus) {
	if noRefresh {
		if username == "" {
			return authCfg.ActiveToken(hostname), gh.RefreshStatusInapplicable
		}
		credential, _ := authCfg.TokenForUser(hostname, username)
		return credential, gh.RefreshStatusInapplicable
	}
	if username == "" {
		credential, status, _ := authCfg.ActiveTokenWithRefresh(hostname)
		return credential, status
	}
	credential, status, _ := authCfg.TokenForUserWithRefresh(hostname, username)
	return credential, status
}
