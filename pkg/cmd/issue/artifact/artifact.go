// Package artifact holds gh issue artifact, the command group for the
// Markdown documents, HTML documents and links attached to an issue.
package artifact

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	cmdCreate "github.com/cli/cli/v2/pkg/cmd/issue/artifact/create"
	cmdDelete "github.com/cli/cli/v2/pkg/cmd/issue/artifact/delete"
	cmdDownload "github.com/cli/cli/v2/pkg/cmd/issue/artifact/download"
	cmdEdit "github.com/cli/cli/v2/pkg/cmd/issue/artifact/edit"
	cmdList "github.com/cli/cli/v2/pkg/cmd/issue/artifact/list"
	cmdView "github.com/cli/cli/v2/pkg/cmd/issue/artifact/view"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

// NewCmdArtifact returns the gh issue artifact command group, which is in
// preview. It inherits -R from gh issue. telemetry records --attach use, as
// in the other commands that take it.
func NewCmdArtifact(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact <command>",
		Short: "处理 issue 产物 (预览版)",
		Long: heredoc.Docf(`
			在 GitHub CLI 中处理 issue 产物目前处于预览阶段，可能会在没有通知的情况下发生变化。

			issue 产物是附加到 issue 上的 Markdown 文档、HTML 文档和链接。

			它们不是 GitHub Actions 产物；要下载这些产物，请使用 %[1]sgh run download%[1]s。
		`, "`"),
	}

	cmd.AddCommand(cmdList.NewCmdList(f, nil))
	cmd.AddCommand(cmdView.NewCmdView(f, nil))
	cmd.AddCommand(cmdCreate.NewCmdCreate(f, telemetry, nil))
	cmd.AddCommand(cmdEdit.NewCmdEdit(f, telemetry, nil))
	cmd.AddCommand(cmdDelete.NewCmdDelete(f, nil))
	cmd.AddCommand(cmdDownload.NewCmdDownload(f, nil))

	return cmd
}
