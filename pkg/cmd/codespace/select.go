package codespace

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type selectOptions struct {
	filePath string
	selector *CodespaceSelector
}

func newSelectCmd(app *App) *cobra.Command {
	var (
		opts selectOptions
	)

	selectCmd := &cobra.Command{
		Use:    "select",
		Short:  "选择一个 Codespace",
		Hidden: true,
		Args:   noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Select(cmd.Context(), opts)
		},
	}

	opts.selector = AddCodespaceSelector(selectCmd, app.apiClient)
	selectCmd.Flags().StringVarP(&opts.filePath, "file", "f", "", "输出文件路径")
	return selectCmd
}

// 隐藏的 codespace `select` 命令允许外部 GH CLI 扩展复用现有的
// codespace 选择对话框。默认将选中的 codespace 名称输出到
// `stdout`。传入 `--file`(`-f`) 标志和文件路径，可将选中的
// codespace 名称输出到文件而非标准输出。
//
// ## 示例
//
// 输出到 `stdout`:
//
// ```shell
//
//	gh codespace select
//
// ```
//
// 输出到文件 (`into-a-file`):
//
// ```shell
//
//	gh codespace select --file /tmp/selected_codespace.txt
//
// ```
func (a *App) Select(ctx context.Context, opts selectOptions) (err error) {
	codespace, err := opts.selector.Select(ctx)
	if err != nil {
		return err
	}

	if opts.filePath != "" {
		f, err := os.Create(opts.filePath)
		if err != nil {
			return fmt.Errorf("创建输出文件失败: %w", err)
		}

		defer safeClose(f, &err)

		_, err = f.WriteString(codespace.Name)
		if err != nil {
			return fmt.Errorf("将 codespace 名称写入输出文件失败: %w", err)
		}

		return nil
	}

	fmt.Fprintln(a.io.Out, codespace.Name)

	return nil
}
