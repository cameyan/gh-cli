package develop

import (
	ctx "context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DevelopOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Remotes    func() (context.Remotes, error)

	IssueNumber int
	Name        string
	BranchRepo  string
	BaseBranch  string
	Checkout    bool
	List        bool
	Worktree    string
}

func NewCmdDevelop(f *cmdutil.Factory, runF func(*DevelopOptions) error) *cobra.Command {
	opts := &DevelopOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		BaseRepo:   f.BaseRepo,
		Remotes:    f.Remotes,
	}

	cmd := &cobra.Command{
		Use:   "develop {<number> | <url>}",
		Short: "管理 issue 的关联分支",
		Long: heredoc.Docf(`
			管理 issue 的关联分支。

			使用 %[1]s--base%[1]s 选项时，新的开发分支将基于指定的远程分支创建。新分支会被配置为使用
			%[1]sgh pr create%[1]s 创建的 PR 的基础分支。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出 issue 123 的分支
			$ gh issue develop --list 123

			# 列出仓库 cli/cli 中 issue 123 的分支
			$ gh issue develop --list --repo cli/cli 123

			# 基于 my-feature 分支为 issue 123 创建分支
			$ gh issue develop 123 --base my-feature

			# 为 issue 123 创建分支并检出
			$ gh issue develop 123 --checkout

			# 为 issue 123 创建分支并在工作树中检出
			$ gh issue develop 123 --checkout --worktree /path/to/wt-feature

			# 在仓库 monalisa/cli 中为仓库 cli/cli 的 issue 123 创建分支
			$ gh issue develop 123 --repo cli/cli --branch-repo monalisa/cli
		`),
		Args: cmdutil.ExactArgs(1, "需要 issue 编号或 URL"),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// 这完全是为了不破坏已弃用的 issue-repo 旗标的临时方案。
			// 它将在不久的将来被移除，届时此临时方案也可一并移除。
			flags := cmd.Flags()
			if flags.Changed("issue-repo") {
				if flags.Changed("repo") {
					if flags.Changed("branch-repo") {
						return cmdutil.FlagErrorf("只能指定 `--repo` 和 `--branch-repo`")
					}
					branchRepo, _ := flags.GetString("repo")
					_ = flags.Set("branch-repo", branchRepo)
				}
				repo, _ := flags.GetString("issue-repo")
				_ = flags.Set("repo", repo)
			}
			if cmd.Parent() != nil {
				return cmd.Parent().PersistentPreRunE(cmd, args)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("worktree") && opts.Worktree == "" {
				return cmdutil.FlagErrorf("--worktree 不能为空")
			}

			issueNumber, baseRepo, err := shared.ParseIssueFromArg(args[0])
			if err != nil {
				return err
			}

			// 如果参数提供了基础仓库，则直接使用它。
			if baseRepo, present := baseRepo.Value(); present {
				opts.BaseRepo = func() (ghrepo.Interface, error) {
					return baseRepo, nil
				}
			} else {
				// 支持 `-R, --repo` 覆盖
				opts.BaseRepo = f.BaseRepo
			}

			opts.IssueNumber = issueNumber

			if err := cmdutil.MutuallyExclusive("`--list` 或 `--branch-repo` 中只能指定一个", opts.List, opts.BranchRepo != ""); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("`--list` 或 `--base` 中只能指定一个", opts.List, opts.BaseBranch != ""); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("`--list` 或 `--checkout` 中只能指定一个", opts.List, opts.Checkout); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("`--list` 或 `--name` 中只能指定一个", opts.List, opts.Name != ""); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("`--list` 或 `--worktree` 中只能指定一个", opts.List, opts.Worktree != ""); err != nil {
				return err
			}
			if opts.Worktree != "" && !opts.Checkout {
				return cmdutil.FlagErrorf("--worktree 需要 --checkout")
			}
			if opts.Worktree != "" {
				opts.Worktree, err = filepath.Abs(opts.Worktree)
				if err != nil {
					return err
				}
			}
			if runF != nil {
				return runF(opts)
			}
			return developRun(opts)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&opts.BranchRepo, "branch-repo", "", "创建新分支的仓库的名称或 URL")
	fl.StringVarP(&opts.BaseBranch, "base", "b", "", "新分支基础的远程分支名称")
	fl.BoolVarP(&opts.Checkout, "checkout", "c", false, "创建分支后检出它")
	fl.BoolVarP(&opts.List, "list", "l", false, "列出 issue 的关联分支")
	fl.StringVarP(&opts.Name, "name", "n", "", "待创建的分支名称")
	fl.StringVar(&opts.Worktree, "worktree", "", "将分支检出到给定 `path` 处的工作树")

	var issueRepoSelector string
	fl.StringVarP(&issueRepoSelector, "issue-repo", "i", "", "issue 所属仓库的名称或 URL")
	_ = cmd.Flags().MarkDeprecated("issue-repo", "请改用 `--repo`")

	return cmd
}

