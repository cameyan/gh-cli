package create

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/capi"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const defaultLogPollInterval = 5 * time.Second

// CreateOptions 保存 create 命令的选项
type CreateOptions struct {
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	CapiClient func() (capi.CapiClient, error)
	Config     func() (gh.Config, error)

	LogRenderer func() shared.LogRenderer
	Sleep       func(d time.Duration)

	ProblemStatement     string
	CustomAgent          string
	BackOff              backoff.BackOff
	BaseBranch           string
	Prompter             prompter.Prompter
	ProblemStatementFile string
	Follow               bool
}

func defaultLogRenderer() shared.LogRenderer {
	return shared.NewLogRenderer()
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:          f.IOStreams,
		CapiClient:  shared.CapiClientFunc(f),
		Config:      f.Config,
		Prompter:    f.Prompter,
		LogRenderer: defaultLogRenderer,
		Sleep:       time.Sleep,
	}

	cmd := &cobra.Command{
		Use:   "create [<task description>] [flags]",
		Short: "创建 agent 任务(预览)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// 支持 -R/--repo 覆盖
			opts.BaseRepo = f.BaseRepo

			if err := cmdutil.MutuallyExclusive("-F 和位置参数只能提供一个", len(args) > 0, opts.ProblemStatementFile != ""); err != nil {
				return err
			}

			// 从位置参数填充 ProblemStatement
			if len(args) > 0 {
				opts.ProblemStatement = args[0]
				if strings.TrimSpace(opts.ProblemStatement) == "" {
					return cmdutil.FlagErrorf("任务描述不能为空")
				}
			} else if opts.ProblemStatementFile == "" && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互模式下需要提供任务描述或 -F")
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
		Example: heredoc.Doc(`
			# 从内联描述创建任务
			$ gh agent-task create "build me a new app"

			# 从内联描述创建任务并跟踪日志
			$ gh agent-task create "build me a new app" --follow

			# 从文件创建任务
			$ gh agent-task create -F task-desc.md

			# 从标准输入读取问题描述来创建任务
			$ echo "build me a new app" | gh agent-task create -F -

			# 使用编辑器创建任务
			$ gh agent-task create

			# 使用编辑器创建任务，并以文件作为模板
			$ gh agent-task create -F task-desc.md

			# 为 PR 选择其他基础分支
			$ gh agent-task create "fix errors" --base branch

			# 使用 '.github/agents/my-agent.md' 中定义的自定义 agent 创建任务
			$ gh agent-task create "build me a new app" --custom-agent my-agent
		`),
	}

	cmdutil.EnableRepoOverride(cmd, f)

	cmd.Flags().StringVarP(&opts.ProblemStatementFile, "from-file", "F", "", "从 `file` 读取任务描述(使用 \"-\" 表示从标准输入读取)")
	cmd.Flags().StringVarP(&opts.BaseBranch, "base", "b", "", "PR 的基础分支(未提供时使用默认分支)")
	cmd.Flags().BoolVar(&opts.Follow, "follow", false, "跟踪 agent 会话日志")
	cmd.Flags().StringVarP(&opts.CustomAgent, "custom-agent", "a", "", "为任务使用自定义 agent，例如使用 'my-agent' 对应 'my-agent.md' 这个 agent")

	return cmd
}

func createRun(opts *CreateOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil || repo == nil {
		// 这里不打印 BaseRepo() 返回的错误，因为我们希望给出
		// 清晰、友好且可操作的信息。
		return fmt.Errorf("需要提供仓库；请在仓库中重新运行，或使用 --repo owner/name 提供仓库")
	}

	if opts.ProblemStatement == "" {
		if opts.ProblemStatementFile != "" {
			fileContent, err := cmdutil.ReadFile(opts.ProblemStatementFile, opts.IO.In)
			if err != nil {
				return fmt.Errorf("无法读取任务描述文件: %w", err)
			}

			trimmed := strings.TrimSpace(string(fileContent))
			if trimmed == "" {
				return errors.New("任务描述文件不能为空")
			}

			opts.ProblemStatement = trimmed
		} else {
			desc, err := opts.Prompter.MarkdownEditor("请输入任务描述", opts.ProblemStatement, false)
			if err != nil {
				return err
			}

			trimmed := strings.TrimSpace(string(desc))
			if trimmed == "" {
				return errors.New("需要提供任务描述")
			}

			opts.ProblemStatement = trimmed
		}
	}

	client, err := opts.CapiClient()
	if err != nil {
		return err
	}

	ctx := context.Background()
	opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在 %s/%s 中创建 agent 任务...", repo.RepoOwner(), repo.RepoName()))
	defer opts.IO.StopProgressIndicator()

	job, err := client.CreateJob(ctx, repo.RepoOwner(), repo.RepoName(), opts.ProblemStatement, opts.BaseBranch, opts.CustomAgent)
	if err != nil {
		return err
	}

	if opts.Follow {
		opts.IO.StopProgressIndicator()
		fmt.Fprintf(opts.IO.Out, "正在显示任务 %s 的会话日志。按 Ctrl+C 停止。\n", job.ID)
		return followLogs(opts, client, job.SessionID)
	}

	sessionURL, err := fetchJobSessionURL(ctx, client, repo, job, opts.BackOff)
	opts.IO.StopProgressIndicator()

	if sessionURL != "" {
		fmt.Fprintln(opts.IO.Out, sessionURL)
	} else {
		if err != nil {
			// 即使真的出现这种情况，我们仍然希望用户能看到兜底
			// 消息和 URL。因此我们不因该错误而返回，但仍会打印它。
			fmt.Fprintf(opts.IO.ErrOut, "%v\n", err)
		}
		fmt.Fprintf(opts.IO.Out, "任务 %s 已排队，查看进度: %s\n", job.ID, capi.AgentsHomeURL)
	}

	return nil
}

