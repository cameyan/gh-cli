package comment

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdComment(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*shared.CommentableOptions) error) *cobra.Command {
	opts := &shared.CommentableOptions{
		IO:                        f.IOStreams,
		HttpClient:                f.HttpClient,
		EditSurvey:                shared.CommentableEditSurvey(f.Config, f.IOStreams),
		InteractiveEditSurvey:     shared.CommentableInteractiveEditSurvey(f.Config, f.IOStreams),
		ConfirmSubmitSurvey:       shared.CommentableConfirmSubmitSurvey(f.Prompter),
		ConfirmCreateIfNoneSurvey: shared.CommentableInteractiveCreateIfNoneSurvey(f.Prompter),
		ConfirmDeleteLastComment:  shared.CommentableConfirmDeleteLastComment(f.Prompter),
		OpenInBrowser:             f.Browser.Browse,
	}

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "comment [<number> | <url> | <branch>]",
		Short: "向拉取请求(PR)添加评论",
		Long: heredoc.Docf(`
			向 GitHub 拉取请求(PR)添加评论。

			若未通过 flag 提供正文文本或附件，该命令将交互式提示输入评论文本。

			Use %[1]s--attach%[1]s to upload an image or video. If the body already references an
			attached file, such as %[1]s![alt](./login.png)%[1]s, that reference is rewritten to point
			at the uploaded asset. Paths in a %[1]s--body-file%[1]s are relative to its directory,
			falling back to the working directory. Any attached file the body does not reference
			is appended to the end of the comment.
			You can attach up to 50 files per command.

			图片的替代文本(alt text)为 %[1]s#%[1]s 之后的路径内容，如
			%[1]s--attach './login.png#The login error state'%[1]s。未提供时使用文件名。
			正文中已有的引用会保留其中书写的替代文本。视频以播放器形式呈现且没有替代文本，
			因此无法为其指定。
		`, "`"),
		Example: heredoc.Doc(`
			# 向拉取请求(PR)添加评论
			$ gh pr comment 13 --body "Hi from GitHub CLI"

			# 附加截图，"#" 之后为替代文本(alt text)
			$ gh pr comment 13 --attach './login.png#The login error state'

			# 通过重复 flag 附加多个文件
			$ gh pr comment 13 --attach ./before.png --attach ./after.png
		`),
		Args: cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 --repo flag 时必须提供位置参数")
			}
			var selector string
			if len(args) > 0 {
				selector = args[0]
			}
			fields := []string{"id", "url"}
			if opts.EditLast || opts.DeleteLast {
				fields = append(fields, "comments")
			}
			finder := shared.NewFinder(f)
			opts.RetrieveCommentable = func() (shared.Commentable, ghrepo.Interface, error) {
				if len(opts.Assets) > 0 {
					fields = append(fields, "repository")
				}
				return finder.Find(shared.FindOptions{
					Selector: selector,
					Fields:   fields,
				})
			}
			if err := shared.CommentablePreRun(cmd, opts, telemetry); err != nil {
				return err
			}

			// 附件不是正文，因此仅附加文件的编辑会保留其正在编辑的评论。
			opts.KeepExistingBody = true

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if bodyFile != "" {
				var err error
				opts.Body, opts.BodyDir, err = cmdutil.ReadMarkdownBodyFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
			}

			if runF != nil {
				return runF(opts)
			}
			return shared.CommentableRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "评论正文 `text`")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")
	cmd.Flags().BoolP("editor", "e", false, "跳过提示，打开文本编辑器撰写正文")
	cmd.Flags().BoolP("web", "w", false, "打开 web 浏览器撰写评论")
	cmd.Flags().BoolVar(&opts.EditLast, "edit-last", false, "编辑当前用户的最后一条评论")
	cmd.Flags().BoolVar(&opts.DeleteLast, "delete-last", false, "删除当前用户的最后一条评论")
	cmd.Flags().BoolVar(&opts.DeleteLastConfirmed, "yes", false, "提供 --delete-last 时跳过删除确认提示")
	cmd.Flags().BoolVar(&opts.CreateIfNone, "create-if-none", false, "未找到任何评论时创建新评论。只能与 --edit-last 一起使用")
	opts.AttachFlag = attachments.AddFlag(cmd)

	return cmd
}
