package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MakeNowJust/heredoc"
	cmdCreate "github.com/cli/cli/v2/pkg/cmd/agent-task/create"
	cmdList "github.com/cli/cli/v2/pkg/cmd/agent-task/list"
	cmdView "github.com/cli/cli/v2/pkg/cmd/agent-task/view"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

// NewCmdAgentTask 创建基础的 `agent-task` 命令。
func NewCmdAgentTask(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agent-task <command>",
		Aliases: []string{"agent-tasks", "agent", "agents"},
		Short:   "操作 agent task(预览版)",
		Long: heredoc.Doc(`
			在 GitHub CLI 中操作 agent 任务目前处于预览阶段，可能会在不另行通知的情况下变更。
		`),
		Annotations: map[string]string{
			"help:arguments": heredoc.Doc(`
				task 可以通过以下任意一种格式作为参数来标识:
				- 通过 PR 编号，例如 "123"
				- 通过 session ID，例如 "12345abc-12345-12345-12345-12345abc"
				- 通过 URL，例如 "https://github.com/OWNER/REPO/pull/123/agent-sessions/12345abc-12345-12345-12345-12345abc"

				对于非交互式使用场景，不建议通过 PR 来标识 task，因为给定的 PR 可能存在多个需要区分的任务。
			`),
		},
		Example: heredoc.Doc(`
			# 列出你最近的 agent 任务
			$ gh agent-task list
			
			# 在当前仓库创建一个新的 agent 任务
			$ gh agent-task create "提升数据处理管道的性能"
			
			# 查看与某个 PR 关联的 agent 任务详情
			$ gh agent-task view 123

			# 查看某个特定 agent 任务的详情
			$ gh agent-task view 12345abc-12345-12345-12345-12345abc
		`),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return requireOAuthToken(f)
		},
		// 这是运行此根命令所必需的。我们希望
		// 运行它以测试 PersistentPreRunE 的行为。
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// 注册子命令
	cmd.AddCommand(cmdList.NewCmdList(f, nil))
	cmd.AddCommand(cmdCreate.NewCmdCreate(f, nil))
	cmd.AddCommand(cmdView.NewCmdView(f, nil))

	return cmd
}

// requireOAuthToken 确保存在有效的 OAuth (device flow) token。
// agent-task 子命令通过 PersistentPreRunE 继承此检查。
func requireOAuthToken(f *cmdutil.Factory) error {
	cfg, err := f.Config()
	if err != nil {
		return err
	}

	authCfg := cfg.Authentication()
	host, _ := authCfg.DefaultHost()
	if host == "" {
		return errors.New("未配置默认主机，请运行 'gh auth login'")
	}

	if auth.IsEnterprise(host) {
		return errors.New("此主机不支持 agent 任务")
	}

	token, source := authCfg.ActiveToken(host)

	// 来源为 "oauth_token" 和 "keyring" 的 token 很可能是
	// 通过我们的 device flow 签发的。
	tokenSourceIsDeviceFlow := source == "oauth_token" || source == "keyring"
	// 带有 "gho_" 前缀的 token 是 OAuth token。
	//
	// TODO: 这里直接匹配了 token 前缀。可以改为查询
	// gh.AuthConfig.ActiveTokenType。
	tokenIsOAuth := strings.HasPrefix(token, "gho_")

	// 如果 token 不是来自 device flow 来源或不是 OAuth token，则拒绝
	if !tokenSourceIsDeviceFlow || !tokenIsOAuth {
		return fmt.Errorf("此命令需要 OAuth token。请重新认证: gh auth login")
	}
	return nil
}
