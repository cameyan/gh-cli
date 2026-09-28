package close

import (
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type CloseOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	IssueNumber int
	Comment     string
	Reason      string
	DuplicateOf string
}

func NewCmdClose(f *cmdutil.Factory, runF func(*CloseOptions) error) *cobra.Command {
	opts := &CloseOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "close {<number> | <url>}",
		Short: "关闭议题",
		Example: heredoc.Doc(`
			# 关闭议题
			$ gh issue close 123

			# 关闭议题并添加一条关闭评论
			$ gh issue close 123 --comment "Closing this issue"

			# 将议题关闭为议题 #456 的重复项
			$ gh issue close 123 --duplicate-of 456

			# 将议题关闭为未计划
			$ gh issue close 123 --reason "not planned"
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
			if opts.DuplicateOf != "" {
				if opts.Reason == "" {
					opts.Reason = "duplicate"
				} else if opts.Reason != "duplicate" {
					return cmdutil.FlagErrorf("`--duplicate-of` 只能与 `--reason duplicate` 一起使用")
				}
			}

			if runF != nil {
				return runF(opts)
			}
			return closeRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Comment, "comment", "c", "", "添加一条关闭评论")
	cmdutil.StringEnumFlag(cmd, &opts.Reason, "reason", "r", "", []string{"completed", "not planned", "duplicate"}, "关闭原因")
	cmd.Flags().StringVar(&opts.DuplicateOf, "duplicate-of", "", "通过编号或 URL 标记为另一议题的重复项")

	return cmd
}

func closeRun(opts *CloseOptions) error {
	cs := opts.IO.ColorScheme()
	closeReason := opts.Reason
	if opts.DuplicateOf != "" {
		if closeReason == "" {
			closeReason = "duplicate"
		} else if closeReason != "duplicate" {
			return cmdutil.FlagErrorf("`--duplicate-of` 只能与 `--reason duplicate` 一起使用")
		}
	}

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

	if issue.State == "CLOSED" {
		fmt.Fprintf(opts.IO.ErrOut, "%s 议题 %s#%d (%s) 已关闭\n", cs.Yellow("!"), ghrepo.FullName(baseRepo), issue.Number, issue.Title)
		return nil
	}

	var duplicateIssueID string
	if opts.DuplicateOf != "" {
		if issue.IsPullRequest() {
			return cmdutil.FlagErrorf("`--duplicate-of` 仅支持议题")
		}
		duplicateIssueNumber, duplicateRepo, err := shared.ParseIssueFromArg(opts.DuplicateOf)
		if err != nil {
			return cmdutil.FlagErrorf("`--duplicate-of` 的值无效: %v", err)
		}
		duplicateIssueRepo := baseRepo
		if parsedRepo, present := duplicateRepo.Value(); present {
			duplicateIssueRepo = parsedRepo
		}
		if ghrepo.IsSame(baseRepo, duplicateIssueRepo) && issue.Number == duplicateIssueNumber {
			return cmdutil.FlagErrorf("`--duplicate-of` 不能引用当前议题")
		}
		duplicateIssue, err := shared.FindIssueOrPR(httpClient, duplicateIssueRepo, duplicateIssueNumber, []string{"id"})
		if err != nil {
			return err
		}
		if duplicateIssue.IsPullRequest() {
			return cmdutil.FlagErrorf("`--duplicate-of` 必须引用一个议题")
		}
		duplicateIssueID = duplicateIssue.ID
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

	err = apiClose(httpClient, baseRepo, issue, closeReason, duplicateIssueID)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已关闭议题 %s#%d (%s)\n", cs.SuccessIconWithColor(cs.Red), ghrepo.FullName(baseRepo), issue.Number, issue.Title)

	return nil
}

func apiClose(httpClient *http.Client, repo ghrepo.Interface, issue *api.Issue, reason string, duplicateIssueID string) error {
	if issue.IsPullRequest() {
		return api.PullRequestClose(httpClient, repo, issue.ID)
	}

	switch reason {
	case "":
		// 如果未指定原因，则不设置它。
	case "not planned":
		reason = "NOT_PLANNED"
	case "duplicate":
		reason = "DUPLICATE"
	default:
		reason = "COMPLETED"
	}

	var mutation struct {
		CloseIssue struct {
			Issue struct {
				ID githubv4.ID
			}
		} `graphql:"closeIssue(input: $input)"`
	}

	variables := map[string]any{
		"input": CloseIssueInput{
			IssueID:          issue.ID,
			StateReason:      reason,
			DuplicateIssueID: duplicateIssueID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "IssueClose", &mutation, variables)
}

type CloseIssueInput struct {
	IssueID          string `json:"issueId"`
	StateReason      string `json:"stateReason,omitempty"`
	DuplicateIssueID string `json:"duplicateIssueId,omitempty"`
}
