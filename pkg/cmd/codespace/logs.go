package codespace

import (
	"context"
	"fmt"

	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/portforwarder"
	"github.com/cli/cli/v2/internal/codespaces/rpc"
	"github.com/spf13/cobra"
)

func newLogsCmd(app *App) *cobra.Command {
	var (
		selector *CodespaceSelector
		follow   bool
	)

	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "访问 codespace 日志",
		Args:  noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Logs(cmd.Context(), selector, follow)
		},
	}

	selector = AddCodespaceSelector(logsCmd, app.apiClient)

	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "跟踪并持续输出日志")

	return logsCmd
}

func (a *App) Logs(ctx context.Context, selector *CodespaceSelector, follow bool) (err error) {
	// Ensure all child tasks (port forwarding, remote exec) terminate before return.
	// 确保所有子任务 (端口转发、远程执行) 在返回前结束。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
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

	// Ensure local port is listening before client (getPostCreateOutput) connects.
	// 在客户端 (getPostCreateOutput) 连接之前，确保本地端口已开始监听。
	listen, localPort, err := codespaces.ListenTCP(0, false)
	if err != nil {
		return err
	}
	defer listen.Close()

	remoteSSHServerPort, sshUser := 0, ""
	err = a.RunWithProgress("正在获取 SSH 详情", func() (err error) {
		invoker, err := rpc.CreateInvoker(ctx, fwd)
		if err != nil {
			return
		}
		defer safeClose(invoker, &err)

		remoteSSHServerPort, sshUser, err = invoker.StartSSHServer(ctx)
		return
	})
	if err != nil {
		return fmt.Errorf("获取 SSH 服务器详情时出错: %w", err)
	}

	cmdType := "cat"
	if follow {
		cmdType = "tail -f"
	}

	dst := fmt.Sprintf("%s@localhost", sshUser)
	cmd, err := codespaces.NewRemoteCommand(
		ctx, localPort, dst, fmt.Sprintf("%s /workspaces/.codespaces/.persistedshare/creation.log", cmdType),
	)
	if err != nil {
		return fmt.Errorf("remote command: %w", err)
	}

	// The log file is external content. On a terminal, route it through
	// ContentOut to neutralize escape sequences (assigning a writer other than an
	// *os.File also forces the remote output through this process so the sanitizer
	// runs). When piped, pass the bytes through unchanged: there is no live
	// terminal to manipulate, and a follow stream cannot be buffered to fail closed.
	// 日志文件属于外部内容。在终端上，通过 ContentOut 输出以中和转义序列
	// (赋一个非 *os.File 的写入器也会强制远程输出经过本进程，从而执行净化器)。
	// 通过管道时按原样传递字节: 没有可被操纵的实时终端，
	// 且 follow 流无法缓冲后整体判定失败。
	if !a.io.IsStdoutTTY() {
		a.io.SetContentSanitization(false)
	}
	cmd.Stdout = a.io.ContentOut

	tunnelClosed := make(chan error, 1)
	go func() {
		opts := portforwarder.ForwardPortOpts{
			Port:     remoteSSHServerPort,
			Internal: true,
		}
		tunnelClosed <- fwd.ForwardPortToListener(ctx, opts, listen)
	}()

	cmdDone := make(chan error, 1)
	go func() {
		cmdDone <- cmd.Run()
	}()

	select {
	case err := <-tunnelClosed:
		return fmt.Errorf("连接已关闭: %w", err)
	case err := <-cmdDone:
		if err != nil {
			return fmt.Errorf("获取日志时出错: %w", err)
		}

		return nil // success
		// 成功
	}
}
