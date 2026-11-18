package checkout

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	cliContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type CheckoutOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	Remotes    func() (cliContext.Remotes, error)
	Branch     func() (string, error)

	PRResolver PRResolver

	RecurseSubmodules bool
	Force             bool
	Detach            bool
	BranchName        string
	Worktree          string
}

func NewCmdCheckout(f *cmdutil.Factory, runF func(*CheckoutOptions) error) *cobra.Command {
	opts := &CheckoutOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
		Remotes:    f.Remotes,
		Branch:     f.Branch,
	}

	cmd := &cobra.Command{
		Use:   "checkout [<number> | <url> | <branch>]",
		Short: "在 git 中检出(check out)一个拉取请求(PR)",
		Example: heredoc.Doc(`
			# 从最近 10 个 PR 中交互式选择一个进行检出(check out)
			$ gh pr checkout

			# 检出(check out)指定的 PR
			$ gh pr checkout 32
			$ gh pr checkout https://github.com/OWNER/REPO/pull/32
			$ gh pr checkout feature
			$ gh pr checkout 32 --branch feature --worktree /path/to/wt-feature
		`),
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"co"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("worktree") && opts.Worktree == "" {
				return cmdutil.FlagErrorf("--worktree 不能为空")
			}

			if len(args) > 0 {
				opts.PRResolver = &specificPRResolver{
					prFinder: shared.NewFinder(f),
					selector: args[0],
				}
			} else if opts.IO.CanPrompt() {
				baseRepo, err := f.BaseRepo()
				if err != nil {
					return err
				}

				httpClient, err := f.HttpClient()
				if err != nil {
					return err
				}

				opts.PRResolver = &promptingPRResolver{
					io:       opts.IO,
					prompter: f.Prompter,
					prLister: shared.NewLister(httpClient),
					baseRepo: baseRepo,
				}
			} else {
				return cmdutil.FlagErrorf("非交互式运行时必须提供拉取请求(PR)编号(number)、URL 或分支(branch)")
			}

			if runF != nil {
				return runF(opts)
			}
			return checkoutRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.RecurseSubmodules, "recurse-submodules", "", false, "检出(check out)后更新所有子模块(submodule)")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "将已有本地分支(branch)重置为拉取请求(PR)的最新状态")
	cmd.Flags().BoolVarP(&opts.Detach, "detach", "", false, "以分离 HEAD(detached HEAD)方式检出(check out) PR")
	cmd.Flags().StringVarP(&opts.BranchName, "branch", "b", "", "要使用的本地分支(branch)名 (默认 [源分支(head branch)的名称])")
	cmd.Flags().StringVar(&opts.Worktree, "worktree", "", "将拉取请求(PR)检出到指定 `path` 处的工作树(worktree)")

	return cmd
}