func agentSessionWebURL(repo ghrepo.Interface, j *capi.Job) string {
	if j.PullRequest == nil {
		return ""
	}
	if j.SessionID == "" {
		return fmt.Sprintf("https://github.com/%s/%s/pull/%d", url.PathEscape(repo.RepoOwner()), url.PathEscape(repo.RepoName()), j.PullRequest.Number)
	}
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d/agent-sessions/%s", url.PathEscape(repo.RepoOwner()), url.PathEscape(repo.RepoName()), j.PullRequest.Number, url.PathEscape(j.SessionID))
}

// fetchJobSessionURL 尝试返回任务的 agent 会话 URL。如果 pull request
// 尚不可用，则返回 ("", nil)。
func fetchJobSessionURL(ctx context.Context, client capi.CapiClient, repo ghrepo.Interface, job *capi.Job, bo backoff.BackOff) (string, error) {
	if job.PullRequest != nil && job.PullRequest.Number > 0 {
		// 如果碰巧拿到了 agent 会话 URL，则直接返回。
		// 目前这种情况不会发生。
		return agentSessionWebURL(repo, job), nil
	}

	if bo == nil {
		bo = backoff.NewExponentialBackOff(
			backoff.WithMaxElapsedTime(10*time.Second),
			backoff.WithInitialInterval(300*time.Millisecond),
			backoff.WithMaxInterval(10*time.Second),
			backoff.WithMultiplier(1.5),
		)
	}

	jobWithPR, err := fetchJobWithBackoff(ctx, client, repo, job.ID, bo)
	if jobWithPR != nil {
		return agentSessionWebURL(repo, jobWithPR), nil
	}
	return "", err
}

// fetchJobWithBackoff 轮询任务资源，直到出现 PR 编号或整体超时。
// 成功时返回更新后的 Job，超时时返回 (nil, nil)，
// 仅在不可重试的失败时返回 (nil, error)。
func fetchJobWithBackoff(ctx context.Context, client capi.CapiClient, repo ghrepo.Interface, jobID string, bo backoff.BackOff) (*capi.Job, error) {
	// 用于标记超时的哨兵错误
	var errPRNotReady = errors.New("任务尚未就绪")

	var result *capi.Job
	retryErr := backoff.Retry(func() error {
		j, err := client.GetJob(ctx, repo.RepoOwner(), repo.RepoName(), jobID)
		if err != nil {
			// GetJob 出错时不重试，立即向上抛出。
			return backoff.Permanent(err)
		}
		if j.PullRequest != nil && j.PullRequest.Number > 0 {
			result = j
			return nil
		}
		return errPRNotReady
	}, backoff.WithContext(bo, ctx))

	if retryErr != nil {
		if errors.Is(retryErr, errPRNotReady) {
			// 已超时
			return nil, nil
		}
		return nil, retryErr
	}
	return result, nil
}

func followLogs(opts *CreateOptions, capiClient capi.CapiClient, sessionID string) error {
	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	ctx := context.Background()
	renderer := opts.LogRenderer()

	var called bool
	fetcher := func() ([]byte, error) {
		if called {
			opts.Sleep(defaultLogPollInterval)
		}
		called = true
		raw, err := capiClient.GetSessionLogs(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}

	return renderer.Follow(fetcher, opts.IO.Out, opts.IO)
}
