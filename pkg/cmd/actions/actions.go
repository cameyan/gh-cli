package actions

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

func NewCmdActions(f *cmdutil.Factory) *cobra.Command {
	cs := f.IOStreams.ColorScheme()

	cmd := &cobra.Command{
		Use:    "actions",
		Short:  "了解如何使用 GitHub Actions",
		Long:   actionsExplainer(cs),
		Hidden: true,
	}

	cmdutil.DisableAuthCheck(cmd)

	return cmd
}

func actionsExplainer(cs *iostreams.ColorScheme) string {
	header := cs.Bold("欢迎来到命令行中的 GitHub Actions")
	runHeader := cs.Bold("与工作流运行进行交互")
	workflowHeader := cs.Bold("与工作流文件交互")
	cacheHeader := cs.Bold("与 GitHub Actions 缓存进行交互")

	return heredoc.Docf(`
			%[2]s

			GitHub CLI 与 GitHub Actions 集成，可帮助您管理运行和工作流。

			%[3]s
			gh run list:      列出最近的工作流运行
			gh run view:      查看工作流运行或其某个作业的详细信息
			gh run watch:     在工作流运行执行时进行监控
			gh run rerun:     重新运行失败的工作流运行
			gh run download:  下载运行生成的工件文件

			如需更多帮助，请运行 %[1]sgh help run <subcommand>%[1]s

			%[4]s
			gh workflow list:     列出仓库中的工作流文件
			gh workflow view:     查看工作流文件的详细信息
			gh workflow enable:   启动工作流文件
			gh workflow disable:  禁用工作流文件
			gh workflow run:      为工作流文件触发一次 workflow_dispatch 运行

			如需更多帮助，请运行 %[1]sgh help workflow <subcommand>%[1]s

			%[5]s
			gh cache list:    列出存储在 GitHub Actions 中的仓库所有缓存
			gh cache delete:  删除仓库在 GitHub Actions 中的一个或所有已保存缓存

			如需更多帮助，请运行 %[1]sgh help cache <subcommand>%[1]s

		`, "`", header, runHeader, workflowHeader, cacheHeader)
}