func checkoutRun(opts *CheckoutOptions) error {
	pr, baseRepo, err := opts.PRResolver.Resolve()
	if err != nil {
		return err
	}

	var reuseWorktree bool
	if opts.Worktree != "" {
		target, err := shared.ResolveWorktreeTarget(opts.GitClient, opts.Worktree)
		if err != nil {
			return err
		}
		reuseWorktree = target.Reuse
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	protocol := cfg.GitProtocol(baseRepo.RepoHost()).Value

	remotes, err := opts.Remotes()
	if err != nil {
		return err
	}
	baseRemote, _ := remotes.FindByRepo(baseRepo.RepoOwner(), baseRepo.RepoName())
	baseURLOrName := ghrepo.FormatRemoteURL(baseRepo, protocol)
	if baseRemote != nil {
		baseURLOrName = baseRemote.Name
	}

	headRemote := baseRemote
	if pr.HeadRepository == nil {
		headRemote = nil
	} else if pr.IsCrossRepository {
		headRemote, _ = remotes.FindByRepo(pr.HeadRepositoryOwner.Login, pr.HeadRepository.Name)
	}

	if strings.HasPrefix(pr.HeadRefName, "-") {
		return fmt.Errorf("无效的分支(branch)名: %q", pr.HeadRefName)
	}

	var cmdQueue [][]string

	if headRemote != nil {
		cmdQueue = append(cmdQueue, cmdsForExistingRemote(headRemote, pr, opts, reuseWorktree)...)
	} else {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return err
		}
		apiClient := api.NewClientFromHTTP(httpClient)

		defaultBranch, err := api.RepoDefaultBranch(apiClient, baseRepo)
		if err != nil {
			return err
		}
		cmdQueue = append(cmdQueue, cmdsForMissingRemote(pr, baseURLOrName, baseRepo.RepoHost(), defaultBranch, protocol, opts, reuseWorktree)...)
	}

	if opts.RecurseSubmodules {
		// 在检出到工作树(worktree)时，将子模块(submodule)命令在该工作树内执行，
		// 以便初始化它自己的子模块(而不是主工作树的子模块)。
		var prefix []string
		if opts.Worktree != "" {
			prefix = []string{"-C", opts.Worktree}
		}
		cmdQueue = append(cmdQueue, slices.Concat(prefix, []string{"submodule", "sync", "--recursive"}))
		cmdQueue = append(cmdQueue, slices.Concat(prefix, []string{"submodule", "update", "--init", "--recursive"}))
	}

	// 注意: 尽管我们很可能是从源分支(head)获取，但实践中 PR 检出(checkout)只能指向一个主机，
	// 且我们知道 baseRepo 必然已填充。
	err = executeCmds(opts.GitClient, git.CredentialPatternFromHost(baseRepo.RepoHost()), cmdQueue)
	if err != nil {
		return err
	}

	if opts.Worktree != "" && opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.ErrOut, "%s 已在工作树(worktree) %s 中检出(check out) PR #%d\n", cs.SuccessIcon(), pr.Number, opts.Worktree)
		fmt.Fprintf(opts.IO.ErrOut, "  开始工作: cd %s\n", opts.Worktree)
	}

	return nil
}

func cmdsForExistingRemote(remote *cliContext.Remote, pr *api.PullRequest, opts *CheckoutOptions, reuseWorktree bool) [][]string {
	var cmds [][]string
	remoteBranch := fmt.Sprintf("%s/%s", remote.Name, pr.HeadRefName)

	refSpec := fmt.Sprintf("+refs/heads/%s", pr.HeadRefName)
	if !opts.Detach {
		refSpec += fmt.Sprintf(":refs/remotes/%s", remoteBranch)
	}

	localBranch := pr.HeadRefName
	if opts.BranchName != "" {
		localBranch = opts.BranchName
	}

	remoteBranchRef := fmt.Sprintf("refs/remotes/%s", remoteBranch)
	fetchCmd := []string{"fetch", remote.Name, refSpec, "--no-tags"}

	if opts.Detach {
		return append(cmds, detachCmds(fetchCmd, opts.Worktree, reuseWorktree)...)
	}

	cmds = append(cmds, fetchCmd)

	switch {
	case opts.Worktree != "":
		worktreeCmds, branchExists := shared.WorktreeCheckoutCommands(
			opts.GitClient,
			shared.WorktreeTarget{Path: opts.Worktree, Reuse: reuseWorktree},
			localBranch,
			remoteBranch,
		)
		cmds = append(cmds, worktreeCmds...)
		if branchExists {
			cmds = append(cmds, syncBranchCmds(opts.Worktree, remoteBranchRef, opts.Force)...)
		}
	case opts.GitClient.HasLocalBranch(context.Background(), localBranch):
		cmds = append(cmds, []string{"checkout", localBranch})
		cmds = append(cmds, syncBranchCmds("", remoteBranchRef, opts.Force)...)
	default:
		cmds = append(cmds, []string{"checkout", "-b", localBranch, "--track", remoteBranch})
	}

	return cmds
}

