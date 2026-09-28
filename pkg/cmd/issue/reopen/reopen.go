package reopen

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type ReopenOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	IssueNumber int
	Comment     string
}

func NewCmdReopen(f *cmdutil.Factory, runF func(*ReopenOptions) error) *cobra.Command {
	opts := &ReopenOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "reopen {<number> | <url>}",
		Short: "重新打开议题",
		Args:  cobra.ExactArgs(1),
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
			return reopenRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Comment, "comment", "c", "", "添加一条重新打开评论")

	return cmd
}

func reopenRun(opts *ReopenOptions) error {
	cs := opts.IO.ColorScheme()

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	issue, err := shared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, []string{"id", "number", "title", "state"})
	if err != nil {
		return err
	}

	if issue.State == "OPEN" {
		fmt.Fprintf(opts.IO.ErrOut, "%s 议题 %s#%d (%s) 已打开\n", cs.Yellow("!"), ghrepo.FullName(baseRepo), issue.Number, issue.Title)
		return nil
	}

	if opts.Comment != "" {
		commentOpts := &prShared.CommentableOptions{
			Body:       opts.Comment,
			HttpClient: opts.HttpClient,
			InputType:  prShared.InputTypeInline,
			Quiet:      true,
			RetrieveCommentable: func() (prShared.Commentable, ghrepo.Interface, error) {
				return issue, baseRepo, nil
			},
		}
		err := prShared.CommentableRun(commentOpts)
		if err != nil {
			return err
		}
	}

	err = apiReopen(httpClient, baseRepo, issue)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已重新打开议题 %s#%d (%s)\n", cs.SuccessIconWithColor(cs.Green), ghrepo.FullName(baseRepo), issue.Number, issue.Title)

	return nil
}

func apiReopen(httpClient *http.Client, repo ghrepo.Interface, issue *api.Issue) error {
	if issue.IsPullRequest() {
		return api.PullRequestReopen(httpClient, repo, issue.ID)
	}

	var mutation struct {
		ReopenIssue struct {
			Issue struct {
				ID githubv4.ID
			}
		} `graphql:"reopenIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.ReopenIssueInput{
			IssueID: issue.ID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "IssueReopen", &mutation, variables)
}
