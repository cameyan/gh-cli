package delete

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

type DeleteOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter

	IssueNumber int
	Confirmed   bool
}

type iprompter interface {
	ConfirmDeletion(string) error
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete {<number> | <url>}",
		Short: "删除 issue",
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

			return deleteRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Confirmed, "confirm", false, "无需提示直接确认删除")
	_ = cmd.Flags().MarkDeprecated("confirm", "请改用 `--yes`")
	cmd.Flags().BoolVar(&opts.Confirmed, "yes", false, "无需提示直接确认删除")
	return cmd
}

func deleteRun(opts *DeleteOptions) error {
	cs := opts.IO.ColorScheme()

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	issue, err := shared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, []string{"id", "number", "title"})
	if err != nil {
		return err
	}
	if issue.IsPullRequest() {
		return fmt.Errorf("issue %s#%d 是一个 PR，无法删除", ghrepo.FullName(baseRepo), issue.Number)
	}

	// 在交互式 shell 中执行时需要确认，
	// 除非已提供。否则跳过确认。
	if opts.IO.CanPrompt() && !opts.Confirmed {
		cs := opts.IO.ColorScheme()
		fmt.Printf("%s 已删除的 issue 无法恢复\n", cs.WarningIcon())
		err := opts.Prompter.ConfirmDeletion(fmt.Sprintf("%d", issue.Number))
		if err != nil {
			return err
		}
	}

	if err := apiDelete(httpClient, baseRepo, issue.ID); err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.ErrOut, "%s 已删除 issue %s#%d (%s)\n", cs.Red("✔"), ghrepo.FullName(baseRepo), issue.Number, issue.Title)
	}

	return nil
}

func apiDelete(httpClient *http.Client, repo ghrepo.Interface, issueID string) error {
	var mutation struct {
		DeleteIssue struct {
			Repository struct {
				ID githubv4.ID
			}
		} `graphql:"deleteIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.DeleteIssueInput{
			IssueID: issueID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "IssueDelete", &mutation, variables)
}