func cmdsForMissingRemote(pr *api.PullRequest, baseURLOrName, repoHost, defaultBranch, protocol string, opts *CheckoutOptions, reuseWorktree bool) [][]string {
	var cmds [][]string
	ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)

	if opts.Detach {
		fetchCmd := []string{"fetch", baseURLOrName, ref, "--no-tags"}
		return detachCmds(fetchCmd, opts.Worktree, reuseWorktree)
	}

	localBranch := pr.HeadRefName
	if opts.BranchName != "" {
		localBranch = opts.BranchName
	} else if pr.HeadRefName == defaultBranch {
		// 避免将新分支(branch)命名为与默认分支相同的名称
		localBranch = fmt.Sprintf("%s/%s", pr.HeadRepositoryOwner.Login, localBranch)
	}

	currentBranch, _ := opts.Branch()
	if opts.Worktree != "" {
		if reuseWorktree {
			// FETCH_HEAD is per-worktree, and git refuses to update a branch via
			// refspec while it is checked out, so fetch to FETCH_HEAD inside the worktree.
			cmds = append(cmds, []string{"-C", opts.Worktree, "fetch", baseURLOrName, ref, "--no-tags"})
			if opts.GitClient.HasLocalBranch(context.Background(), localBranch) {
				cmds = append(cmds, []string{"-C", opts.Worktree, "checkout", localBranch})
				cmds = append(cmds, syncBranchCmds(opts.Worktree, "FETCH_HEAD", opts.Force)...)
			} else {
				cmds = append(cmds, []string{"-C", opts.Worktree, "checkout", "-b", localBranch, "FETCH_HEAD"})
			}
		} else {
			fetchCmd := []string{"fetch", baseURLOrName, fmt.Sprintf("%s:%s", ref, localBranch), "--no-tags"}
			if opts.Force {
				fetchCmd = append(fetchCmd, "--force")
			}
			cmds = append(cmds, fetchCmd)
			cmds = append(cmds, []string{"worktree", "add", "--", opts.Worktree, localBranch})
		}
	} else if localBranch == currentBranch {
		// PR 的源分支(head)与当前已检出的分支(branch)匹配
		cmds = append(cmds, []string{"fetch", baseURLOrName, ref, "--no-tags"})
		cmds = append(cmds, syncBranchCmds("", "FETCH_HEAD", opts.Force)...)
	} else {
		// TODO: 检查是否为非快进(non-fast-forward)，并建议使用 `--force`
		fetchCmd := []string{"fetch", baseURLOrName, fmt.Sprintf("%s:%s", ref, localBranch), "--no-tags"}
		if opts.Force {
			fetchCmd = append(fetchCmd, "--force")
		}
		cmds = append(cmds, fetchCmd)
		cmds = append(cmds, []string{"checkout", localBranch})
	}

	remote := baseURLOrName
	mergeRef := ref
	if pr.MaintainerCanModify && pr.HeadRepository != nil {
		headRepo := ghrepo.NewWithHost(pr.HeadRepositoryOwner.Login, pr.HeadRepository.Name, repoHost)
		remote = ghrepo.FormatRemoteURL(headRepo, protocol)
		mergeRef = fmt.Sprintf("refs/heads/%s", pr.HeadRefName)
	}
	if missingMergeConfigForBranch(opts.GitClient, localBranch) {
		// .remote 是 `git pull` 工作所必需的
		// .pushRemote 是 `git push` 工作所必需的(如果用户设置了 `remote.pushDefault`)
		// 参见 https://git-scm.com/docs/git-config#Documentation/git-config.txt-branchltnamegtremote
		cmds = append(cmds, []string{"config", fmt.Sprintf("branch.%s.remote", localBranch), remote})
		cmds = append(cmds, []string{"config", fmt.Sprintf("branch.%s.pushRemote", localBranch), remote})
		cmds = append(cmds, []string{"config", fmt.Sprintf("branch.%s.merge", localBranch), mergeRef})
	}

	return cmds
}

func missingMergeConfigForBranch(client *git.Client, b string) bool {
	mc, err := client.Config(context.Background(), fmt.Sprintf("branch.%s.merge", b))
	return err != nil || mc == ""
}

// detachCmds 返回分离 HEAD(detached)检出的命令。在复用已关联的
// 工作树(worktree)时，FETCH_HEAD 必须写入其中(它是 per-worktree 的)，
// 因此 fetch 以 -C <path> 运行。
func detachCmds(fetchCmd []string, worktree string, reuseWorktree bool) [][]string {
	if worktree == "" {
		return [][]string{
			fetchCmd,
			{"checkout", "--detach", "FETCH_HEAD"},
		}
	}

	if reuseWorktree {
		return [][]string{
			append([]string{"-C", worktree}, fetchCmd...),
			{"-C", worktree, "checkout", "--detach", "FETCH_HEAD"},
		}
	}
	return [][]string{
		fetchCmd,
		{"worktree", "add", "--detach", "--", worktree, "FETCH_HEAD"},
	}
}

