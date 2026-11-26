package logout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/authflow"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared/gitcredentials"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	shellquote "github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
)

type LogoutOptions struct {
	IO                 *iostreams.IOStreams
	Config             func() (gh.Config, error)
	GitClient          *git.Client
	Prompter           shared.Prompt
	Context            context.Context
	OAuthAppHttpClient func() (*http.Client, error)
	Hostname           string
	Username           string
	NoRevoke           bool
}

func NewCmdLogout(f *cmdutil.Factory, runF func(*LogoutOptions) error) *cobra.Command {
	opts := &LogoutOptions{
		IO:                 f.IOStreams,
		Config:             f.Config,
		GitClient:          f.GitClient,
		Prompter:           f.Prompter,
		OAuthAppHttpClient: f.OAuthAppHttpClient,
	}

	cmd := &cobra.Command{
		Use:   "logout",
		Args:  cobra.ExactArgs(0),
		Short: "Log out of a GitHub account",
		Long: heredoc.Docf(`
			Remove authentication for a GitHub account.

			By default, this command revokes the selected account's GitHub CLI OAuth
			token and removes its saved authentication locally. Copies of the same
			token used elsewhere stop working. Independently issued tokens are not revoked.

			Other token types and unrecognized token formats are removed locally,
			with a warning that they were not revoked.

			After local logout, saved HTTPS credentials for the host are also removed
			from Git credential helpers. If cleanup fails, the command prints a warning
			but still succeeds.

			If revocation fails, including when it cannot be confirmed, local credentials
			are kept so you can retry. The command exits with an error.

			If a refreshable OAuth token is not found, this command refreshes it once,
			saves the replacement credentials, and retries revocation.
			If saving fails, it attempts to revoke the replacement and returns an error
			requiring you to sign in again.

			Use %[1]s--no-revoke%[1]s for local-only logout, without a network request.
			This still prints a warning that the token was not revoked.
		`, "`"),
		Example: heredoc.Doc(`
			# 通过提示选择要登出的主机和账户
			$ gh auth logout

			# 登出特定主机上的特定账户
			$ gh auth logout --hostname enterprise.internal --user monalisa

			# Remove local credentials without revoking the token
			$ gh auth logout --no-revoke
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Context = cmd.Context()
			if runF != nil {
				return runF(opts)
			}

			return logoutRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Hostname, "hostname", "h", "", "The hostname of the GitHub instance to log out of")
	cmd.Flags().StringVarP(&opts.Username, "user", "u", "", "The account to log out of")
	cmd.Flags().BoolVar(&opts.NoRevoke, "no-revoke", false, "Remove local credentials without revoking the GitHub CLI OAuth token")

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

	result := logoutResult{warning: "Authentication token was not revoked."}
	if opts.NoRevoke {
		// A configured host must have an active user.
		result.preLogoutActiveUser, _ = authCfg.ActiveUser(hostname)
		if err := authCfg.Logout(hostname, username); err != nil {
			return err
		}
	} else {
		result, err = logoutWithRevocation(opts, authCfg, hostname, username)
		if err != nil {
			return err
		}
	}

	cs := opts.IO.ColorScheme()
	credentialUpdater := &gitcredentials.Updater{GitClient: opts.GitClient}
	if err := credentialUpdater.RejectExisting(hostname); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "%s Failed to remove credentials from git credential helper: %v\n",
			cs.WarningIcon(), err)
	}

	postLogoutActiveUser, _ := authCfg.ActiveUser(hostname)
	hasSwitchedToNewUser := result.preLogoutActiveUser != postLogoutActiveUser &&
		postLogoutActiveUser != ""

	fmt.Fprintf(opts.IO.ErrOut, "%s Logged out of %s account %s\n",
		cs.SuccessIcon(), hostname, cs.Bold(username))
	if !result.revoked {
		fmt.Fprintf(opts.IO.ErrOut, "%s %s\n", cs.WarningIcon(), result.warning)
		if result.advice != "" {
			fmt.Fprintf(opts.IO.ErrOut, "  %s\n", result.advice)
		}
	}

	if hasSwitchedToNewUser {
		fmt.Fprintf(opts.IO.ErrOut, "%s 已将 %s 的活跃账户切换为 %s\n",
			cs.SuccessIcon(), hostname, cs.Bold(postLogoutActiveUser))
	}

	return nil
}

type logoutResult struct {
	preLogoutActiveUser string
	revoked             bool
	warning             string
	advice              string
}

func logoutWithRevocation(opts *LogoutOptions, authCfg gh.AuthConfig, hostname, username string) (logoutResult, error) {
	result := logoutResult{warning: "Authentication token was not revoked."}
	localLogoutStarted := false
	var saveErr error
	err := authCfg.LogoutWithCredential(hostname, username, func(credential gh.Credential, _ error, save func(gh.RefreshableCredential) error) error {
		result.preLogoutActiveUser, _ = authCfg.ActiveUser(hostname)
		switch {
		case strings.HasPrefix(credential.Token, string(gh.TokenTypeOAuth)):
			client, err := opts.OAuthAppHttpClient()
			if err != nil {
				return err
			}
			ctx := opts.Context
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			err = authflow.RevokeToken(ctx, client, hostname, credential.Token)
			if errors.Is(err, authflow.ErrTokenNotFound) && credential.IsRefreshable() {
				refreshClient := *client
				transport := client.Transport
				if transport == nil {
					transport = http.DefaultTransport
				}
				refreshClient.Transport = logoutContextTransport{ctx: ctx, base: transport}
				refreshClient.CheckRedirect = func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				}
				renewed, refreshErr := authflow.NewTokenRefresher(&refreshClient).Refresh(credential.RefreshToken, hostname)
				if refreshErr != nil {
					// OAuth error messages can echo credentials. Only include known-safe causes.
					switch {
					case errors.Is(refreshErr, gh.ErrRefreshTokenInvalid):
						return fmt.Errorf("could not refresh token for revocation: %w", gh.ErrRefreshTokenInvalid)
					case errors.Is(refreshErr, context.DeadlineExceeded):
						return errors.New("could not refresh token for revocation: request timed out")
					case errors.Is(refreshErr, context.Canceled):
						return fmt.Errorf("could not refresh token for revocation: %w", context.Canceled)
					default:
						return errors.New("could not refresh token for revocation")
					}
				}
				saveErr = save(renewed)
				// Also revoke an unsaved replacement: refreshing already invalidated the old pair.
				err = authflow.RevokeToken(ctx, client, hostname, renewed.AccessToken)
			}
			if err == nil {
				result.revoked = true
				cs := opts.IO.ColorScheme()
				fmt.Fprintf(opts.IO.ErrOut, "%s Revoked GitHub CLI OAuth token for %s (%s)\n",
					cs.SuccessIcon(), cs.Bold(username), hostname)
			}
			if saveErr != nil {
				if err != nil {
					err = fmt.Errorf("could not revoke unsaved replacement: %w", err)
				}
				return errors.Join(fmt.Errorf("could not save refreshed credentials: %w", saveErr), err)
			}
			if err != nil {
				return fmt.Errorf("failed to revoke GitHub CLI OAuth token: %w", err)
			}
		case strings.HasPrefix(credential.Token, string(gh.TokenTypePersonalAccess)),
			strings.HasPrefix(credential.Token, string(gh.TokenTypeFineGrainedPAT)):
			result.warning = "Personal access token was not revoked."
			result.advice = "Revoke it at " + ghinstance.HostPrefix(hostname) + "settings/tokens"
		case strings.HasPrefix(credential.Token, string(gh.TokenTypeUserToServer)),
			strings.HasPrefix(credential.Token, string(gh.TokenTypeServerToServer)):
			result.warning = "GitHub App token was not revoked."
			result.advice = "Revoke it through the app that issued it."
		}
		localLogoutStarted = true
		return nil
	})
	if err != nil && !localLogoutStarted {
		if saveErr != nil {
			login := shellquote.Join("gh", "auth", "login", "--hostname", hostname)
			return result, fmt.Errorf("%w\nRefreshed credentials could not be saved. Sign in again with:\n  %s", err, login)
		}
		recovery := shellquote.Join("gh", "auth", "logout", "--hostname", hostname, "--user", username, "--no-revoke")
		err = fmt.Errorf("%w\nLocal credentials were kept.\nRetry, or log out locally without revoking:\n  %s", err, recovery)
	}
	return result, err
}

type logoutContextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t logoutContextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(t.ctx))
}
