package sync

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/context"
	gitpkg "github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	notFastForwardErrorMessage     = "Update is not a fast forward"
	branchDoesNotExistErrorMessage = "Reference does not exist"
)

type SyncOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Remotes    func() (context.Remotes, error)
	Git        gitClient
	DestArg    string
	SrcArg     string
	Branch     string
	Force      bool
}

func NewCmdSync(f *cmdutil.Factory, runF func(*SyncOptions) error) *cobra.Command {
	opts := SyncOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
		BaseRepo:   f.BaseRepo,
		Remotes:    f.Remotes,
		Git:        &gitExecuter{client: f.GitClient},
	}

	cmd := &cobra.Command{
		Use:   "sync [<destination-repository>]",
		Short: "同步仓库",
		Long: heredoc.Docf(`
			从源仓库同步目标仓库。同步使用源仓库的默认分支更新目标仓库上匹配的分支，
			使两者一致。除指定 %[1]s--force%[1]s 标志外，将使用快进(fast forward)更新；
			指定该标志时，将使用硬重置(hard reset)同步两个分支。

			未提供参数时，本地仓库将被选为目标仓库。

			默认情况下，源仓库是目标仓库的父仓库。
			可以使用 %[1]s--source%[1]s 标志覆盖此行为。
		`, "`"),
		Example: heredoc.Doc(`
			# 从远程父仓库同步本地仓库
			$ gh repo sync

			# 在特定分支上从远程父仓库同步本地仓库
			$ gh repo sync --branch v1

			# 从其父仓库同步远程复刻(fork)
			$ gh repo sync owner/cli-fork

			# 从另一个远程仓库同步远程仓库
			$ gh repo sync owner/repo --source owner2/repo2
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.DestArg = args[0]
			}
			if runF != nil {
				return runF(&opts)
			}
			return syncRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.SrcArg, "source", "s", "", "源仓库")
	cmd.Flags().StringVarP(&opts.Branch, "branch", "b", "", "要同步的分支(默认为 [默认分支])")
	cmd.Flags().BoolVarP(&opts.Force, "force", "", false, "硬重置目标仓库的分支以匹配源仓库")
	return cmd
}

func syncRun(opts *SyncOptions) error {
	if opts.DestArg == "" {
		return syncLocalRepo(opts)
	} else {
		return syncRemoteRepo(opts)
	}
}

func syncLocalRepo(opts *SyncOptions) error {
	var srcRepo ghrepo.Interface

	if opts.SrcArg != "" {
		var err error
		srcRepo, err = ghrepo.FromFullName(opts.SrcArg)
		if err != nil {
			return err
		}
	} else {
		var err error
		srcRepo, err = opts.BaseRepo()
		if err != nil {
			return err
		}
	}

	// 查找与 srcRepo 匹配的远程(remote)
	var remote string
	remotes, err := opts.Remotes()
	if err != nil {
		return err
	}
	if r, err := remotes.FindByRepo(srcRepo.RepoOwner(), srcRepo.RepoName()); err == nil {
		remote = r.Name
	} else {
		return fmt.Errorf("找不到 %s 对应的远程(remote)", ghrepo.FullName(srcRepo))
	}

	if opts.Branch == "" {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return err
		}
		apiClient := api.NewClientFromHTTP(httpClient)
		opts.IO.StartProgressIndicator()
		opts.Branch, err = api.RepoDefaultBranch(apiClient, srcRepo)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}
	}

	// git fetch 可能需要用户输入，因此要在启动进度指示器之前执行。
	if err := opts.Git.Fetch(remote, fmt.Sprintf("refs/heads/%s", opts.Branch)); err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	err = executeLocalRepoSync(srcRepo, remote, opts)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if errors.Is(err, divergingError) {
			return fmt.Errorf("无法同步，因为存在分歧的变更；使用 `--force` 覆盖目标分支")
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "%s 已从 \"%s\" 同步 \"%s\" 分支到本地仓库\n",
			cs.SuccessIcon(),
			opts.Branch,
			ghrepo.FullName(srcRepo))
	}

	return nil
}

func syncRemoteRepo(opts *SyncOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	var destRepo, srcRepo ghrepo.Interface

	destRepo, err = ghrepo.FromFullName(opts.DestArg)
	if err != nil {
		return err
	}

	if opts.SrcArg != "" {
		srcRepo, err = ghrepo.FromFullName(opts.SrcArg)
		if err != nil {
			return err
		}
	}

	if srcRepo != nil && destRepo.RepoHost() != srcRepo.RepoHost() {
		return fmt.Errorf("无法同步来自不同主机的仓库")
	}

	opts.IO.StartProgressIndicator()
	baseBranchLabel, err := executeRemoteRepoSync(apiClient, destRepo, srcRepo, opts)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if errors.Is(err, divergingError) {
			return fmt.Errorf("无法同步，因为存在分歧的变更；使用 `--force` 覆盖目标分支")
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		branchName := opts.Branch
		if _, after, ok := strings.Cut(baseBranchLabel, ":"); ok {
			branchName = after
		}
		fmt.Fprintf(opts.IO.Out, "%s 已从 \"%s\" 同步 \"%s:%s\" 分支\n",
			cs.SuccessIcon(),
			destRepo.RepoOwner(),
			branchName,
			baseBranchLabel)
	}

	return nil
}

var divergingError = errors.New("存在分歧的变更")

func executeLocalRepoSync(srcRepo ghrepo.Interface, remote string, opts *SyncOptions) error {
	git := opts.Git
	branch := opts.Branch
	useForce := opts.Force

	hasLocalBranch := git.HasLocalBranch(branch)
	if hasLocalBranch {
		fastForward, err := git.IsAncestor(branch, "FETCH_HEAD")
		if err != nil {
			return err
		}
		if !fastForward && !useForce {
			return divergingError
		}
		if fastForward && useForce {
			useForce = false
		}
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil && !errors.Is(err, gitpkg.ErrNotOnAnyBranch) {
		return err
	}
	if currentBranch == branch {
		if isDirty, err := git.IsDirty(); err == nil && isDirty {
			return fmt.Errorf("拒绝同步，因为存在未提交/未跟踪的本地变更\n提示: 重试同步前使用 `git stash --all`，之后运行 `git stash pop`")
		} else if err != nil {
			return err
		}
		if useForce {
			if err := git.ResetHard("FETCH_HEAD"); err != nil {
				return err
			}
		} else {
			if err := git.MergeFastForward("FETCH_HEAD"); err != nil {
				return err
			}
		}
	} else {
		if hasLocalBranch {
			if err := git.UpdateBranch(branch, "FETCH_HEAD"); err != nil {
				worktrees, worktreeErr := git.Worktrees()
				if worktreeErr != nil {
					return fmt.Errorf("无法更新分支 %q: %w；同时也无法获取工作树(worktree): %w", branch, err, worktreeErr)
				}
				if worktree := gitpkg.WorktreeForBranch(worktrees, branch); worktree != nil {
					if worktree.Prunable {
						return fmt.Errorf("无法同步 %q，因为过期的工作树(worktree)元数据仍将其关联到 %s\n提示: 运行 `git worktree prune` 后重试", branch, worktree.Path)
					}
					return fmt.Errorf("无法同步 %q，因为它已在 %s 处的另一个工作树(worktree)中检出\n提示: 请改为在该工作树中运行 `gh repo sync`", branch, worktree.Path)
				}
				return err
			}
		} else {
			if err := git.CreateBranch(branch, "FETCH_HEAD", fmt.Sprintf("%s/%s", remote, branch)); err != nil {
				return err
			}
		}
	}

	return nil
}

// ExecuteRemoteRepoSync 将通过多个步骤同步源仓库和目标仓库。
// 首先尝试使用 merge-upstream API 端点。若因合并冲突或未知的合并问题而失败，
// 则回退到低层级的 git 引用(references) API 端点。
// 之所以需要回退，是为了更好地处理这些错误情况。git references API
// 端点允许我们同步不兼容快进(fast-forward)合并的仓库。此外，
// git references API 端点会给出更详细的同步失败原因说明。
// 除非指定 --force 标志，否则不会执行非快进合并。
func executeRemoteRepoSync(client *api.Client, destRepo, srcRepo ghrepo.Interface, opts *SyncOptions) (string, error) {
	branchName := opts.Branch
	if branchName == "" {
		var err error
		branchName, err = api.RepoDefaultBranch(client, destRepo)
		if err != nil {
			return "", err
		}
	}

	if baseBranch, err := triggerUpstreamMerge(client, destRepo, branchName); err == nil {
		return baseBranch, nil
	} else if _, ok := errors.AsType[upstreamMergeErr](err); !ok {
		return "", err
	}

	if srcRepo == nil {
		var err error
		srcRepo, err = api.RepoParent(client, destRepo)
		if err != nil {
			return "", err
		}
		if srcRepo == nil {
			return "", fmt.Errorf("无法确定 %s 的源仓库，因为该仓库不是复刻(fork)", ghrepo.FullName(destRepo))
		}
	}

	commit, err := latestCommit(client, srcRepo, branchName)
	if err != nil {
		return "", err
	}

	// 使用字符串比较来判定 API 端点返回的错误是一种脆弱的方式，
	// 但遗憾的是 API 会因多种原因返回 422，因此我们必须解读消息，
	// 为用户提供更好的错误提示。
	err = syncFork(client, destRepo, branchName, commit.Object.SHA, opts.Force)
	if err != nil {
		if httpErr, ok := errors.AsType[api.HTTPError](err); ok {
			switch httpErr.Message {
			case notFastForwardErrorMessage:
				return "", divergingError
			case branchDoesNotExistErrorMessage:
				return "", fmt.Errorf("%s 分支在仓库 %s 上不存在", branchName, ghrepo.FullName(destRepo))
			}
		}
		return "", err
	}

	return fmt.Sprintf("%s:%s", srcRepo.RepoOwner(), branchName), nil
}