// syncBranchCmds 将分支(branch)同步到 ref: force 时使用硬重置(hard reset)，
// 否则使用仅快进(fast-forward-only)合并。非空路径时在该路径下执行命令。
func syncBranchCmds(path, ref string, force bool) [][]string {
	var prefix []string
	if path != "" {
		prefix = []string{"-C", path}
	}
	if force {
		return [][]string{append(prefix, "reset", "--hard", ref)}
	}
	// TODO: 检查是否为非快进(non-fast-forward)，并建议使用 `--force`
	return [][]string{append(prefix, "merge", "--ff-only", ref)}
}

func executeCmds(client *git.Client, credentialPattern git.CredentialPattern, cmdQueue [][]string) error {
	for _, args := range cmdQueue {
		// 确定 git 子命令，跳过任何 -C <path> 前缀。
		subCmd := args[0]
		if len(args) >= 3 && args[0] == "-C" {
			subCmd = args[2]
		}

		var err error
		var cmd *git.Command
		switch subCmd {
		case "submodule":
			cmd, err = authenticatedCommand(client, credentialPattern, args)
		case "fetch":
			cmd, err = authenticatedCommand(client, git.AllMatchingCredentialsPattern, args)
		default:
			cmd, err = client.Command(context.Background(), args...)
		}
		if err != nil {
			return err
		}
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

// authenticatedCommand 构建一个经过认证的 git 命令，透明地处理开头的
// -C <path> 前缀。AuthenticatedCommand 会在所有参数之前前置凭证助手(credential-helper)
// flags，因此 -C 前缀会错位；这里改为剥离该前缀并将其应用为 cmd.Dir。
func authenticatedCommand(client *git.Client, credentialPattern git.CredentialPattern, args []string) (*git.Command, error) {
	if args[0] == "-C" {
		cmd, err := client.AuthenticatedCommand(context.Background(), credentialPattern, args[2:]...)
		if err != nil {
			return nil, err
		}
		cmd.Dir = args[1]
		return cmd, nil
	}
	return client.AuthenticatedCommand(context.Background(), credentialPattern, args...)
}

type PRResolver interface {
	Resolve() (*api.PullRequest, ghrepo.Interface, error)
}

type specificPRResolver struct {
	prFinder shared.PRFinder
	selector string
}

func (r *specificPRResolver) Resolve() (*api.PullRequest, ghrepo.Interface, error) {
	pr, baseRepo, err := r.prFinder.Find(shared.FindOptions{
		Selector: r.selector,
		Fields: []string{
			"number",
			"headRefName",
			"headRepository",
			"headRepositoryOwner",
			"isCrossRepository",
			"maintainerCanModify",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return pr, baseRepo, nil
}

type promptingPRResolver struct {
	io       *iostreams.IOStreams
	prompter shared.Prompt

	prLister shared.PRLister

	baseRepo ghrepo.Interface
}

func (r *promptingPRResolver) Resolve() (*api.PullRequest, ghrepo.Interface, error) {
	r.io.StartProgressIndicator()
	listResult, err := r.prLister.List(shared.ListOptions{
		BaseRepo: r.baseRepo,
		State:    "open",
		Fields: []string{
			"number",
			"title",
			"state",
			"isDraft",

			"headRefName",
			"headRepository",
			"headRepositoryOwner",
			"isCrossRepository",
			"maintainerCanModify",
		},
		LimitResults: 10})
	r.io.StopProgressIndicator()
	if err != nil {
		return nil, nil, err
	}
	if len(listResult.PullRequests) == 0 {
		return nil, nil, shared.ListNoResults(ghrepo.FullName(r.baseRepo), "pull request", false)
	}

	candidates := []string{}
	for _, pr := range listResult.PullRequests {
		candidates = append(candidates, fmt.Sprintf("%d\t%s %s [%s]",
			pr.Number,
			shared.PrStateWithDraft(&pr),
			text.RemoveExcessiveWhitespace(pr.Title),
			pr.HeadLabel(),
		))
	}

	selected, err := r.prompter.Select("选择一个拉取请求(PR)", "", candidates)
	if err != nil {
		return nil, nil, err
	}

	return &listResult.PullRequests[selected], r.baseRepo, nil
}
