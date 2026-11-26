package codespace

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

type stopOptions struct {
	selector *CodespaceSelector
	orgName  string
	userName string
}

func newStopCmd(app *App) *cobra.Command {
	opts := &stopOptions{}

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "停止正在运行的 codespace",
		Args:  noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.orgName != "" && opts.selector.codespaceName != "" && opts.userName == "" {
				return cmdutil.FlagErrorf("`--org` 与 `--codespace` 同时使用时需要 `--user`")
			}
			return app.StopCodespace(cmd.Context(), opts)
		},
	}
	opts.selector = AddCodespaceSelector(stopCmd, app.apiClient)
	stopCmd.Flags().StringVarP(&opts.orgName, "org", "o", "", "组织的 `login` 句柄 (仅限管理员)")
	stopCmd.Flags().StringVarP(&opts.userName, "user", "u", "", "要停止其 codespace 的 `username` (与 --org 配合使用)")

	return stopCmd
}

func (a *App) StopCodespace(ctx context.Context, opts *stopOptions) error {
	var (
		codespaceName = opts.selector.codespaceName
		repoName      = opts.selector.repoName
		ownerName     = opts.userName
	)

	if codespaceName == "" {
		var codespaces []*api.Codespace
		err := a.RunWithProgress("正在获取 codespace", func() (err error) {
			codespaces, err = a.apiClient.ListCodespaces(ctx, api.ListCodespacesOptions{
				RepoName: repoName,
				OrgName:  opts.orgName,
				UserName: ownerName,
			})
			return
		})
		if err != nil {
			return fmt.Errorf("列出 codespace 失败: %w", err)
		}

		var runningCodespaces []*api.Codespace
		for _, c := range codespaces {
			cs := codespace{c}
			if cs.running() {
				runningCodespaces = append(runningCodespaces, c)
			}
		}
		if len(runningCodespaces) == 0 {
			return errors.New("没有正在运行的 codespace")
		}

		includeOwner := opts.orgName != ""
		skipPromptForSingleOption := repoName != ""
		codespace, err := chooseCodespaceFromList(ctx, runningCodespaces, includeOwner, skipPromptForSingleOption)
		if err != nil {
			return fmt.Errorf("选择 codespace 失败: %w", err)
		}
		codespaceName = codespace.Name
		ownerName = codespace.Owner.Login
	} else {
		var c *api.Codespace
		err := a.RunWithProgress("正在获取 codespace", func() (err error) {
			if opts.orgName == "" {
				c, err = a.apiClient.GetCodespace(ctx, codespaceName, false)
			} else {
				c, err = a.apiClient.GetOrgMemberCodespace(ctx, opts.orgName, ownerName, codespaceName)
			}
			return
		})
		if err != nil {
			return fmt.Errorf("获取 codespace 失败: %q: %w", codespaceName, err)
		}

		cs := codespace{c}
		if !cs.running() {
			return fmt.Errorf("codespace %q 未在运行", codespaceName)
		}
	}

	err := a.RunWithProgress("正在停止 codespace", func() (err error) {
		err = a.apiClient.StopCodespace(ctx, codespaceName, opts.orgName, ownerName)
		return
	})
	if err != nil {
		return fmt.Errorf("停止 codespace 失败: %w", err)
	}

	return nil
}
