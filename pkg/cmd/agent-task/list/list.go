package list

import (
	"context"
	"fmt"
	"time"

	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/capi"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const defaultLimit = 30

// ListOptions 是 list 命令的选项
type ListOptions struct {
	IO         *iostreams.IOStreams
	Limit      int
	CapiClient func() (capi.CapiClient, error)
	Web        bool
	Browser    browser.Browser
	Exporter   cmdutil.Exporter
}

// NewCmdList 创建 list 命令
func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		CapiClient: shared.CapiClientFunc(f),
		Limit:      defaultLimit,
		Browser:    f.Browser,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出 agent 任务(预览版)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效的限制值: %v", opts.Limit)
			}
			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", defaultLimit, "最大可获取的 agent 任务数量")
	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开 agent 任务")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, capi.SessionFields)

	return cmd
}

func listRun(opts *ListOptions) error {
	if opts.Web {
		webURL := capi.AgentsHomeURL
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(webURL))
		}
		return opts.Browser.Browse(webURL)
	}

	if opts.Limit <= 0 {
		opts.Limit = defaultLimit
	}

	capiClient, err := opts.CapiClient()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicatorWithLabel("正在获取 agent 任务...")
	defer opts.IO.StopProgressIndicator()
	var sessions []*capi.Session
	ctx := context.Background()

	sessions, err = capiClient.ListLatestSessionsForViewer(ctx, opts.Limit)
	if err != nil {
		return err
	}

	opts.IO.StopProgressIndicator()

	if len(sessions) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("未找到 agent 任务")
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, sessions)
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	if opts.IO.IsStdoutTTY() {
		count := len(sessions)
		header := fmt.Sprintf("正在显示 %d 个 session", count)
		fmt.Fprintf(opts.IO.Out, "%s\n\n", header)
	}

	cs := opts.IO.ColorScheme()
	tp := tableprinter.New(opts.IO, tableprinter.WithHeader("会话名称", "PR", "仓库", "会话状态", "创建时间"))
	for _, s := range sessions {
		if s.ResourceType != "pull" || s.PullRequest == nil || s.PullRequest.Repository == nil {
			// 跳过这些可能出现的 session（暂时）。
			continue
		}

		pr := fmt.Sprintf("#%d", s.PullRequest.Number)
		repo := s.PullRequest.Repository.NameWithOwner

		// 名称
		tp.AddField(s.Name)
		if tp.IsTTY() {
			tp.AddField(pr, tableprinter.WithColor(cs.ColorFromString(prShared.ColorForPRState(*s.PullRequest))))
		} else {
			tp.AddField(pr)
		}

		// 仓库
		tp.AddField(repo, tableprinter.WithColor(cs.Muted))

		// 状态
		if tp.IsTTY() {
			tp.AddField(shared.SessionStateString(s.State), tableprinter.WithColor(shared.ColorFuncForSessionState(*s, cs)))
		} else {
			tp.AddField(shared.SessionStateString(s.State))
		}

		// 创建时间
		if tp.IsTTY() {
			tp.AddTimeField(time.Now(), s.CreatedAt, cs.Muted)
		} else {
			tp.AddField(s.CreatedAt.Format(time.RFC3339))
		}

		tp.EndRow()
	}

	if err := tp.Render(); err != nil {
		return err
	}

	return nil
}
