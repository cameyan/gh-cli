package autolink

import (
	"github.com/MakeNowJust/heredoc"
	cmdCreate "github.com/cli/cli/v2/pkg/cmd/repo/autolink/create"
	cmdDelete "github.com/cli/cli/v2/pkg/cmd/repo/autolink/delete"
	cmdList "github.com/cli/cli/v2/pkg/cmd/repo/autolink/list"
	cmdView "github.com/cli/cli/v2/pkg/cmd/repo/autolink/view"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdAutolink(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autolink <command>",
		Short: "管理自动链接引用",
		Long: heredoc.Docf(`
			自动链接(autolink)将议题(issue)、拉取请求(pull request)、提交信息和发布描述链接到外部第三方服务。

			查看或管理自动链接需要 %[1]sadmin%[1]s 角色。

			更多信息请参见 <https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/configuring-autolinks-to-reference-external-resources>
		`, "`"),
	}
	cmdutil.EnableRepoOverride(cmd, f)

	cmd.AddCommand(cmdList.NewCmdList(f, nil))
	cmd.AddCommand(cmdCreate.NewCmdCreate(f, nil))
	cmd.AddCommand(cmdView.NewCmdView(f, nil))
	cmd.AddCommand(cmdDelete.NewCmdDelete(f, nil))

	return cmd
}
