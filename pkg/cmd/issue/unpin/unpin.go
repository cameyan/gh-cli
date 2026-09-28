package unpin

import (
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type UnpinOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	IssueNumber int
}

func NewCmdUnpin(f *cmdutil.Factory, runF func(*UnpinOptions) error) *cobra.Command {
	opts := &UnpinOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		BaseRepo:   f.BaseRepo,
	}

	cmd := &cobra.Command{
		Use:   "unpin {<number> | <url>}",
		Short: "取消置顶议题",
		Long: heredoc.Doc(`
			从仓库取消置顶议题。

			可以通过议题编号或 URL 指定议题。
		`),
		Example: heredoc.Doc(`
			# 从当前仓库取消置顶议题
			$ gh issue unpin 23

			# 通过 URL 取消置顶议题
			$ gh issue unpin https://github.com/owner/repo/issues/23

			# 从指定仓库取消置顶议题
			$ gh issue unpin 23 --repo owner/repo
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if runF != nil {
				return runF(opts)
			}

			return unpinRun(opts)
		},
	}

	return cmd
}

func unpinRun(opts *UnpinOptions) error {
	cs := opts.IO.ColorScheme()

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	issue, err := shared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, []string{"id", "number", "title", "isPinned"})
	if err != nil {
		return err
	}

	if !issue.IsPinned {
		fmt.Fprintf(opts.IO.ErrOut, "%s 议题 %s#%d (%s) 未置顶\n", cs.Yellow("!"), ghrepo.FullName(baseRepo), issue.Number, issue.Title)
		return nil
	}

	err = unpinIssue(httpClient, baseRepo, issue)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已取消置顶议题 %s#%d (%s)\n", cs.SuccessIconWithColor(cs.Red), ghrepo.FullName(baseRepo), issue.Number, issue.Title)

	return nil
}

func unpinIssue(httpClient *http.Client, repo ghrepo.Interface, issue *api.Issue) error {
	var mutation struct {
		UnpinIssue struct {
			Issue struct {
				ID githubv4.ID
			}
		} `graphql:"unpinIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.UnpinIssueInput{
			IssueID: issue.ID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)

	return gql.Mutate(repo.RepoHost(), "IssueUnpin", &mutation, variables)
}
