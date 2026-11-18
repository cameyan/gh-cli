package view

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/capi"
	"github.com/cli/cli/v2/pkg/cmd/agent-task/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	defaultLimit           = 40
	defaultLogPollInterval = 5 * time.Second
)

type ViewOptions struct {
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	CapiClient func() (capi.CapiClient, error)
	HttpClient func() (*http.Client, error)
	Finder     prShared.PRFinder
	Prompter   prompter.Prompter
	Browser    browser.Browser
	Exporter   cmdutil.Exporter

	LogRenderer func() shared.LogRenderer
	Sleep       func(d time.Duration)

	SelectorArg string
	PRNumber    int
	SessionID   string
	Web         bool
	Log         bool
	Follow      bool
}

func defaultLogRenderer() shared.LogRenderer {
	return shared.NewLogRenderer()
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:          f.IOStreams,
		HttpClient:  f.HttpClient,
		CapiClient:  shared.CapiClientFunc(f),
		Prompter:    f.Prompter,
		Browser:     f.Browser,
		LogRenderer: defaultLogRenderer,
		Sleep:       time.Sleep,
	}

	cmd := &cobra.Command{
		Use:   "view [<session-id> | <pr-number> | <pr-url> | <pr-branch>]",
		Short: "查看 agent 任务会话(预览版)",
		Long: heredoc.Doc(`
			查看 agent 任务会话
		`),
		Example: heredoc.Doc(`
			# 按 session ID 查看 agent 任务
			$ gh agent-task view e2fa49d2-f164-4a56-ab99-498090b8fcdf

			# 按当前仓库中的 PR 编号查看 agent 任务
			$ gh agent-task view 12345

			# 按 PR 编号查看 agent 任务
			$ gh agent-task view --repo OWNER/REPO 12345

			# 按 PR 引用查看 agent 任务
			$ gh agent-task view OWNER/REPO#12345

			# 在浏览器中查看 PR 的 agent 任务
			$ gh agent-task view 12345 --web
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// 支持 -R/--repo 覆盖
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.SelectorArg = args[0]
				if shared.IsSessionID(opts.SelectorArg) {
					opts.SessionID = opts.SelectorArg
				} else if sessionID, err := shared.ParseSessionIDFromURL(opts.SelectorArg); err == nil {
					opts.SessionID = sessionID
				}
			}

			if opts.SessionID == "" && !opts.IO.CanPrompt() {
				return fmt.Errorf("非交互模式下必须提供 session ID")
			}

			if opts.Follow && !opts.Log {
				return cmdutil.FlagErrorf("提供 --follow 时必须同时提供 --log")
			}

			if opts.Finder == nil {
				opts.Finder = prShared.NewFinder(f)
			}

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmdutil.EnableRepoOverride(cmd, f)

	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开 agent 任务")
	cmd.Flags().BoolVar(&opts.Log, "log", false, "显示 agent 会话日志")
	cmd.Flags().BoolVar(&opts.Follow, "follow", false, "持续跟踪 agent 会话日志")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, capi.SessionFields)

	return cmd
}

func viewRun(opts *ViewOptions) error {
	capiClient, err := opts.CapiClient()
	if err != nil {
		return err
	}

	ctx := context.Background()
	cs := opts.IO.ColorScheme()

	opts.IO.StartProgressIndicatorWithLabel("正在获取 agent 会话...")
	defer opts.IO.StopProgressIndicator()

	var session *capi.Session

	if opts.SessionID != "" {
		sess, err := capiClient.GetSession(ctx, opts.SessionID)
		if err != nil {
			if errors.Is(err, capi.ErrSessionNotFound) {
				fmt.Fprintln(opts.IO.ErrOut, "未找到会话")
				return cmdutil.SilentError
			}
			return err
		}

		opts.IO.StopProgressIndicator()

		if opts.Web {
			var webURL string
			if sess.PullRequest != nil {
				webURL = fmt.Sprintf("%s/agent-sessions/%s", sess.PullRequest.URL, url.PathEscape(sess.ID))
			} else {
				// 目前网页端的 Copilot Agents 主页不支持聚焦到某个特定会话，
				// 因此我们只能跳转到主页。
				webURL = capi.AgentsHomeURL
			}

			if opts.IO.IsStdoutTTY() {
				fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(webURL))
			}
			return opts.Browser.Browse(webURL)
		}

		session = sess
	} else {
		var prID int64
		var prURL string

		if opts.SelectorArg != "" {
			// Finder 不支持 PR/issue 引用格式 (如 owner/repo#123)，
			// 因此我们需要检查选择器参数是否为引用格式，并直接获取对应的 PR。
			if repo, num, err := prShared.ParseFullReference(opts.SelectorArg); err == nil {
				// 由于选择器是引用格式 (即不包含主机名信息)，我们需要
				// 查询基础仓库来获取主机名。
				baseRepo, err := opts.BaseRepo()
				if err != nil {
					return err
				}

				hostname := baseRepo.RepoHost()
				if hostname != ghinstance.Default() {
					return fmt.Errorf("此主机不支持 agent 任务: %s", hostname)
				}

				prID, prURL, err = capiClient.GetPullRequestDatabaseID(ctx, hostname, repo.RepoOwner(), repo.RepoName(), num)
				if err != nil {
					return fmt.Errorf("获取 PR 失败: %w", err)
				}
			}
		}

		if prID == 0 {
			findOptions := prShared.FindOptions{
				Selector:        opts.SelectorArg,
				Fields:          []string{"id", "url", "fullDatabaseId"},
				DisableProgress: true,
			}

			pr, repo, err := opts.Finder.Find(findOptions)
			if err != nil {
				return err
			}

			if repo.RepoHost() != ghinstance.Default() {
				return fmt.Errorf("此主机不支持 agent 任务: %s", repo.RepoHost())
			}

			databaseID, err := strconv.ParseInt(pr.FullDatabaseID, 10, 64)
			if err != nil {
				return fmt.Errorf("解析 PR 失败: %w", err)
			}

			prID = databaseID
			prURL = pr.URL
		}

		sessions, err := capiClient.ListSessionsByResourceID(ctx, "pull", prID, defaultLimit)
		if err != nil {
			return fmt.Errorf("列出 PR 的会话失败: %w", err)
		}

		if len(sessions) == 0 {
			fmt.Fprintln(opts.IO.ErrOut, "该 PR 没有找到会话")
			return cmdutil.SilentError
		}

		opts.IO.StopProgressIndicator()

		if opts.Web {
			// 注意，我们必须确保 PR 存在且至少关联了一个会话，
			// 否则 `/agent-sessions` 页面会显示 404 错误。

			// 我们不需要跳转到特定会话；如果只有一个会话，
			// 界面会自动显示它，否则用户可以从列表中选择。
			// 这样做是为了避免不必要的提示。
			webURL := prURL + "/agent-sessions"
			if opts.IO.IsStdoutTTY() {
				fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(webURL))
			}
			return opts.Browser.Browse(webURL)
		}

		selectedSession := sessions[0]
		if len(sessions) > 1 {
			now := time.Now()
			options := make([]string, 0, len(sessions))
			for _, session := range sessions {
				options = append(options, fmt.Sprintf(
					"%s %s • 更新于 %s",
					shared.SessionSymbol(cs, session.State),
					session.Name,
					text.FuzzyAgo(now, session.LastUpdatedAt),
				))
			}

			selected, err := opts.Prompter.Select("选择一个会话", "", options)
			if err != nil {
				return err
			}

			selectedSession = sessions[selected]
		}

		opts.IO.StartProgressIndicatorWithLabel("正在获取 agent 会话...")
		defer opts.IO.StopProgressIndicator()

		// ListSessionsByResourceID 返回的会话并不包含所有字段。
		// 因此我们需要单独获取该会话以拿到全部详情。
		session, err = capiClient.GetSession(ctx, selectedSession.ID)
		if err != nil {
			return err
		}

		opts.IO.StopProgressIndicator()
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, session)
	}

	if opts.Log {
		return printLogs(opts, capiClient, session.ID)
	}

	printSession(opts, session)
	return nil
}

func printSession(opts *ViewOptions, session *capi.Session) {
	cs := opts.IO.ColorScheme()

	fmt.Fprintf(opts.IO.Out, "%s • %s\n",
		shared.ColorFuncForSessionState(*session, cs)(shared.SessionStateString(session.State)),
		cs.Bold(session.Name),
	)

	if session.User != nil {
		fmt.Fprintf(opts.IO.Out, "代表 %s 发起于 %s\n", session.User.Login, text.FuzzyAgo(time.Now(), session.CreatedAt))
	} else {
		// 正常情况下不应发生，但我们需要覆盖该路径
		fmt.Fprintf(opts.IO.Out, "发起于 %s\n", text.FuzzyAgo(time.Now(), session.CreatedAt))
	}

	usedPremiumRequests := strings.TrimSuffix(fmt.Sprintf("%.1f", session.PremiumRequests), ".0")
	usedPremiumRequestsNote := fmt.Sprintf("已使用 %s 个高级请求", usedPremiumRequests)

	var durationNote string
	if session.CompletedAt.After(session.CreatedAt) {
		durationNote = fmt.Sprintf(" • 耗时 %s", session.CompletedAt.Sub(session.CreatedAt).Round(time.Second).String())
	}

	fmt.Fprintf(opts.IO.Out, "%s%s\n", cs.Muted(usedPremiumRequestsNote), cs.Muted(durationNote))

	// 注意，会话刚创建时还没有可用的 PR。
	if session.PullRequest != nil {
		fmt.Fprintf(opts.IO.Out, "\n%s%s • %s\n",
			session.PullRequest.Repository.NameWithOwner,
			cs.ColorFromString(prShared.ColorForPRState(*session.PullRequest))(fmt.Sprintf("#%d", session.PullRequest.Number)),
			cs.Bold(session.PullRequest.Title),
		)
	}

	if session.Error != nil {
		var workflowRunURL string
		if session.WorkflowRunID != 0 && session.PullRequest != nil {
			if u, err := url.Parse(session.PullRequest.URL); err == nil {
				workflowRunURL = fmt.Sprintf("%s://%s/%s/actions/runs/%d", u.Scheme, u.Host, session.PullRequest.Repository.NameWithOwner, session.WorkflowRunID)
			}
		}

		message := session.Error.Message
		if message == "" {
			message = "发生了一个错误"
		}
		fmt.Fprintf(opts.IO.Out, "\n%s %s\n", cs.FailureIconWithColor(cs.Red), message)

		if workflowRunURL != "" {
			// 我们不需要在链接前添加任何文字 (如 "在此查看日志")，
			// 因为错误消息本身已经包含了全部信息。
			fmt.Fprintf(opts.IO.Out, "%s\n", workflowRunURL)
		}
	}

	if !opts.Log {
		fmt.Fprint(opts.IO.Out, cs.Mutedf("\n查看详细的会话日志，请尝试:\ngh agent-task view '%s' --log\n", session.ID))
	} else if !opts.Follow {
		fmt.Fprint(opts.IO.Out, cs.Mutedf("\n持续跟踪会话日志，请尝试:\ngh agent-task view '%s' --log --follow\n", session.ID))
	}

	if session.PullRequest != nil {
		fmt.Fprintln(opts.IO.Out, cs.Muted("\n在 GitHub 上查看此会话:"))
		fmt.Fprintln(opts.IO.Out, cs.Muted(fmt.Sprintf("%s/agent-sessions/%s", session.PullRequest.URL, url.PathEscape(session.ID))))
	}
}

func printLogs(opts *ViewOptions, capiClient capi.CapiClient, sessionID string) error {
	ctx := context.Background()

	renderer := opts.LogRenderer()

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	if opts.Follow {
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

	raw, err := capiClient.GetSessionLogs(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("获取会话日志失败: %w", err)
	}

	_, err = renderer.Render(raw, opts.IO.Out, opts.IO)
	return err
}
