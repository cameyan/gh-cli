package comment

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/discussion/client"
	"github.com/cli/cli/v2/pkg/cmd/discussion/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// CommentOptions holds the configuration for the discussion comment command.
type CommentOptions struct {
	IO       *iostreams.IOStreams
	BaseRepo func() (ghrepo.Interface, error)
	Client   func() (client.DiscussionClient, error)
	Prompter prompter.Prompter

	ParsedArg *shared.ParsedDiscussionOrCommentArg

	Body     string
	BodyFile string
	Edit     bool
	Delete   bool
	Yes      bool
}

// NewCmdComment returns the "discussion comment" command.
func NewCmdComment(f *cmdutil.Factory, runF func(*CommentOptions) error) *cobra.Command {
	opts := &CommentOptions{
		IO:       f.IOStreams,
		Prompter: f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "comment {<number> | <discussion-url> | <comment-id> | <comment-url>} [flags]",
		Short: "添加、编辑或删除讨论的评论或回复(预览版)",
		Long: heredoc.Docf(`
			管理 GitHub 讨论的评论或回复。

			位置参数可以是讨论编号或 URL(用于添加新的顶层评论)，也可以是评论节点
			ID 或评论 URL(用于回复、编辑或删除该评论)。

			当参数为讨论编号或 URL 时，默认操作是添加新的顶层评论。同理，当参数为
			评论 URL 或 ID 时，默认操作是添加回复。

			使用 %[1]s--edit%[1]s 更新评论/回复内容，或使用 %[1]s--delete%[1]s 将其删除。

			评论内容可通过 %[1]s--body%[1]s、%[1]s--body-file%[1]s 提供，或通过编辑器交互式输入。
		`, "`"),
		Example: heredoc.Doc(`
			# 向讨论 #123 添加顶层评论
			$ gh discussion comment 123 --body 'Thanks'

			# 通过 URL 回复某条评论
			$ gh discussion comment 'https://github.com/OWNER/REPO/discussions/123#discussioncomment-456' --body 'Thanks'

			# 通过节点 ID 回复某条评论
			$ gh discussion comment DC_abc123 --body 'Thanks'

			# 编辑评论/回复
			$ gh discussion comment 'https://github.com/OWNER/REPO/discussions/123#discussioncomment-456' --edit --body 'Thanks'

			# 删除评论/回复
			$ gh discussion comment 'https://github.com/OWNER/REPO/discussions/123#discussioncomment-456' --delete

			# 删除评论/回复且不显示确认提示
			$ gh discussion comment 'https://github.com/OWNER/REPO/discussions/123#discussioncomment-456' --delete --yes
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo
			opts.Client = shared.DiscussionClientFunc(f)

			if err := cmdutil.MutuallyExclusive("specify only one of --edit or --delete",
				cmd.Flags().Changed("edit"), cmd.Flags().Changed("delete")); err != nil {
				return err
			}
			if opts.Delete {
				if cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file") {
					return cmdutil.FlagErrorf("--delete 不能与 --body 或 --body-file 同时使用")
				}
			}
			if opts.Yes && !opts.Delete {
				return cmdutil.FlagErrorf("--yes 只能与 --delete 一起使用")
			}
			if !opts.IO.CanPrompt() && opts.Delete && !opts.Yes {
				return cmdutil.FlagErrorf("非交互式运行且使用 --delete 时必须指定 --yes")
			}
			if !opts.IO.CanPrompt() && !opts.Delete {
				if opts.Body == "" && opts.BodyFile == "" {
					return cmdutil.FlagErrorf("非交互式运行时必须指定 --body 或 --body-file")
				}
			}
			if err := cmdutil.MutuallyExclusive("specify only one of --body or --body-file",
				cmd.Flags().Changed("body"), cmd.Flags().Changed("body-file")); err != nil {
				return err
			}

			parsed, err := shared.ParseDiscussionOrCommentArg(args[0])
			if err != nil {
				return err
			}

			opts.ParsedArg = parsed

			if (opts.Edit || opts.Delete) && (parsed.CommentNodeID == "" && parsed.CommentDatabaseID == 0) {
				return cmdutil.FlagErrorf("--edit 和 --delete 需要评论 ID 或评论 URL 作为参数")
			}

			if opts.ParsedArg.Repo != nil {
				opts.BaseRepo = func() (ghrepo.Interface, error) {
					return parsed.Repo, nil
				}
			}

			if runF != nil {
				return runF(opts)
			}
			return commentRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "评论内容文本")
	cmd.Flags().StringVarP(&opts.BodyFile, "body-file", "F", "", "从文件读取评论内容(使用 \"-\" 表示从标准输入读取)")
	cmd.Flags().BoolVar(&opts.Edit, "edit", false, "编辑指定的评论")
	cmd.Flags().BoolVar(&opts.Delete, "delete", false, "删除指定的评论")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "跳过删除确认提示")

	cmdutil.EnableRepoOverride(cmd, f)

	return cmd
}

func commentRun(opts *CommentOptions) error {
	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}

	if opts.Delete {
		return runDelete(opts, c, baseRepo)
	}
	if opts.Edit {
		return runEdit(opts, c, baseRepo)
	}
	if opts.ParsedArg.CommentNodeID != "" || opts.ParsedArg.CommentDatabaseID != 0 {
		return runReply(opts, c, baseRepo)
	}
	return runAdd(opts, c, baseRepo)
}

func runDelete(opts *CommentOptions, c client.DiscussionClient, baseRepo ghrepo.Interface) error {
	commentID, err := resolveCommentID(opts, c, baseRepo)
	if err != nil {
		return err
	}

	if _, err := c.GetComment(baseRepo.RepoHost(), commentID); err != nil {
		return err
	}

	if !opts.Yes {
		confirmed, err := opts.Prompter.Confirm("确定要删除此评论吗?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			return cmdutil.CancelError
		}
	}

	return c.DeleteComment(baseRepo, commentID)
}

func runEdit(opts *CommentOptions, c client.DiscussionClient, baseRepo ghrepo.Interface) error {
	commentID, err := resolveCommentID(opts, c, baseRepo)
	if err != nil {
		return err
	}

	existing, err := c.GetComment(baseRepo.RepoHost(), commentID)
	if err != nil {
		return err
	}

	body, err := resolveBody(opts, existing.Body)
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	comment, err := c.UpdateComment(baseRepo, commentID, body)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	fmt.Fprintln(opts.IO.Out, comment.URL)
	return nil
}

func runReply(opts *CommentOptions, c client.DiscussionClient, baseRepo ghrepo.Interface) error {
	commentID, err := resolveCommentID(opts, c, baseRepo)
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	existing, err := c.GetComment(baseRepo.RepoHost(), commentID)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	body, err := resolveBody(opts, "")
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	comment, err := c.AddComment(baseRepo, existing.DiscussionID, body, commentID)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	fmt.Fprintln(opts.IO.Out, comment.URL)
	return nil
}

func runAdd(opts *CommentOptions, c client.DiscussionClient, baseRepo ghrepo.Interface) error {
	body, err := resolveBody(opts, "")
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	discussion, err := c.GetByNumber(baseRepo, opts.ParsedArg.Number)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	comment, err := c.AddComment(baseRepo, discussion.ID, body, "")
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	fmt.Fprintln(opts.IO.Out, comment.URL)
	return nil
}

// resolveCommentID 返回评论节点 ID，若参数为评论 URL，则通过
// API 将其从数据库 ID 解析而来。
func resolveCommentID(opts *CommentOptions, c client.DiscussionClient, repo ghrepo.Interface) (string, error) {
	if opts.ParsedArg.CommentNodeID != "" {
		return opts.ParsedArg.CommentNodeID, nil
	}
	if opts.ParsedArg.CommentDatabaseID != 0 {
		return c.ResolveCommentNodeID(repo, opts.ParsedArg.CommentDatabaseID)
	}
	// 由于标志解析时的检查，正常不应到达此处。
	return "", fmt.Errorf("没有可用的评论 ID/URL")
}

// resolveBody 从标志或交互式输入确定评论内容。
// defaultBody 用作编辑器的初始内容(例如编辑时使用现有评论内容)。
func resolveBody(opts *CommentOptions, defaultBody string) (string, error) {
	if opts.BodyFile != "" {
		b, err := cmdutil.ReadFile(opts.BodyFile, opts.IO.In)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	if opts.Body != "" {
		return opts.Body, nil
	}

	body, err := opts.Prompter.MarkdownEditor("内容", defaultBody, false)
	if err != nil {
		return "", err
	}

	return body, nil
}
