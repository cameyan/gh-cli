package root

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ci"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/extensions"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// NewCmdOfficialExtensionStub 为尚未安装的官方扩展(extension)创建一个隐藏的
// stub 命令。被调用时，它会建议安装该扩展，并在交互式会话中提供立即安装的选项。
// 安装成功后，将使用原始参数分发给该扩展。
func NewCmdOfficialExtensionStub(io *iostreams.IOStreams, p prompter.Prompter, em extensions.ExtensionManager, ext *extensions.OfficialExtension) *cobra.Command {
	cmd := &cobra.Command{
		Use:     ext.Name,
		Short:   fmt.Sprintf("安装官方 %s 扩展", ext.Name),
		Hidden:  true,
		GroupID: "extension",
		// 接受用户可能传入的任何参数/flag，以免在到达 RunE 之前
		// 出现 cobra 校验错误。
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return officialExtensionStubRun(io, p, em, ext)
		},
	}

	cmdutil.DisableAuthCheck(cmd)

	return cmd
}

func officialExtensionStubRun(io *iostreams.IOStreams, p prompter.Prompter, em extensions.ExtensionManager, ext *extensions.OfficialExtension) error {
	stderr := io.ErrOut

	// 在 CI 中跳过提示，以免 agent 与 CI 运行器因 Y/n 确认而阻塞。
	if !ci.IsCI() {
		if io.CanPrompt() {
			prompt := heredoc.Docf(`
				%[1]s 可作为官方扩展安装，
				要现在安装它吗?
			`, fmt.Sprintf("gh %s", ext.Name))
			confirmed, err := p.Confirm(prompt, true)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		} else {
			fmt.Fprint(stderr, heredoc.Docf(`
				%[1]s 可作为官方扩展安装，
				安装命令:
				  gh extension install %[2]s/%[3]s
			`, fmt.Sprintf("gh %s", ext.Name), ext.Owner, ext.Repo))
			return cmdutil.SilentError
		}
	}

	repo := ext.Repository()
	io.StartProgressIndicatorWithLabel(fmt.Sprintf("正在安装 %s/%s...", ext.Owner, ext.Repo))
	installErr := em.Install(repo, "")
	io.StopProgressIndicator()
	if installErr != nil {
		return fmt.Errorf("安装扩展失败: %w", installErr)
	}

	fmt.Fprintf(stderr, "已成功安装 %s/%s\n", ext.Owner, ext.Repo)
	return nil
}
