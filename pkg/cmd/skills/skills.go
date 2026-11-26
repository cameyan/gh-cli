package skills

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/pkg/cmd/skills/install"
	skilllist "github.com/cli/cli/v2/pkg/cmd/skills/list"
	"github.com/cli/cli/v2/pkg/cmd/skills/preview"
	"github.com/cli/cli/v2/pkg/cmd/skills/publish"
	"github.com/cli/cli/v2/pkg/cmd/skills/search"
	"github.com/cli/cli/v2/pkg/cmd/skills/update"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

// NewCmdSkills 返回顶层 "skill" 命令。
func NewCmdSkills(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill <command>",
		Short: "安装和管理 agent 技能(预览版)",
		Long: heredoc.Doc(`
			从 GitHub 仓库安装和管理 agent 技能。

			在 GitHub CLI 中使用 agent 技能的功能处于预览阶段，可能随时变更，恕不另行通知。
		`),
		Aliases: []string{"skills"},
		GroupID: "core",
		Example: heredoc.Doc(`
			# 搜索技能
			$ gh skill search terraform

			# 安装技能
			$ gh skill install github/awesome-copilot documentation-writer

			# 列出已安装的技能
			$ gh skill list

			# 安装前预览技能
			$ gh skill preview github/awesome-copilot documentation-writer

			# 更新所有已安装的技能
			$ gh skill update --all

			# 校验待发布的技能
			$ gh skill publish --dry-run
		`),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			telemetry.SetSampleRate(ghtelemetry.SAMPLE_ALL)
			return nil
		},
	}

	cmd.AddCommand(install.NewCmdInstall(f, telemetry, nil))
	cmd.AddCommand(skilllist.NewCmdList(f, telemetry, nil))
	cmd.AddCommand(preview.NewCmdPreview(f, telemetry, nil))
	cmd.AddCommand(publish.NewCmdPublish(f, nil))
	cmd.AddCommand(search.NewCmdSearch(f, telemetry, nil))
	cmd.AddCommand(update.NewCmdUpdate(f, nil))

	return cmd
}