func developRun(opts *DevelopOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在获取 issue #%d", opts.IssueNumber))
	defer opts.IO.StopProgressIndicator()

	issue, err := shared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, []string{"id", "number"})
	if err != nil {
		return err
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	opts.IO.StartProgressIndicatorWithLabel("正在检查关联分支支持")
	err = api.CheckLinkedBranchFeature(apiClient, baseRepo.RepoHost())
	if err != nil {
		return err
	}
	opts.IO.StopProgressIndicator()

	if opts.List {
		return developRunList(opts, apiClient, baseRepo, issue)
	}
	return developRunCreate(opts, apiClient, baseRepo, issue)
}

func developRunCreate(opts *DevelopOptions, apiClient *api.Client, issueRepo ghrepo.Interface, issue *api.Issue) error {
	branchRepo := issueRepo
	if opts.BranchRepo != "" {
		var err error
		branchRepo, err = ghrepo.FromFullName(opts.BranchRepo)
		if err != nil {
			return err
		}
	}

	var worktreeTarget prShared.WorktreeTarget
	if opts.Worktree != "" {
		var err error
		worktreeTarget, err = prShared.ResolveWorktreeTarget(opts.GitClient, opts.Worktree)
		if err != nil {
			return err
		}
	}

	opts.IO.StartProgressIndicatorWithLabel("正在准备关联分支")
	defer opts.IO.StopProgressIndicator()

	branchName := ""
	reusedExisting := false
	if opts.Name != "" {
		opts.IO.StartProgressIndicatorWithLabel("正在检查现有关联分支")
		branches, err := api.ListLinkedBranches(apiClient, issueRepo, issue.Number)
		if err != nil {
			return err
		}
		branchName = findExistingLinkedBranchName(branches, branchRepo, opts.Name)
		reusedExisting = branchName != ""
	}

	repoID := ""
	branchID := ""
	baseValidated := false
	if opts.BaseBranch != "" {
		opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在验证基础分支 %q", opts.BaseBranch))
		foundRepoID, foundBranchID, err := api.FindRepoBranchID(apiClient, branchRepo, opts.BaseBranch)
		if err != nil {
			return err
		}
		repoID = foundRepoID
		branchID = foundBranchID
		baseValidated = true
	}

	if branchName == "" {
		if !baseValidated {
			opts.IO.StartProgressIndicatorWithLabel("正在解析基础分支")
			foundRepoID, foundBranchID, err := api.FindRepoBranchID(apiClient, branchRepo, opts.BaseBranch)
			if err != nil {
				return err
			}
			repoID = foundRepoID
			branchID = foundBranchID
		}

		opts.IO.StartProgressIndicatorWithLabel("正在创建关联分支")
		createdBranchName, err := api.CreateLinkedBranch(apiClient, branchRepo.RepoHost(), repoID, issue.ID, branchID, opts.Name)
		if err != nil {
			return err
		}
		branchName = createdBranchName
	}

	if branchName == "" {
		return fmt.Errorf("创建关联分支失败: API 返回了空分支名称")
	}

	opts.IO.StopProgressIndicator()

	if reusedExisting && opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.ErrOut, "正在使用现有关联分支 %q\n", branchName)
	}

	// 记住在创建 PR 时要作为目标的分支。
	if opts.BaseBranch != "" {
		if err := opts.GitClient.SetBranchConfig(ctx.Background(), branchName, git.MergeBaseConfig, opts.BaseBranch); err != nil {
			return err
		}
	}

	fmt.Fprintf(opts.IO.Out, "%s/%s/tree/%s\n", branchRepo.RepoHost(), ghrepo.FullName(branchRepo), branchName)

	return checkoutBranch(opts, branchRepo, branchName, worktreeTarget)
}

func findExistingLinkedBranchName(branches []api.LinkedBranch, branchRepo ghrepo.Interface, branchName string) string {
	for _, branch := range branches {
		if branch.BranchName != branchName {
			continue
		}
		linkedRepo, err := linkedBranchRepoFromURL(branch.URL)
		if err != nil {
			continue
		}
		if ghrepo.IsSame(linkedRepo, branchRepo) {
			return branch.BranchName
		}
	}
	return ""
}

