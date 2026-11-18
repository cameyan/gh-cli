package merge

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
	"github.com/spf13/cobra"
)

type editor interface {
	Edit(string, string) (string, error)
}

type MergeOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	IO         *iostreams.IOStreams
	Branch     func() (string, error)
	Remotes    func() (ghContext.Remotes, error)
	Prompter   shared.Prompt

	Finder shared.PRFinder

	SelectorArg  string
	DeleteBranch bool
	MergeMethod  PullRequestMergeMethod

	AutoMergeEnable  bool
	AutoMergeDisable bool

	AuthorEmail string

	Body    string
	BodySet bool
	Subject string
	Editor  editor

	UseAdmin                bool
	IsDeleteBranchIndicated bool
	CanDeleteLocalBranch    bool
	MergeStrategyEmpty      bool
	MatchHeadCommit         string
}

// ErrAlreadyInMergeQueue 表示该拉取请求(PR)已在合并队列(merge queue)中
var ErrAlreadyInMergeQueue = errors.New("已在合并队列中")

func NewCmdMerge(f *cmdutil.Factory, runF func(*MergeOptions) error) *cobra.Command {
	opts := &MergeOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Branch:     f.Branch,
		Remotes:    f.Remotes,
		Prompter:   f.Prompter,
	}

	var (
		flagMerge  bool
		flagSquash bool
		flagRebase bool
	)

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "merge [<number> | <url> | <branch>]",
		Short: "合并一个拉取请求(PR)",
		Long: heredoc.Docf(`
			在 GitHub 上合并一个拉取请求(PR)。

			若未提供参数，将选择属于当前分支的拉取请求(PR)。

			当目标分支需要合并队列(merge queue)时，无需指定合并策略。
			若必须(required)检查尚未通过，将启用自动合并(auto-merge)。
			若必须检查已通过，该拉取请求(PR)将被加入合并队列。
			若要绕过合并队列直接合并，请传入 %[1]s--admin%[1]s flag。
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 --repo flag 时必须提供位置参数")
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			methodFlags := 0
			if flagMerge {
				opts.MergeMethod = PullRequestMergeMethodMerge
				methodFlags++
			}
			if flagRebase {
				opts.MergeMethod = PullRequestMergeMethodRebase
				methodFlags++
			}
			if flagSquash {
				opts.MergeMethod = PullRequestMergeMethodSquash
				methodFlags++
			}
			if methodFlags == 0 {
				opts.MergeStrategyEmpty = true
			} else if methodFlags > 1 {
				return cmdutil.FlagErrorf("只能启用 --merge、--rebase 或 --squash 之一")
			}

			opts.IsDeleteBranchIndicated = cmd.Flags().Changed("delete-branch")
			opts.CanDeleteLocalBranch = !cmd.Flags().Changed("repo")

			bodyProvided := cmd.Flags().Changed("body")
			bodyFileProvided := bodyFile != ""

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--auto`、`--disable-auto` 或 `--admin` 之一",
				opts.AutoMergeEnable,
				opts.AutoMergeDisable,
				opts.UseAdmin,
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--body` 或 `--body-file` 之一",
				bodyProvided,
				bodyFileProvided,
			); err != nil {
				return err
			}

			if bodyProvided || bodyFileProvided {
				opts.BodySet = true
				if bodyFileProvided {
					b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
					if err != nil {
						return err
					}
					opts.Body = string(b)
				}
			}

			opts.Editor = &userEditor{
				io:     opts.IO,
				config: f.Config,
			}

			if runF != nil {
				return runF(opts)
			}

			err := mergeRun(opts)
			if errors.Is(err, ErrAlreadyInMergeQueue) {
				return nil
			}
			return err
		},
	}

	cmd.Flags().BoolVar(&opts.UseAdmin, "admin", false, "使用管理员权限合并不满足要求的拉取请求(PR)")
	cmd.Flags().BoolVarP(&opts.DeleteBranch, "delete-branch", "d", false, "合并后删除本地与远端分支(branch)")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "合并提交(merge commit)的正文 `text`")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")
	cmd.Flags().StringVarP(&opts.Subject, "subject", "t", "", "合并提交(merge commit)的主题 `text`")
	cmd.Flags().BoolVarP(&flagMerge, "merge", "m", false, "将这些提交与基础分支(base branch)合并")
	cmd.Flags().BoolVarP(&flagRebase, "rebase", "r", false, "将这些提交变基(rebase)到基础分支上")
	cmd.Flags().BoolVarP(&flagSquash, "squash", "s", false, "将这些提交压缩(squash)为一个提交并合并入基础分支")
	cmd.Flags().BoolVar(&opts.AutoMergeEnable, "auto", false, "仅在满足全部必要条件后自动合并")
	cmd.Flags().BoolVar(&opts.AutoMergeDisable, "disable-auto", false, "禁用该拉取请求(PR)的自动合并(auto-merge)")
	cmd.Flags().StringVar(&opts.MatchHeadCommit, "match-head-commit", "", "允许合并时拉取请求(PR)源分支(head)必须匹配的提交(commit) `SHA`")
	cmd.Flags().StringVarP(&opts.AuthorEmail, "author-email", "A", "", "合并提交(merge commit)作者的邮箱 `text`")
	return cmd
}

// mergeContext 包含合并拉取请求(PR)所需的状态与依赖。
type mergeContext struct {
	pr                 *api.PullRequest
	baseRepo           ghrepo.Interface
	httpClient         *http.Client
	opts               *MergeOptions
	cs                 *iostreams.ColorScheme
	isTerminal         bool
	merged             bool
	localBranchExists  bool
	autoMerge          bool
	crossRepoPR        bool
	deleteBranch       bool
	mergeQueueRequired bool
}

// 尝试在该拉取请求(PR)上禁用自动合并(auto merge)。
func (m *mergeContext) disableAutoMerge() error {
	if err := disableAutoMerge(m.httpClient, m.baseRepo, m.pr.ID); err != nil {
		return err
	}
	return m.infof("%s 已为拉取请求(PR) %s#%d 禁用自动合并(auto-merge)\n", m.cs.SuccessIconWithColor(m.cs.Green), ghrepo.FullName(m.baseRepo), m.pr.Number)
}

// 检查该拉取请求(PR)是否在合并队列(merge queue)中
func (m *mergeContext) inMergeQueue() error {
	// 如果拉取请求(PR)已在合并队列中，则无法执行任何后续操作
	if m.pr.IsInMergeQueue {
		_ = m.warnf("%s 拉取请求(PR) %s#%d 已在合并(merge)队列中\n", m.cs.WarningIcon(), ghrepo.FullName(m.baseRepo), m.pr.Number)
		return ErrAlreadyInMergeQueue
	}
	return nil
}

// 若拉取请求(PR)与远端分支(branch)已产生分歧，则发出警告。
func (m *mergeContext) warnIfDiverged() {
	if m.opts.SelectorArg != "" || len(m.pr.Commits.Nodes) == 0 {
		return
	}

	localBranchLastCommit, err := m.opts.GitClient.LastCommit(context.Background())
	if err != nil {
		return
	}

	if localBranchLastCommit.Sha == m.pr.Commits.Nodes[len(m.pr.Commits.Nodes)-1].Commit.OID {
		return
	}

	_ = m.warnf("%s 拉取请求(PR) %s#%d (%s) 已与本地分支(branch)产生分歧\n", m.cs.Yellow("!"), ghrepo.FullName(m.baseRepo), m.pr.Number, m.pr.Title)
}

// 检查拉取请求(PR)的当前状态是否允许合并
func (m *mergeContext) canMerge() error {
	if m.mergeQueueRequired {
		// 对启用了合并队列(merge queue)策略的 PR 请求删除分支是不允许的。
		// 这样做可能意外地在合并前删除分支、关闭 PR，
		// 并将 PR 从合并队列中移除。
		if m.opts.DeleteBranch {
			return fmt.Errorf("%s 启用合并队列(merge queue)时不能使用 `-d` 或 `--delete-branch`", m.cs.FailureIcon())
		}
		// 否则，拉取请求(PR)总是可以被加入合并队列
		return nil
	}

	reason := blockedReason(m.pr.MergeStateStatus, m.opts.UseAdmin)

	if reason == "" || m.autoMerge || m.merged {
		return nil
	}

	_ = m.warnf("%s 拉取请求(PR) %s#%d 当前无法合并: %s。\n", m.cs.FailureIcon(), ghrepo.FullName(m.baseRepo), m.pr.Number, reason)
	_ = m.warnf("若要在满足全部条件后自动合并该拉取请求(PR)，请添加 `--auto` flag。\n")
	if remote := remoteForMergeConflictResolution(m.baseRepo, m.pr, m.opts); remote != nil {
		mergeOrRebase := "merge"
		if m.opts.MergeMethod == PullRequestMergeMethodRebase {
			mergeOrRebase = "rebase"
		}
		fetchBranch := fmt.Sprintf("%s %s", remote.Name, m.pr.BaseRefName)
		mergeBranch := fmt.Sprintf("%s %s/%s", mergeOrRebase, remote.Name, m.pr.BaseRefName)
		cmd := fmt.Sprintf("gh pr checkout %d && git fetch %s && git %s", m.pr.Number, fetchBranch, mergeBranch)
		_ = m.warnf("运行以下命令以在本地解决合并冲突(merge conflict):\n  %s\n", m.cs.Bold(cmd))
	}
	if !m.opts.UseAdmin && allowsAdminOverride(m.pr.MergeStateStatus) {
		// TODO: 仅对仓库管理员显示该 flag
		_ = m.warnf("若要使用管理员权限立即合并该拉取请求(PR)，请添加 `--admin` flag。\n")
	}
	return cmdutil.SilentError
}

// 合并拉取请求(PR)。可能提示用户输入合并所需的参数。
func (m *mergeContext) merge() error {
	if m.merged {
		return nil
	}

	payload := mergePayload{
		repo:            m.baseRepo,
		pullRequestID:   m.pr.ID,
		method:          m.opts.MergeMethod,
		auto:            m.autoMerge,
		commitSubject:   m.opts.Subject,
		commitBody:      m.opts.Body,
		setCommitBody:   m.opts.BodySet,
		expectedHeadOid: m.opts.MatchHeadCommit,
		authorEmail:     m.opts.AuthorEmail,
	}

	if m.shouldAddToMergeQueue() {
		if !m.opts.MergeStrategyEmpty {
			// 目前仅发出警告
			_ = m.warnf("%s %s 的合并策略由合并队列(merge queue)设定\n", m.cs.Yellow("!"), m.pr.BaseRefName)
		}
		// 自动合并要么启用 auto merge，要么加入合并队列
		payload.auto = true
	} else {
		// 若尚未提供，则获取用户输入
		if m.opts.MergeStrategyEmpty {
			if !m.opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须提供 --merge、--rebase 或 --squash")
			}

			_ = m.infof("正在合并拉取请求(PR) %s#%d (%s)\n", ghrepo.FullName(m.baseRepo), m.pr.Number, m.pr.Title)

			apiClient := api.NewClientFromHTTP(m.httpClient)
			r, err := api.GitHubRepo(apiClient, m.baseRepo)
			if err != nil {
				return err
			}

			payload.method, err = mergeMethodSurvey(m.opts.Prompter, r)
			if err != nil {
				return err
			}

			m.deleteBranch, err = deleteBranchSurvey(m.opts, m.crossRepoPR, m.localBranchExists)
			if err != nil {
				return err
			}

			allowEditMsg := payload.method != PullRequestMergeMethodRebase
			for {
				action, err := confirmSurvey(m.opts.Prompter, allowEditMsg)
				if err != nil {
					return fmt.Errorf("无法确认: %w", err)
				}

				submit, err := confirmSubmission(m.httpClient, m.opts, action, &payload)
				if err != nil {
					return err
				}
				if submit {
					break
				}
			}
		}
	}

	err := mergePullRequest(m.httpClient, payload)
	if err != nil {
		return err
	}

	if m.shouldAddToMergeQueue() {
		_ = m.infof("%s 拉取请求(PR) %s#%d 就绪后将被加入 %s 的合并队列(merge queue)\n", m.cs.SuccessIconWithColor(m.cs.Green), ghrepo.FullName(m.baseRepo), m.pr.Number, m.pr.BaseRefName)
		return nil
	}

	if payload.auto {
		method := ""
		switch payload.method {
		case PullRequestMergeMethodRebase:
			method = " (通过 rebase)"
		case PullRequestMergeMethodSquash:
			method = " (通过 squash)"
		}
		return m.infof("%s 拉取请求(PR) %s#%d 将在满足全部条件后自动合并(auto merge)%s\n", m.cs.SuccessIconWithColor(m.cs.Green), ghrepo.FullName(m.baseRepo), m.pr.Number, method)
	}

	action := "已合并(Merged)"
	switch payload.method {
	case PullRequestMergeMethodRebase:
		action = "已变基并合并(Rebased and merged)"
	case PullRequestMergeMethodSquash:
		action = "已压缩并合并(Squashed and merged)"
	}
	return m.infof("%s %s拉取请求(PR) %s#%d (%s)\n", m.cs.SuccessIconWithColor(m.cs.Magenta), action, ghrepo.FullName(m.baseRepo), m.pr.Number, m.pr.Title)
}

// 若被请求且被允许，删除本地分支(branch)。
func (m *mergeContext) deleteLocalBranch() error {
	if m.autoMerge {
		return nil
	}

	if m.merged {
		if m.opts.IO.CanPrompt() && !m.opts.IsDeleteBranchIndicated {
			message := fmt.Sprintf("拉取请求(PR) %s#%d 已被合并。在本地删除该分支(branch)?", ghrepo.FullName(m.baseRepo), m.pr.Number)
			confirmed, err := m.opts.Prompter.Confirm(message, false)
			if err != nil {
				return fmt.Errorf("无法显示提示: %w", err)
			}
			m.deleteBranch = confirmed
		} else {
			_ = m.warnf("%s 拉取请求(PR) %s#%d 已被合并\n", m.cs.WarningIcon(), ghrepo.FullName(m.baseRepo), m.pr.Number)
		}
	}

	if !m.deleteBranch || !m.opts.CanDeleteLocalBranch || !m.localBranchExists {
		return nil
	}

	ctx := context.Background()

	worktrees, err := m.opts.GitClient.Worktrees(ctx)
	if err != nil {
		_ = m.warnf("%s 无法检查工作树(worktree); 跳过删除本地分支(branch): %s\n", m.cs.WarningIcon(), err)
		return nil
	}

	currentWorkdir, err := m.opts.GitClient.ToplevelDir(ctx)
	if err != nil {
		_ = m.warnf("%s 无法确定当前工作树(worktree); 跳过删除本地分支(branch): %s\n", m.cs.WarningIcon(), err)
		return nil
	}

	switchedToBranch := ""
	headWorktree := git.WorktreeForBranch(worktrees, m.pr.HeadRefName)
	if headWorktree != nil && headWorktree.Prunable {
		if err := m.opts.GitClient.WorktreePrune(ctx); err != nil {
			_ = m.warnf("%s 无法为 %s 清理过期的工作树(worktree)元数据; 跳过删除本地分支(branch): %s\n", m.cs.WarningIcon(), headWorktree.Path, err)
			return nil
		}
		_ = m.infof("%s 已清理 %s 的过期工作树(worktree)元数据\n", m.cs.SuccessIconWithColor(m.cs.Red), headWorktree.Path)
		headWorktree = nil
	}
	if headWorktree != nil {
		mainWorktree := &worktrees[0]

		switch {
		// 源分支(head)在当前关联的工作树(worktree)中检出。
		case headWorktree.Path == currentWorkdir && headWorktree.Path != mainWorktree.Path:
			_ = m.warnf("%s 分支(branch) %s 已在当前工作树(%s)中检出; 跳过本地删除\n",
				m.cs.WarningIcon(), m.cs.Cyan(m.pr.HeadRefName), currentWorkdir)
			_ = m.warnf("  要完成清理，请先退出该工作树，然后运行:\n")
			_ = m.warnf("  git worktree remove %s && git branch -D %s\n",
				currentWorkdir, m.pr.HeadRefName)
			return nil

		// 源分支(head)在当前主工作树中检出。
		case headWorktree.Path == currentWorkdir:
			baseWorktree := git.WorktreeForBranch(worktrees, m.pr.BaseRefName)
			if baseWorktree != nil && baseWorktree.Prunable {
				if err := m.opts.GitClient.WorktreePrune(ctx); err != nil {
					_ = m.warnf("%s 无法为 %s 清理过期的工作树(worktree)元数据; 跳过删除本地分支(branch): %s\n", m.cs.WarningIcon(), baseWorktree.Path, err)
					return nil
				}
				_ = m.infof("%s 已清理 %s 的过期工作树(worktree)元数据\n", m.cs.SuccessIconWithColor(m.cs.Red), baseWorktree.Path)
				baseWorktree = nil
			}
			if baseWorktree != nil && baseWorktree.Path != currentWorkdir {
				_ = m.warnf("%s 基础分支(base branch) %s 已在另一个工作树(%s)中检出; 跳过本地删除\n",
					m.cs.WarningIcon(), m.cs.Cyan(m.pr.BaseRefName), baseWorktree.Path)
				_ = m.warnf("  要完成清理，请将 %s 处的工作树切离 %s，然后在当前路径下运行:\n",
					baseWorktree.Path, m.cs.Cyan(m.pr.BaseRefName))
				_ = m.warnf("  git checkout %s && git branch -D %s\n", m.pr.BaseRefName, m.pr.HeadRefName)
				return nil
			}

			remotes, err := m.opts.Remotes()
			if err != nil {
				return err
			}

			baseRemote, err := remotes.FindByRepo(m.baseRepo.RepoOwner(), m.baseRepo.RepoName())
			if err != nil {
				return err
			}

			targetBranch := m.pr.BaseRefName
			if m.opts.GitClient.HasLocalBranch(ctx, targetBranch) {
				if err := m.opts.GitClient.CheckoutBranch(ctx, targetBranch); err != nil {
					return err
				}
			} else {
				if err := m.opts.GitClient.CheckoutNewBranch(ctx, baseRemote.Name, targetBranch); err != nil {
					return err
				}
			}

			if err := m.opts.GitClient.Pull(ctx, baseRemote.Name, targetBranch); err != nil {
				_ = m.warnf("%s 警告: 无法快进(fast-forward)到: %q\n", m.cs.WarningIcon(), targetBranch)
			}

			switchedToBranch = targetBranch

		// 源分支(head)在主工作树中检出，但该命令在其他位置运行。
		case headWorktree.Path == mainWorktree.Path:
			_ = m.warnf("%s 分支(branch) %s 已在主工作树(%s)中检出; 跳过本地删除\n",
				m.cs.WarningIcon(), m.cs.Cyan(m.pr.HeadRefName), mainWorktree.Path)
			_ = m.warnf("  要完成清理，请将主工作树切离 %s，然后运行:\n", m.cs.Cyan(m.pr.HeadRefName))
			_ = m.warnf("  git branch -D %s\n", m.pr.HeadRefName)
			return nil

		// 源分支(head)在另一个关联的工作树(worktree)中检出。
		default:
			if err := m.opts.GitClient.WorktreeRemove(ctx, headWorktree.Path); err != nil {
				_ = m.warnf("%s 无法移除工作树(worktree) %s; 跳过删除本地分支(branch): %s\n", m.cs.WarningIcon(), headWorktree.Path, err)
				return nil
			}
			_ = m.infof("%s 已移除工作树(worktree) %s\n", m.cs.SuccessIconWithColor(m.cs.Red), headWorktree.Path)
		}
	}

	if err := m.opts.GitClient.DeleteLocalBranch(ctx, m.pr.HeadRefName); err != nil {
		return fmt.Errorf("删除本地分支(branch) %s 失败: %w", m.cs.Cyan(m.pr.HeadRefName), err)
	}

	switchedStatement := ""
	if switchedToBranch != "" {
		switchedStatement = fmt.Sprintf(" 并已切换到分支(branch) %s", m.cs.Cyan(switchedToBranch))
	}

	return m.infof("%s 已删除本地分支(branch) %s%s\n", m.cs.SuccessIconWithColor(m.cs.Red), m.cs.Cyan(m.pr.HeadRefName), switchedStatement)
}

// 若被请求且被允许，删除远端分支(branch)。
func (m *mergeContext) deleteRemoteBranch() error {
	// 如果用户未提供 flag，则此前已被询问过是否要删除该分支(branch)
	if !m.deleteBranch || m.crossRepoPR || m.autoMerge {
		return nil
	}

	if !m.merged {
		apiClient := api.NewClientFromHTTP(m.httpClient)
		err := api.BranchDeleteRemote(apiClient, m.baseRepo, m.pr.HeadRefName)
		if err != nil {
			// 通常，当该分支(branch)已被删除时，API 会返回 422，并附带消息
			// "Reference does not exist"。它也可能返回 404 及同样的消息，但这种情况很少见。
			// 无论哪种情况，目标都已达成，因此不应返回错误。

			var isAlreadyDeletedError bool
			if httpErr := (api.HTTPError{}); errors.As(err, &httpErr) {
				// TODO: 由于 API 还会因其他几个原因返回 422，为了更准确，
				// 我们或许应将错误消息与 "Reference does not exist" 进行核对。
				isAlreadyDeletedError = httpErr.StatusCode == http.StatusUnprocessableEntity || httpErr.StatusCode == http.StatusNotFound
			}

			if !isAlreadyDeletedError {
				return fmt.Errorf("删除远端分支(branch) %s 失败: %w", m.cs.Cyan(m.pr.HeadRefName), err)
			}
		}
	}

	return m.infof("%s 已删除远端分支(branch) %s\n", m.cs.SuccessIconWithColor(m.cs.Red), m.cs.Cyan(m.pr.HeadRefName))
}

// 将拉取请求(PR)加入合并队列(merge queue)
// 管理员可绕过队列直接合并
func (m *mergeContext) shouldAddToMergeQueue() bool {
	return m.mergeQueueRequired && !m.opts.UseAdmin
}

func (m *mergeContext) warnf(format string, args ...any) error {
	_, err := fmt.Fprintf(m.opts.IO.ErrOut, format, args...)
	return err
}

func (m *mergeContext) infof(format string, args ...any) error {
	if !m.isTerminal {
		return nil
	}
	_, err := fmt.Fprintf(m.opts.IO.ErrOut, format, args...)
	return err
}

// 从 MergeOptions 创建一个新的 MergeContext。
func NewMergeContext(opts *MergeOptions) (*mergeContext, error) {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "number", "state", "title", "lastCommit", "mergeStateStatus", "headRefName", "baseRefName", "headRefOid", "isCrossRepository", "isInMergeQueue", "isMergeQueueEnabled"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return nil, err
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return nil, err
	}

	return &mergeContext{
		opts:               opts,
		pr:                 pr,
		cs:                 opts.IO.ColorScheme(),
		baseRepo:           baseRepo,
		isTerminal:         opts.IO.IsStdoutTTY(),
		httpClient:         httpClient,
		merged:             pr.State == MergeStateStatusMerged,
		deleteBranch:       opts.DeleteBranch,
		crossRepoPR:        pr.IsCrossRepository,
		autoMerge:          opts.AutoMergeEnable && !isImmediatelyMergeable(pr.MergeStateStatus),
		localBranchExists:  opts.CanDeleteLocalBranch && opts.GitClient.HasLocalBranch(context.Background(), pr.HeadRefName),
		mergeQueueRequired: pr.IsMergeQueueEnabled,
	}, nil
}

// 运行合并(merge)命令。
func mergeRun(opts *MergeOptions) error {
	ctx, err := NewMergeContext(opts)
	if err != nil {
		return err
	}

	if err := ctx.inMergeQueue(); err != nil {
		return err
	}

	// 禁用自动合并(auto merge)时无法执行任何后续操作
	if opts.AutoMergeDisable {
		return ctx.disableAutoMerge()
	}

	ctx.warnIfDiverged()

	if err := ctx.canMerge(); err != nil {
		return err
	}

	if err := ctx.merge(); err != nil {
		return err
	}

	if err := ctx.deleteLocalBranch(); err != nil {
		return err
	}

	if err := ctx.deleteRemoteBranch(); err != nil {
		return err
	}

	return nil
}

func mergeMethodSurvey(p shared.Prompt, baseRepo *api.Repository) (PullRequestMergeMethod, error) {
	type mergeOption struct {
		title  string
		method PullRequestMergeMethod
	}

	var mergeOpts []mergeOption
	if baseRepo.MergeCommitAllowed {
		opt := mergeOption{title: "创建合并提交(merge commit)", method: PullRequestMergeMethodMerge}
		mergeOpts = append(mergeOpts, opt)
	}
	if baseRepo.RebaseMergeAllowed {
		opt := mergeOption{title: "变基(rebase)并合并", method: PullRequestMergeMethodRebase}
		mergeOpts = append(mergeOpts, opt)
	}
	if baseRepo.SquashMergeAllowed {
		opt := mergeOption{title: "压缩(squash)并合并", method: PullRequestMergeMethodSquash}
		mergeOpts = append(mergeOpts, opt)
	}

	var surveyOpts []string
	for _, v := range mergeOpts {
		surveyOpts = append(surveyOpts, v.title)
	}

	result, err := p.Select("您想使用哪种合并(merge)方式?", "", surveyOpts)

	return mergeOpts[result].method, err
}

func deleteBranchSurvey(opts *MergeOptions, crossRepoPR, localBranchExists bool) (bool, error) {
	if !opts.IsDeleteBranchIndicated {
		var message string

		if opts.CanDeleteLocalBranch && localBranchExists {
			if crossRepoPR {
				message = "在本地删除该分支(branch)?"
			} else {
				message = "在本地和 GitHub 上删除该分支(branch)?"
			}
		} else if !crossRepoPR {
			message = "在 GitHub 上删除该分支(branch)?"
		}

		return opts.Prompter.Confirm(message, false)
	}

	return opts.DeleteBranch, nil
}

func confirmSurvey(p shared.Prompt, allowEditMsg bool) (shared.Action, error) {
	const (
		submitLabel            = "提交"
		editCommitSubjectLabel = "编辑提交主题(subject)"
		editCommitMsgLabel     = "编辑提交信息(message)"
		cancelLabel            = "取消"
	)

	options := []string{submitLabel}
	if allowEditMsg {
		options = append(options, editCommitSubjectLabel, editCommitMsgLabel)
	}
	options = append(options, cancelLabel)

	selected, err := p.Select("下一步做什么?", "", options)
	if err != nil {
		return shared.CancelAction, fmt.Errorf("无法显示提示: %w", err)
	}

	switch options[selected] {
	case submitLabel:
		return shared.SubmitAction, nil
	case editCommitSubjectLabel:
		return shared.EditCommitSubjectAction, nil
	case editCommitMsgLabel:
		return shared.EditCommitMessageAction, nil
	default:
		return shared.CancelAction, nil
	}
}

func confirmSubmission(client *http.Client, opts *MergeOptions, action shared.Action, payload *mergePayload) (bool, error) {
	var err error

	switch action {
	case shared.EditCommitMessageAction:
		if !payload.setCommitBody {
			_, payload.commitBody, err = getMergeText(client, payload.repo, payload.pullRequestID, payload.method)
			if err != nil {
				return false, err
			}
		}

		payload.commitBody, err = opts.Editor.Edit("*.md", payload.commitBody)
		if err != nil {
			return false, err
		}
		payload.setCommitBody = true

		return false, nil

	case shared.EditCommitSubjectAction:
		if payload.commitSubject == "" {
			payload.commitSubject, _, err = getMergeText(client, payload.repo, payload.pullRequestID, payload.method)
			if err != nil {
				return false, err
			}
		}

		payload.commitSubject, err = opts.Editor.Edit("*.md", payload.commitSubject)
		if err != nil {
			return false, err
		}

		return false, nil

	case shared.CancelAction:
		fmt.Fprintln(opts.IO.ErrOut, "已取消。")
		return false, cmdutil.CancelError

	case shared.SubmitAction:
		return true, nil

	default:
		return false, fmt.Errorf("无法确认: %w", err)
	}
}

type userEditor struct {
	io     *iostreams.IOStreams
	config func() (gh.Config, error)
}

func (e *userEditor) Edit(filename, startingText string) (string, error) {
	editorCommand, err := cmdutil.DetermineEditor(e.config)
	if err != nil {
		return "", err
	}

	return surveyext.Edit(editorCommand, filename, startingText, e.io.In, e.io.Out, e.io.ErrOut)
}

// blockedReason 将各种 MergeStateStatus GraphQL 取值转换为人类可读的原因
func blockedReason(status string, useAdmin bool) string {
	switch status {
	case MergeStateStatusBlocked:
		if useAdmin {
			return ""
		}
		return "基础分支(base branch)策略禁止该合并(merge)"
	case MergeStateStatusBehind:
		if useAdmin {
			return ""
		}
		return "源分支(head branch)未与基础分支(base branch)保持同步"
	case MergeStateStatusDirty:
		return "无法干净地创建合并提交(merge commit)"
	default:
		return ""
	}
}

func allowsAdminOverride(status string) bool {
	switch status {
	case MergeStateStatusBlocked, MergeStateStatusBehind:
		return true
	default:
		return false
	}
}

func remoteForMergeConflictResolution(baseRepo ghrepo.Interface, pr *api.PullRequest, opts *MergeOptions) *ghContext.Remote {
	if !mergeConflictStatus(pr.MergeStateStatus) || !opts.CanDeleteLocalBranch {
		return nil
	}
	remotes, err := opts.Remotes()
	if err != nil {
		return nil
	}
	remote, err := remotes.FindByRepo(baseRepo.RepoOwner(), baseRepo.RepoName())
	if err != nil {
		return nil
	}
	return remote
}

func mergeConflictStatus(status string) bool {
	return status == MergeStateStatusDirty
}

func isImmediatelyMergeable(status string) bool {
	switch status {
	case MergeStateStatusClean, MergeStateStatusHasHooks, MergeStateStatusUnstable:
		return true
	default:
		return false
	}
}
