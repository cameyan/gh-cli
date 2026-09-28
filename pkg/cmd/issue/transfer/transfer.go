package transfer

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type TransferOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	IssueNumber      int
	DestRepoSelector string
}

func NewCmdTransfer(f *cmdutil.Factory, runF func(*TransferOptions) error) *cobra.Command {
	opts := TransferOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "transfer {<number> | <url>} <destination-repo>",
		Short: "将议题转移到另一个仓库",
		Args:  cmdutil.ExactArgs(2, "必须提供议题和目标仓库"),
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

			opts.DestRepoSelector = args[1]

			if runF != nil {
				return runF(&opts)
			}

			return transferRun(&opts)
		},
	}

	return cmd
}

func transferRun(opts *TransferOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	issue, err := shared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, []string{"id", "number"})
	if err != nil {
		return err
	}
	if issue.IsPullRequest() {
		return fmt.Errorf("议题 %s#%d 是拉取请求，无法转移", ghrepo.FullName(baseRepo), issue.Number)
	}

	destRepo, err := ghrepo.FromFullNameWithHost(opts.DestRepoSelector, baseRepo.RepoHost())
	if err != nil {
		return err
	}

	url, err := issueTransfer(httpClient, issue.ID, destRepo)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(opts.IO.Out, url)
	return err
}

func issueTransfer(httpClient *http.Client, issueID string, destRepo ghrepo.Interface) (string, error) {
	var destinationRepoID string
	if r, ok := destRepo.(*api.Repository); ok {
		destinationRepoID = r.ID
	} else {
		apiClient := api.NewClientFromHTTP(httpClient)
		r, err := api.IssueRepoInfo(apiClient, destRepo)
		if err != nil {
			return "", err
		}
		destinationRepoID = r.ID
	}

	var mutation struct {
		TransferIssue struct {
			Issue struct {
				URL string
			}
		} `graphql:"transferIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.TransferIssueInput{
			IssueID:      issueID,
			RepositoryID: destinationRepoID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	err := gql.Mutate(destRepo.RepoHost(), "IssueTransfer", &mutation, variables)
	return mutation.TransferIssue.Issue.URL, err
}
