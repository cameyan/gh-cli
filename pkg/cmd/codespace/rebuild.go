package codespace

import (
	"context"
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/codespaces/portforwarder"
	"github.com/cli/cli/v2/internal/codespaces/rpc"
	"github.com/spf13/cobra"
)

func newRebuildCmd(app *App) *cobra.Command {
	var (
		selector    *CodespaceSelector
		fullRebuild bool
	)

	rebuildCmd := &cobra.Command{
		Use:   "rebuild",
		Short: "重建 codespace",
		Long: heredoc.Doc(`
			重建会重新创建你的 codespace。

			你的代码和当前的所有更改都会保留。codespace 将使用你工作目录中的
			dev container 重建。完整重建还会移除缓存的 Docker 镜像。
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Rebuild(cmd.Context(), selector, fullRebuild)
		},
	}

	selector = AddCodespaceSelector(rebuildCmd, app.apiClient)

	rebuildCmd.Flags().BoolVar(&fullRebuild, "full", false, "执行完整重建")

	return rebuildCmd
}

func (a *App) Rebuild(ctx context.Context, selector *CodespaceSelector, full bool) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
	}

	// There's no need to rebuild again because users can't modify their codespace while it rebuilds
	// 无需再次重建，因为重建期间用户无法修改自己的 codespace
	if codespace.State == api.CodespaceStateRebuilding {
		fmt.Fprintf(a.io.Out, "%s 已在重建中\n", codespace.Name)
		return nil
	}

	codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, codespace)
	if err != nil {
		return fmt.Errorf("连接 codespace 时出错: %w", err)
	}

	fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
	if err != nil {
		return fmt.Errorf("创建端口转发器失败: %w", err)
	}
	defer safeClose(fwd, &err)

	invoker, err := rpc.CreateInvoker(ctx, fwd)
	if err != nil {
		return err
	}
	defer safeClose(invoker, &err)

	err = invoker.RebuildContainer(ctx, full)
	if err != nil {
		return fmt.Errorf("通过会话重建 codespace 失败: %w", err)
	}

	fmt.Fprintf(a.io.Out, "%s 正在重建\n", codespace.Name)
	return nil
}
