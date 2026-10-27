package codespace

import (
	"context"
	"fmt"
	"os"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

const (
	minutesInDay = 1440
)

type viewOptions struct {
	selector *CodespaceSelector
	exporter cmdutil.Exporter
}

func newViewCmd(app *App) *cobra.Command {
	opts := &viewOptions{}

	viewCmd := &cobra.Command{
		Use:   "view",
		Short: "查看 codespace 详情",
		Example: heredoc.Doc(`
			# 从你拥有的所有 codespace 列表中选择一个
			$ gh cs view

			# 查看指定 codespace 的详情
			$ gh cs view -c codespace-name-12345

			# 查看 codespace 所有可用字段的列表
			$ gh cs view --json

			# 查看 codespace 的指定字段
			$ gh cs view --json displayName,machineDisplayName,state
		`),
		Args: noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.ViewCodespace(cmd.Context(), opts)
		},
	}
	opts.selector = AddCodespaceSelector(viewCmd, app.apiClient)
	cmdutil.AddJSONFlags(viewCmd, &opts.exporter, api.ViewCodespaceFields)

	return viewCmd
}

func (a *App) ViewCodespace(ctx context.Context, opts *viewOptions) error {
	// 如果当前在 codespace 内且未提供 codespace 名称，则显示当前连接的 codespace 详情
	if (os.Getenv("CODESPACES") == "true") && opts.selector.codespaceName == "" {
		codespaceName := os.Getenv("CODESPACE_NAME")
		opts.selector.codespaceName = codespaceName
	}

	selectedCodespace, err := opts.selector.Select(ctx)
	if err != nil {
		return err
	}

	if err := a.io.StartPager(); err != nil {
		a.errLogger.Printf("启动分页器出错: %v", err)
	}
	defer a.io.StopPager()

	if opts.exporter != nil {
		return opts.exporter.Write(a.io, selectedCodespace)
	}

	//nolint:staticcheck // SA1019: 显示 NAME|VALUE 表头对表格没有帮助。
	tp := tableprinter.New(a.io, tableprinter.NoHeader)
	c := codespace{selectedCodespace}
	formattedName := formatNameForVSCSTarget(c.Name, c.VSCSTarget)

	// 创建要在表格中显示的字段及其值的数组
	fields := []struct {
		name  string
		value string
	}{
		{"名称 (Name)", formattedName},
		{"状态 (State)", c.State},
		{"仓库 (Repository)", c.Repository.FullName},
		{"Git 状态 (Git Status)", formatGitStatus(c)},
		{"Devcontainer 路径 (Devcontainer Path)", c.DevContainerPath},
		{"机器显示名称 (Machine Display Name)", c.Machine.DisplayName},
		{"闲置超时 (Idle Timeout)", fmt.Sprintf("%d 分钟", c.IdleTimeoutMinutes)},
		{"创建时间 (Created At)", c.CreatedAt},
		{"保留期 (Retention Period)", formatRetentionPeriodDays(c)},
	}

	for _, field := range fields {
		// 输出到 TTY 时，值为空的字段不显示
		if !a.io.IsStdoutTTY() || field.value != "" {
			tp.AddField(field.name)
			tp.AddField(field.value)
			tp.EndRow()
		}
	}

	err = tp.Render()
	if err != nil {
		return err
	}

	return nil
}

func formatGitStatus(codespace codespace) string {
	branchWithGitStatus := codespace.branchWithGitStatus()

	// 按正确复数格式化领先/落后提交数
	commitsAhead := text.Pluralize(codespace.GitStatus.Ahead, "commit")
	commitsBehind := text.Pluralize(codespace.GitStatus.Behind, "commit")

	return fmt.Sprintf("%s - 领先 %s，落后 %s", branchWithGitStatus, commitsAhead, commitsBehind)
}

func formatRetentionPeriodDays(codespace codespace) string {
	days := codespace.RetentionPeriodMinutes / minutesInDay
	// 保留期为 0 天时不显示
	if days == 0 {
		return ""
	} else if days == 1 {
		return "1 天"
	}

	return fmt.Sprintf("%d 天", days)
}
