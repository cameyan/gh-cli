package issue

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	cmdArtifact "github.com/cli/cli/v2/pkg/cmd/issue/artifact"
	cmdClose "github.com/cli/cli/v2/pkg/cmd/issue/close"
	cmdComment "github.com/cli/cli/v2/pkg/cmd/issue/comment"
	cmdCreate "github.com/cli/cli/v2/pkg/cmd/issue/create"
	cmdDelete "github.com/cli/cli/v2/pkg/cmd/issue/delete"
	cmdDevelop "github.com/cli/cli/v2/pkg/cmd/issue/develop"
	cmdEdit "github.com/cli/cli/v2/pkg/cmd/issue/edit"
	cmdList "github.com/cli/cli/v2/pkg/cmd/issue/list"
	cmdLock "github.com/cli/cli/v2/pkg/cmd/issue/lock"
	cmdPin "github.com/cli/cli/v2/pkg/cmd/issue/pin"
	cmdReopen "github.com/cli/cli/v2/pkg/cmd/issue/reopen"
	cmdStatus "github.com/cli/cli/v2/pkg/cmd/issue/status"
	cmdTransfer "github.com/cli/cli/v2/pkg/cmd/issue/transfer"
	cmdUnpin "github.com/cli/cli/v2/pkg/cmd/issue/unpin"
	cmdView "github.com/cli/cli/v2/pkg/cmd/issue/view"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdIssue(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue <command>",
		Short: "管理 issue",
		Long:  `使用 GitHub issue 进行工作`,
		Example: heredoc.Doc(`
			$ gh issue list
			$ gh issue create --label bug
			$ gh issue view 123 --web
		`),
		Annotations: map[string]string{
			"help:arguments": heredoc.Doc(`
				可以通过以下任意格式在参数中提供 issue:
				- 通过编号, 例如 "123"; 或
				- 通过 URL, 例如 "https://github.com/OWNER/REPO/issues/123".
			`),
		},
		GroupID: "core",
	}

	cmdutil.EnableRepoOverride(cmd, f)

	cmdutil.AddGroup(cmd, "通用命令",
		cmdList.NewCmdList(f, nil),
		cmdCreate.NewCmdCreate(f, telemetry, nil),
		cmdStatus.NewCmdStatus(f, nil),
	)

	cmdutil.AddGroup(cmd, "定向命令",
		cmdView.NewCmdView(f, nil),
		cmdComment.NewCmdComment(f, telemetry, nil),
		cmdClose.NewCmdClose(f, nil),
		cmdReopen.NewCmdReopen(f, nil),
		cmdEdit.NewCmdEdit(f, telemetry, nil),
		cmdDevelop.NewCmdDevelop(f, nil),
		cmdLock.NewCmdLock(f, cmd.Name(), nil),
		cmdLock.NewCmdUnlock(f, cmd.Name(), nil),
		cmdPin.NewCmdPin(f, nil),
		cmdUnpin.NewCmdUnpin(f, nil),
		cmdTransfer.NewCmdTransfer(f, nil),
		cmdDelete.NewCmdDelete(f, nil),
		cmdArtifact.NewCmdArtifact(f, telemetry),
	)

	return cmd
}
