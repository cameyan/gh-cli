package codespace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type deleteOptions struct {
	deleteAll     bool
	skipConfirm   bool
	codespaceName string
	repoFilter    string
	keepDays      uint16
	orgName       string
	userName      string
	repoOwner     string

	isInteractive bool
	now           func() time.Time
	prompter      prompter
}

//go:generate moq -fmt goimports -rm -skip-ensure -out mock_prompter.go . prompter
type prompter interface {
	Confirm(message string) (bool, error)
}

func newDeleteCmd(app *App) *cobra.Command {
	opts := deleteOptions{
		isInteractive: hasTTY,
		now:           time.Now,
		prompter:      &surveyPrompter{},
	}

	var selector *CodespaceSelector

	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "删除 codespace",
		Long: heredoc.Doc(`
			根据选择条件删除 codespace。

			可以删除已认证用户的所有 codespace，也可以删除特定仓库的
			codespace。此外，还可以只删除超过 N 天的 codespace。

			组织管理员可以删除由该组织付费的任何 codespace。
		`),
		Args: noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: ideally we would use the selector directly, but the logic here is too intertwined with other flags to do so elegantly
			// After the admin subcommand is added (see https://github.com/cli/cli/pull/6944#issuecomment-1419553639) we can revisit this.
			// TODO: 理想情况下应直接使用选择器，但这里的逻辑与其他标志交织过深，难以优雅地这样做
			// 待 admin 子命令添加后 (见 https://github.com/cli/cli/pull/6944#issuecomment-1419553639) 再重新审视。
			opts.codespaceName = selector.codespaceName
			opts.repoFilter = selector.repoName
			opts.repoOwner = selector.repoOwner

			if opts.deleteAll && opts.repoFilter != "" {
				return cmdutil.FlagErrorf("`--all` 和 `--repo` 不能同时使用")
			}

			if opts.orgName != "" && opts.codespaceName != "" && opts.userName == "" {
				return cmdutil.FlagErrorf("`--org` 与 `--codespace` 同时使用时需要 `--user`")
			}
			return app.Delete(cmd.Context(), opts)
		},
	}

	selector = AddCodespaceSelector(deleteCmd, app.apiClient)
	if err := addDeprecatedRepoShorthand(deleteCmd, &selector.repoName); err != nil {
		fmt.Fprintf(app.io.ErrOut, "%v\n", err)
	}

	deleteCmd.Flags().BoolVar(&opts.deleteAll, "all", false, "删除所有 codespace")
	deleteCmd.Flags().BoolVarP(&opts.skipConfirm, "force", "f", false, "对包含未保存更改的 codespace 跳过确认")
	deleteCmd.Flags().Uint16Var(&opts.keepDays, "days", 0, "删除超过 `N` 天的 codespace")
	deleteCmd.Flags().StringVarP(&opts.orgName, "org", "o", "", "组织的 `login` 句柄 (仅限管理员)")
	deleteCmd.Flags().StringVarP(&opts.userName, "user", "u", "", "要删除其 codespace 的 `username` (与 --org 配合使用)")

	return deleteCmd
}

