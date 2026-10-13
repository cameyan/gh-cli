package pin

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

type PinOptions struct {
	HttpClient  func() (*http.Client, error)
	Config      func() (gh.Config, error)
	IO          *iostreams.IOStreams
	BaseRepo    func() (ghrepo.Interface, error)
	IssueNumber int
}

func NewCmdPin(f *cmdutil.Factory, runF func(*PinOptions) error) *cobra.Command {
	opts := &PinOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		BaseRepo:   f.BaseRepo,
	}

	cmd := &cobra.Command{
		Use:   "pin {<number> | <url>}",
		Short: "置顶议题",
		Long: heredoc.Doc(`
			将议题置顶到仓库。

			可以通过议题编号或 URL 指定议题。
		`),
		Example: heredoc.Doc(`
			# 将议题置顶到当前仓库
			$ gh issue pin 23

			# 通过 URL 置顶议题
			$ gh issue pin https://github.com/owner/repo/issues/23

			# 将议题置顶到指定仓库
			$ gh issue pin 23 --repo owner/repo
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

			return pinRun(opts)
		},
	}

	return cmd
}

func pinRun(opts *PinOptions) error {
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

	if issue.IsPinned {
		fmt.Fprintf(opts.IO.ErrOut, "%s 议题 %s#%d (%s) 已置顶\n", cs.Yellow("!"), ghrepo.FullName(baseRepo), issue.Number, issue.Title)
		return nil
	}

	err = pinIssue(httpClient, baseRepo, issue)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已置顶议题 %s#%d (%s)\n", cs.SuccessIcon(), ghrepo.FullName(baseRepo), issue.Number, issue.Title)

	return nil
}

func pinIssue(httpClient *http.Client, repo ghrepo.Interface, issue *api.Issue) error {
	var mutation struct {
		PinIssue struct {
			Issue struct {
				ID githubv4.ID
			}
		} `graphql:"pinIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.PinIssueInput{
			IssueID: issue.ID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)

	return gql.Mutate(repo.RepoHost(), "IssuePin", &mutation, variables)
}
