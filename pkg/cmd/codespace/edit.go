package codespace

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

type editOptions struct {
	selector    *CodespaceSelector
	displayName string
	machine     string
}

func newEditCmd(app *App) *cobra.Command {
	opts := editOptions{}

	editCmd := &cobra.Command{
		Use:   "edit",
		Short: "编辑 codespace",
		Args:  noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.displayName == "" && opts.machine == "" {
				return cmdutil.FlagErrorf("必须提供 `--display-name` 或 `--machine`")
			}

			return app.Edit(cmd.Context(), opts)
		},
	}

	opts.selector = AddCodespaceSelector(editCmd, app.apiClient)
	editCmd.Flags().StringVarP(&opts.displayName, "display-name", "d", "", "设置显示名称")
	editCmd.Flags().StringVar(&opts.displayName, "displayName", "", "显示名称")
	if err := editCmd.Flags().MarkDeprecated("displayName", "请改用 `--display-name`"); err != nil {
		fmt.Fprintf(app.io.ErrOut, "将标志标记为已弃用时出错: %v\n", err)
	}
	editCmd.Flags().StringVarP(&opts.machine, "machine", "m", "", "设置虚拟机的硬件规格")

	return editCmd
}

// 编辑 codespace
func (a *App) Edit(ctx context.Context, opts editOptions) error {
	codespaceName, err := opts.selector.SelectName(ctx)
	if err != nil {
		// TODO: is there a cleaner way to do this?
		// TODO: 有没有更简洁的写法？
		if errors.Is(err, errNoCodespaces) || errors.Is(err, errNoFilteredCodespaces) {
			return err
		}
		return fmt.Errorf("选择 codespace 时出错: %w", err)
	}

	err = a.RunWithProgress("正在编辑 codespace", func() (err error) {
		_, err = a.apiClient.EditCodespace(ctx, codespaceName, &api.EditCodespaceParams{
			DisplayName: opts.displayName,
			Machine:     opts.machine,
		})
		return
	})
	if err != nil {
		return fmt.Errorf("编辑 codespace 时出错: %w", err)
	}

	return nil
}
