package codespace

import (
	"context"
	"fmt"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

type listOptions struct {
	limit    int
	repo     string
	orgName  string
	userName string
	useWeb   bool
}

func newListCmd(app *App) *cobra.Command {
	opts := &listOptions{}
	var exporter cmdutil.Exporter

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "列出 codespace",
		Long: heredoc.Doc(`
			列出已认证用户的 codespace。

			此外，组织管理员可以列出由该组织付费的所有 codespace。
		`),
		Aliases: []string{"ls"},
		Args:    noArgsConstraint,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.repo != "" {
				if err := validateNWO(opts.repo); err != nil {
					return cmdutil.FlagErrorf("--repo 的值无效: %v", err)
				}
			}

			if err := cmdutil.MutuallyExclusive(
				"`--org` 或 `--user` 不能与 `--repo` 同时使用",
				opts.repo != "",
				opts.orgName != "" || opts.userName != "",
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"`--web` 不能与 `--org` 或 `--user` 同时使用，请改为配合 `--repo` 使用",
				opts.useWeb,
				opts.orgName != "" || opts.userName != "",
			); err != nil {
				return err
			}

			if opts.limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %v", opts.limit)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.List(cmd.Context(), opts, exporter)
		},
	}

	listCmd.Flags().IntVarP(&opts.limit, "limit", "L", 30, "要列出的 codespace 数量上限")
	listCmd.Flags().StringVarP(&opts.repo, "repo", "R", "", "带所有者的仓库名称: user/repo")
	if err := addDeprecatedRepoShorthand(listCmd, &opts.repo); err != nil {
		fmt.Fprintf(app.io.ErrOut, "%v\n", err)
	}

	listCmd.Flags().StringVarP(&opts.orgName, "org", "o", "", "要列出其 codespace 的组织的 `login` 句柄 (仅限管理员)")
	listCmd.Flags().StringVarP(&opts.userName, "user", "u", "", "要列出其 codespace 的 `username` (与 --org 配合使用)")
	cmdutil.AddJSONFlags(listCmd, &exporter, api.ListCodespaceFields)

	listCmd.Flags().BoolVarP(&opts.useWeb, "web", "w", false, "在网页浏览器中列出 codespace，不能与 --user 或 --org 同时使用")

	return listCmd
}

func (a *App) List(ctx context.Context, opts *listOptions, exporter cmdutil.Exporter) error {
	if opts.useWeb && opts.repo == "" {
		return a.browser.Browse(fmt.Sprintf("%s/codespaces", a.apiClient.ServerURL()))
	}

	var codespaces []*api.Codespace
	err := a.RunWithProgress("正在获取 codespace", func() (err error) {
		codespaces, err = a.apiClient.ListCodespaces(ctx, api.ListCodespacesOptions{Limit: opts.limit, RepoName: opts.repo, OrgName: opts.orgName, UserName: opts.userName})
		return
	})
	if err != nil {
		return fmt.Errorf("获取 codespace 时出错: %w", err)
	}

	hasNonProdVSCSTarget := false
	for _, apiCodespace := range codespaces {
		if apiCodespace.VSCSTarget != "" && apiCodespace.VSCSTarget != api.VSCSTargetProduction {
			hasNonProdVSCSTarget = true
			break
		}
	}

	if err := a.io.StartPager(); err != nil {
		a.errLogger.Printf("启动分页器出错: %v", err)
	}
	defer a.io.StopPager()

	if exporter != nil {
		return exporter.Write(a.io, codespaces)
	}

	if len(codespaces) == 0 {
		return cmdutil.NewNoResultsError("未找到 codespace")
	}

	if opts.useWeb && codespaces[0].Repository.ID > 0 {
		return a.browser.Browse(fmt.Sprintf("%s/codespaces?repository_id=%d", a.apiClient.ServerURL(), codespaces[0].Repository.ID))
	}

	headers := []string{
		"NAME",
		"DISPLAY NAME",
	}
	if opts.orgName != "" {
		headers = append(headers, "OWNER")
	}
	headers = append(headers,
		"REPOSITORY",
		"BRANCH",
		"STATE",
		"CREATED AT",
	)
	if hasNonProdVSCSTarget {
		headers = append(headers, "VSCS TARGET")
	}
	tp := tableprinter.New(a.io, tableprinter.WithHeader(headers...))

	cs := a.io.ColorScheme()
	for _, apiCodespace := range codespaces {
		c := codespace{apiCodespace}

		var stateColor func(string) string
		switch c.State {
		case api.CodespaceStateStarting:
			stateColor = cs.Yellow
		case api.CodespaceStateAvailable:
			stateColor = cs.Green
		}

		formattedName := formatNameForVSCSTarget(c.Name, c.VSCSTarget)

		var nameColor func(string) string
		switch c.PendingOperation {
		case false:
			nameColor = cs.Yellow
		case true:
			nameColor = cs.Muted
		}

		tp.AddField(formattedName, tableprinter.WithColor(nameColor))
		tp.AddField(c.DisplayName)
		if opts.orgName != "" {
			tp.AddField(c.Owner.Login)
		}
		tp.AddField(c.Repository.FullName)
		tp.AddField(c.branchWithGitStatus(), tableprinter.WithColor(cs.Cyan))
		if c.PendingOperation {
			tp.AddField(c.PendingOperationDisabledReason, tableprinter.WithColor(nameColor))
		} else {
			tp.AddField(c.State, tableprinter.WithColor(stateColor))
		}

		ct, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			return fmt.Errorf("解析日期 %q 时出错: %w", c.CreatedAt, err)
		}
		tp.AddTimeField(time.Now(), ct, cs.Muted)

		if hasNonProdVSCSTarget {
			tp.AddField(c.VSCSTarget)
		}

		tp.EndRow()
	}

	return tp.Render()
}

func formatNameForVSCSTarget(name, vscsTarget string) string {
	if vscsTarget == api.VSCSTargetDevelopment || vscsTarget == api.VSCSTargetLocal {
		return fmt.Sprintf("%s 🚧", name)
	}

	if vscsTarget == api.VSCSTargetPPE {
		return fmt.Sprintf("%s ✨", name)
	}

	return name
}