func linkedBranchRepoFromURL(branchURL string) (ghrepo.Interface, error) {
	u, err := url.Parse(branchURL)
	if err != nil {
		return nil, err
	}
	pathParts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3)
	if len(pathParts) < 2 {
		return nil, fmt.Errorf("无效的关联分支 URL: %q", branchURL)
	}
	u.Path = "/" + strings.Join(pathParts[0:2], "/")
	return ghrepo.FromURL(u)
}

func developRunList(opts *DevelopOptions, apiClient *api.Client, issueRepo ghrepo.Interface, issue *api.Issue) error {
	opts.IO.StartProgressIndicatorWithLabel("正在获取关联分支")
	defer opts.IO.StopProgressIndicator()

	branches, err := api.ListLinkedBranches(apiClient, issueRepo, issue.Number)
	if err != nil {
		return err
	}
	opts.IO.StopProgressIndicator()

	if len(branches) == 0 {
		return cmdutil.NewNoResultsError(fmt.Sprintf("未找到 %s#%d 的关联分支", ghrepo.FullName(issueRepo), issue.Number))
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.Out, "\n显示 %s#%d 的关联分支\n\n", ghrepo.FullName(issueRepo), issue.Number)
	}

	printLinkedBranches(opts.IO, branches)

	return nil
}

func printLinkedBranches(io *iostreams.IOStreams, branches []api.LinkedBranch) {
	cs := io.ColorScheme()
	table := tableprinter.New(io, tableprinter.WithHeader("分支", "URL"))
	for _, branch := range branches {
		table.AddField(branch.BranchName, tableprinter.WithColor(cs.ColorFromString("cyan")))
		table.AddField(branch.URL)
		table.EndRow()
	}
	_ = table.Render()
}

func checkoutBranch(opts *DevelopOptions, branchRepo ghrepo.Interface, checkoutBranch string, worktreeTarget prShared.WorktreeTarget) (err error) {
	remotes, err := opts.Remotes()
	if err != nil {
		// 如果用户指定了要检出的分支且未找到远程仓库，
		// 则显示错误。否则静默退出，很可能该命令并非
		// 从 git 目录内运行。
		if opts.Checkout {
			return err
		} else {
			return nil
		}
	}

	baseRemote, err := remotes.FindByRepo(branchRepo.RepoOwner(), branchRepo.RepoName())
	if err != nil {
		// 如果用户指定了要检出的分支，且没有与基础仓库匹配的
		// 远程仓库，则显示错误。否则静默退出。
		if opts.Checkout {
			return err
		} else {
			return nil
		}
	}

	gc := opts.GitClient

	if err := gc.Fetch(ctx.Background(), baseRemote.Name, fmt.Sprintf("+refs/heads/%[1]s:refs/remotes/%[2]s/%[1]s", checkoutBranch, baseRemote.Name)); err != nil {
		return err
	}

	if !opts.Checkout {
		return nil
	}

	if opts.Worktree != "" {
		commands, branchExists := prShared.WorktreeCheckoutCommands(
			gc,
			worktreeTarget,
			checkoutBranch,
			fmt.Sprintf("%s/%s", baseRemote.Name, checkoutBranch),
		)
		if err := runGitCommands(gc, commands); err != nil {
			return err
		}
		if branchExists {
			if err := gc.Pull(ctx.Background(), baseRemote.Name, checkoutBranch, git.WithRepoDir(opts.Worktree)); err != nil {
				_, _ = fmt.Fprintf(opts.IO.ErrOut, "%s 警告: 无法快进到: %q\n", opts.IO.ColorScheme().WarningIcon(), checkoutBranch)
			}
		}
		return nil
	}

	if gc.HasLocalBranch(ctx.Background(), checkoutBranch) {
		if err := gc.CheckoutBranch(ctx.Background(), checkoutBranch); err != nil {
			return err
		}

		if err := gc.Pull(ctx.Background(), baseRemote.Name, checkoutBranch); err != nil {
			_, _ = fmt.Fprintf(opts.IO.ErrOut, "%s 警告: 无法快进到: %q\n", opts.IO.ColorScheme().WarningIcon(), checkoutBranch)
		}
	} else {
		if err := gc.CheckoutNewBranch(ctx.Background(), baseRemote.Name, checkoutBranch); err != nil {
			return err
		}
	}

	return nil
}

func runGitCommands(client *git.Client, commands [][]string) error {
	for _, args := range commands {
		cmd, err := client.Command(ctx.Background(), args...)
		if err != nil {
			return err
		}
		if _, err := cmd.Output(); err != nil {
			return err
		}
	}
	return nil
}
