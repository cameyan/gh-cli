package gitcredentials

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/google/shlex"
)

// HelperConfig 用于配置和检查 git 凭据助手(credential helper)的状态。
type HelperConfig struct {
	SelfExecutablePath string
	GitClient          *git.Client
}

// ConfigureOurs 设置 git 凭据助手链，将 GitHub CLI 凭据助手用于 git 仓库
// (包括 gists)。
func (hc *HelperConfig) ConfigureOurs(hostname string) error {
	ctx := context.TODO()

	credHelperKeys := []string{
		keyFor(hostname),
	}

	gistHost := strings.TrimSuffix(ghinstance.GistHost(hostname), "/")
	if strings.HasPrefix(gistHost, "gist.") {
		credHelperKeys = append(credHelperKeys, keyFor(gistHost))
	}

	var configErr error

	for _, credHelperKey := range credHelperKeys {
		if configErr != nil {
			break
		}
		// 首先使用空值告知 git，我们要切断凭据助手链
		preConfigureCmd, err := hc.GitClient.Command(ctx, "config", "--global", "--replace-all", credHelperKey, "")
		if err != nil {
			configErr = err
			break
		}
		if _, err = preConfigureCmd.Output(); err != nil {
			configErr = err
			break
		}

		// 其次为该主机配置实际的助手
		configureCmd, err := hc.GitClient.Command(ctx,
			"config", "--global", "--add",
			credHelperKey,
			fmt.Sprintf("!%s auth git-credential", shellQuote(hc.SelfExecutablePath)),
		)
		if err != nil {
			configErr = err
		} else {
			_, configErr = configureCmd.Output()
		}
	}

	return configErr
}

// Helper 表示一个 git 凭据助手配置。
type Helper struct {
	Cmd string
}

// IsConfigured 在助手的命令非空时返回 true，即 git config 中有条目
func (h Helper) IsConfigured() bool {
	return h.Cmd != ""
}

// IsOurs 在助手命令是 GitHub CLI 凭据助手时返回 true
func (h Helper) IsOurs() bool {
	if !strings.HasPrefix(h.Cmd, "!") {
		return false
	}

	args, err := shlex.Split(h.Cmd[1:])
	if err != nil || len(args) == 0 {
		return false
	}

	return strings.TrimSuffix(filepath.Base(args[0]), ".exe") == "gh"
}

// ConfiguredHelper 返回给定主机名已配置的 git 凭据助手。
func (hc *HelperConfig) ConfiguredHelper(hostname string) (Helper, error) {
	ctx := context.TODO()

	hostHelperCmd, err := hc.GitClient.Config(ctx, keyFor(hostname))
	if hostHelperCmd != "" {
		// TODO: 这是一次直接重构，移除了具名返回和裸返回，
		// 但我们或许应该更仔细地审视这里的错误处理
		return Helper{
			Cmd: hostHelperCmd,
		}, err
	}

	globalHelperCmd, err := hc.GitClient.Config(ctx, "credential.helper")
	if globalHelperCmd != "" {
		return Helper{
			Cmd: globalHelperCmd,
		}, err
	}

	return Helper{}, nil
}

func keyFor(hostname string) string {
	host := strings.TrimSuffix(ghinstance.HostPrefix(hostname), "/")
	return fmt.Sprintf("credential.%s.helper", host)
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " $\\") {
		return "'" + s + "'"
	}
	return s
}