func (a *App) Delete(ctx context.Context, opts deleteOptions) (err error) {
	var codespaces []*api.Codespace
	nameFilter := opts.codespaceName
	if nameFilter == "" {
		err = a.RunWithProgress("正在获取 codespace", func() (fetchErr error) {
			userName := opts.userName
			if userName == "" && opts.orgName != "" {
				currentUser, fetchErr := a.apiClient.GetUser(ctx)
				if fetchErr != nil {
					return fetchErr
				}
				userName = currentUser.Login
			}
			codespaces, fetchErr = a.apiClient.ListCodespaces(ctx, api.ListCodespacesOptions{OrgName: opts.orgName, UserName: userName})
			if opts.repoOwner != "" {
				codespaces = filterCodespacesByRepoOwner(codespaces, opts.repoOwner)
			}
			return
		})
		if err != nil {
			return fmt.Errorf("获取 codespace 时出错: %w", err)
		}

		if !opts.deleteAll && opts.repoFilter == "" {
			includeUsername := opts.orgName != ""
			c, err := chooseCodespaceFromList(ctx, codespaces, includeUsername, false)
			if err != nil {
				return fmt.Errorf("选择 codespace 时出错: %w", err)
			}
			nameFilter = c.Name
		}
	} else {
		var codespace *api.Codespace
		err := a.RunWithProgress("正在获取 codespace", func() (fetchErr error) {
			if opts.orgName == "" || opts.userName == "" {
				codespace, fetchErr = a.apiClient.GetCodespace(ctx, nameFilter, false)
			} else {
				codespace, fetchErr = a.apiClient.GetOrgMemberCodespace(ctx, opts.orgName, opts.userName, opts.codespaceName)
			}
			return
		})
		if err != nil {
			return fmt.Errorf("获取 codespace 信息时出错: %w", err)
		}

		codespaces = []*api.Codespace{codespace}
	}

	codespacesToDelete := make([]*api.Codespace, 0, len(codespaces))
	lastUpdatedCutoffTime := opts.now().AddDate(0, 0, -int(opts.keepDays))
	for _, c := range codespaces {
		if nameFilter != "" && c.Name != nameFilter {
			continue
		}
		if opts.repoFilter != "" && !strings.EqualFold(c.Repository.FullName, opts.repoFilter) {
			continue
		}

		if opts.keepDays > 0 {
			t, err := time.Parse(time.RFC3339, c.LastUsedAt)
			if err != nil {
				return fmt.Errorf("解析 last_used_at 时间戳 %q 时出错: %w", c.LastUsedAt, err)
			}
			if t.After(lastUpdatedCutoffTime) {
				continue
			}
		}
		if !opts.skipConfirm {
			confirmed, err := confirmDeletion(opts.prompter, c, opts.isInteractive)
			if err != nil {
				return fmt.Errorf("无法确认: %w", err)
			}
			if !confirmed {
				continue
			}
		}
		codespacesToDelete = append(codespacesToDelete, c)
	}

	if len(codespacesToDelete) == 0 {
		return errors.New("没有要删除的 codespace")
	}

	progressLabel := "正在删除 codespace"
	if len(codespacesToDelete) > 1 {
		progressLabel = "正在删除 codespace"
	}

	var deletedCodespaces uint32
	err = a.RunWithProgress(progressLabel, func() error {
		var g errgroup.Group
		for _, c := range codespacesToDelete {
			codespaceName := c.Name
			g.Go(func() error {
				if err := a.apiClient.DeleteCodespace(ctx, codespaceName, opts.orgName, opts.userName); err != nil {
					a.errLogger.Printf("删除 codespace %q 时出错: %v\n", codespaceName, err)
					return err
				}
				atomic.AddUint32(&deletedCodespaces, 1)
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return fmt.Errorf("%d 个 codespace 删除失败", len(codespacesToDelete)-int(deletedCodespaces))
		}
		return nil
	})

	if a.io.IsStdoutTTY() && deletedCodespaces > 0 {
		successMsg := fmt.Sprintf("成功删除 %d 个 codespace\n", deletedCodespaces)
		fmt.Fprint(a.io.ErrOut, successMsg)
	}

	return err
}

func confirmDeletion(p prompter, apiCodespace *api.Codespace, isInteractive bool) (bool, error) {
	cs := codespace{apiCodespace}
	if !cs.hasUnsavedChanges() {
		return true, nil
	}
	if !isInteractive {
		return false, fmt.Errorf("codespace %s 有未保存的更改 (使用 --force 强制执行)", cs.Name)
	}
	return p.Confirm(fmt.Sprintf("Codespace %s 有未保存的更改。确定删除吗？", cs.Name))
}

type surveyPrompter struct{}

func (p *surveyPrompter) Confirm(message string) (bool, error) {
	prompter := &Prompter{}
	var confirmed struct {
		Confirmed bool
	}
	q := []*survey.Question{
		{
			Name: "confirmed",
			Prompt: &survey.Confirm{
				Message: message,
			},
		},
	}
	if err := prompter.Ask(q, &confirmed); err != nil {
		return false, fmt.Errorf("提示输入失败: %w", err)
	}

	return confirmed.Confirmed, nil
}
