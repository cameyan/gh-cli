package review

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type ReviewOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	Prompter   prompter.Prompter

	Finder shared.PRFinder

	SelectorArg     string
	InteractiveMode bool
	ReviewType      api.PullRequestReviewState
	Body            string
}

func NewCmdReview(f *cmdutil.Factory, runF func(*ReviewOptions) error) *cobra.Command {
	opts := &ReviewOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
	}

	var (
		flagApprove        bool
		flagRequestChanges bool
		flagComment        bool
	)

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "review [<number> | <url> | <branch>]",
		Short: "向拉取请求(PR)添加审核(review)",
		Long: heredoc.Doc(`
			向拉取请求(PR)添加审核(review)。

			若未提供参数，将审核属于当前分支的拉取请求(PR)。
		`),
		Example: heredoc.Doc(`
			# 批准(approve)当前分支的拉取请求(PR)
			$ gh pr review --approve

			# 为当前分支的拉取请求留下审核评论
			$ gh pr review --comment -b "interesting"

			# 为指定的拉取请求(PR)添加审核(review)
			$ gh pr review 123

			# 对指定的拉取请求(PR)请求变更(request changes)
			$ gh pr review 123 -r -b "needs more ASCII art"
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

			bodyProvided := cmd.Flags().Changed("body")
			bodyFileProvided := bodyFile != ""

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--body` 或 `--body-file` 之一",
				bodyProvided,
				bodyFileProvided,
			); err != nil {
				return err
			}
			if bodyFileProvided {
				b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.Body = string(b)
			}

			found := 0
			if flagApprove {
				found++
				opts.ReviewType = api.ReviewApprove
			}
			if flagRequestChanges {
				found++
				opts.ReviewType = api.ReviewRequestChanges
				if opts.Body == "" {
					return cmdutil.FlagErrorf("请求变更(request-changes)审核的正文不能为空")
				}
			}
			if flagComment {
				found++
				opts.ReviewType = api.ReviewComment
				if opts.Body == "" {
					return cmdutil.FlagErrorf("评论(comment)审核的正文不能为空")
				}
			}

			if found == 0 && opts.Body == "" {
				if !opts.IO.CanPrompt() {
					return cmdutil.FlagErrorf("非交互式运行时必须提供 --approve、--request-changes 或 --comment")
				}
				opts.InteractiveMode = true
			} else if found == 0 && opts.Body != "" {
				return cmdutil.FlagErrorf("--body 必须与 --approve、--request-changes 或 --comment 之一配合使用")
			} else if found > 1 {
				return cmdutil.FlagErrorf("必须且只能指定 --approve、--request-changes 或 --comment 之一")
			}

			if runF != nil {
				return runF(opts)
			}
			return reviewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&flagApprove, "approve", "a", false, "批准(approve)拉取请求(PR)")
	cmd.Flags().BoolVarP(&flagRequestChanges, "request-changes", "r", false, "对拉取请求(PR)请求变更(request changes)")
	cmd.Flags().BoolVarP(&flagComment, "comment", "c", false, "对拉取请求(PR)发表评论(comment)")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "指定审核(review)的正文")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")

	return cmd
}

func reviewRun(opts *ReviewOptions) error {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "number"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	var reviewData *api.PullRequestReviewInput
	if opts.InteractiveMode {
		reviewData, err = reviewSurvey(opts)
		if err != nil {
			return err
		}
		if reviewData == nil {
			fmt.Fprint(opts.IO.ErrOut, "正在丢弃。\n")
			return nil
		}
	} else {
		reviewData = &api.PullRequestReviewInput{
			State: opts.ReviewType,
			Body:  opts.Body,
		}
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	err = api.AddReview(apiClient, baseRepo, pr, reviewData)
	if err != nil {
		return fmt.Errorf("创建审核(review)失败: %w", err)
	}

	if !opts.IO.IsStdoutTTY() || !opts.IO.IsStderrTTY() {
		return nil
	}

	cs := opts.IO.ColorScheme()

	switch reviewData.State {
	case api.ReviewComment:
		fmt.Fprintf(opts.IO.ErrOut, "%s 已审核(reviewed)拉取请求(PR) %s#%d\n", cs.Muted("-"), ghrepo.FullName(baseRepo), pr.Number)
	case api.ReviewApprove:
		fmt.Fprintf(opts.IO.ErrOut, "%s 已批准(approved)拉取请求(PR) %s#%d\n", cs.SuccessIcon(), ghrepo.FullName(baseRepo), pr.Number)
	case api.ReviewRequestChanges:
		fmt.Fprintf(opts.IO.ErrOut, "%s 已对拉取请求(PR) %s#%d 请求变更(requested changes)\n", cs.Red("+"), ghrepo.FullName(baseRepo), pr.Number)
	}

	return nil
}

func reviewSurvey(opts *ReviewOptions) (*api.PullRequestReviewInput, error) {
	options := []string{"评论", "批准", "请求变更"}
	reviewType, err := opts.Prompter.Select(
		"您想提交哪种类型的审核(review)?",
		options[0],
		options)
	if err != nil {
		return nil, err
	}

	var reviewState api.PullRequestReviewState

	switch reviewType {
	case 0:
		reviewState = api.ReviewComment
	case 1:
		reviewState = api.ReviewApprove
	case 2:
		reviewState = api.ReviewRequestChanges
	default:
		panic("不应到达的状态")
	}

	blankAllowed := false
	if reviewState == api.ReviewApprove {
		blankAllowed = true
	}

	body, err := opts.Prompter.MarkdownEditor("审核(review)正文", "", blankAllowed)
	if err != nil {
		return nil, err
	}

	if body == "" && (reviewState == api.ReviewComment || reviewState == api.ReviewRequestChanges) {
		return nil, errors.New("该类型的审核(review)不能为空")
	}

	if len(body) > 0 {
		renderedBody, err := markdown.Render(body,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithWrap(opts.IO.TerminalWidth()))
		if err != nil {
			return nil, err
		}

		fmt.Fprintf(opts.IO.Out, "收到:\n%s", renderedBody)
	}

	confirm, err := opts.Prompter.Confirm("提交?", true)
	if err != nil {
		return nil, err
	}

	if !confirm {
		return nil, nil
	}

	return &api.PullRequestReviewInput{
		Body:  body,
		State: reviewState,
	}, nil
}
