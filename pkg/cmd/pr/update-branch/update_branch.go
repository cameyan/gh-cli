package update_branch

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	shared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type UpdateBranchOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	GitClient  *git.Client

	Finder shared.PRFinder

	SelectorArg string
	Rebase      bool
}

func NewCmdUpdateBranch(f *cmdutil.Factory, runF func(*UpdateBranchOptions) error) *cobra.Command {
	opts := &UpdateBranchOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
	}

	cmd := &cobra.Command{
		Use:   "update-branch [<number> | <url> | <branch>]",
		Short: "更新拉取请求(PR)的分支(branch)",
		Long: heredoc.Docf(`
			使用基础分支(base branch)的最新变更更新拉取请求(PR)的分支。

			不带参数时，将选择属于当前分支的拉取请求。

			默认行为是通过合并提交(merge commit)来更新(即把基础分支合并进 PR 的分支)。
			若要通过变基(rebase)到基础分支之上的方式来调和这些变更，应提供
			%[1]s--rebase%[1]s 选项。
		`, "`"),
		Example: heredoc.Doc(`
			$ gh pr update-branch 23
			$ gh pr update-branch 23 --rebase
			$ gh pr update-branch 23 --repo owner/repo
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 --repo flag 时必须提供位置参数")
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if runF != nil {
				return runF(opts)
			}

			return updateBranchRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Rebase, "rebase", false, "通过变基(rebase)到最新基础分支之上来更新 PR 分支")

	return cmd
}

func updateBranchRun(opts *UpdateBranchOptions) error {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "number", "headRefName", "headRefOid", "headRepositoryOwner", "mergeable"},
	}

	pr, repo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	if pr.Mergeable == api.PullRequestMergeableConflicting {
		fmt.Fprintf(opts.IO.ErrOut, "%s 由于存在冲突(conflict)，无法更新 PR 分支\n", cs.FailureIcon())
		return cmdutil.SilentError
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	headRef := pr.HeadRefName
	if pr.HeadRepositoryOwner.Login != repo.RepoOwner() {
		// 仅当 PR 的 head 分支位于另一个仓库(例如 fork)时，
		// 才在比较用 head ref 前加上 `owner:`。
		headRef = pr.HeadRepositoryOwner.Login + ":" + headRef
	}

	opts.IO.StartProgressIndicator()
	comparison, err := api.ComparePullRequestBaseBranchWith(apiClient, repo, pr.Number, headRef)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if comparison.BehindBy == 0 {
		// PR 分支并不落后于基础分支，因此它已是最新状态。
		fmt.Fprintf(opts.IO.Out, "%s PR 分支已是最新\n", cs.SuccessIcon())
		return nil
	}

	opts.IO.StartProgressIndicator()
	err = updatePullRequestBranch(apiClient, repo, pr.ID, pr.HeadRefOid, opts.Rebase)
	opts.IO.StopProgressIndicator()
	if err != nil {
		// TODO: 这是一种尽力而为的方式，并非健壮的 API 错误处理方法。
		if strings.Contains(err.Error(), "GraphQL: merge conflict between base and head (updatePullRequestBranch)") {
			fmt.Fprintf(opts.IO.ErrOut, "%s 由于存在冲突(conflict)，无法更新 PR 分支\n", cs.FailureIcon())
			return cmdutil.SilentError
		}
		return err
	}

	fmt.Fprintf(opts.IO.Out, "%s PR 分支已更新\n", cs.SuccessIcon())
	return nil
}

// updatePullRequestBranch 调用 GraphQL API 端点，使用其基础分支的最新变更
// 更新给定的 PR 分支。
func updatePullRequestBranch(apiClient *api.Client, repo ghrepo.Interface, pullRequestID string, expectedHeadOid string, rebase bool) error {
	updateMethod := githubv4.PullRequestBranchUpdateMethodMerge
	if rebase {
		updateMethod = githubv4.PullRequestBranchUpdateMethodRebase
	}

	params := githubv4.UpdatePullRequestBranchInput{
		PullRequestID:   pullRequestID,
		ExpectedHeadOid: (*githubv4.GitObjectID)(&expectedHeadOid),
		UpdateMethod:    &updateMethod,
	}

	return api.UpdatePullRequestBranch(apiClient, repo, params)
}
