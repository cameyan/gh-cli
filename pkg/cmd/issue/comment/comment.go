package comment

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdComment(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*prShared.CommentableOptions) error) *cobra.Command {
	opts := &prShared.CommentableOptions{
		IO:                        f.IOStreams,
		HttpClient:                f.HttpClient,
		Config:                    f.Config,
		EditSurvey:                prShared.CommentableEditSurvey(f.Config, f.IOStreams),
		InteractiveEditSurvey:     prShared.CommentableInteractiveEditSurvey(f.Config, f.IOStreams),
		ConfirmSubmitSurvey:       prShared.CommentableConfirmSubmitSurvey(f.Prompter),
		ConfirmCreateIfNoneSurvey: prShared.CommentableInteractiveCreateIfNoneSurvey(f.Prompter),
		ConfirmDeleteLastComment:  prShared.CommentableConfirmDeleteLastComment(f.Prompter),
		OpenInBrowser:             f.Browser.Browse,
	}

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "comment {<number> | <url>}",
		Short: "为议题添加评论",
		Long: heredoc.Docf(`
			为 GitHub 议题添加评论。

			如果未通过旗标提供正文文本或附件，命令将
			以交互方式提示输入评论文本。

			使用 %[1]s--attach%[1]s 上传图片或视频。如果正文中已经引用了
			某个已附加文件，例如 %[1]s![alt](./login.png)%[1]s，该引用会被重写为指向
			已上传的资源。正文未引用的任何已附加文件都会被追加
			到评论末尾。
			每条命令最多可附加 50 个文件。

			图片的替代文本会跟在 %[1]s#%[1]s 之后的路径中，如
			%[1]s--attach './login.png#The login error state'%[1]s。若不提供，则使用文件名。
			正文中已有的引用会保留写在其处的替代文本。视频会渲染
			为播放器且没有替代文本，因此无法为其指定。
		`, "`"),
		Example: heredoc.Doc(`
			# 为议题添加评论
			$ gh issue comment 12 --body "Hi from GitHub CLI"

			# 附加截图，替代文本位于 "#" 之后
			$ gh issue comment 12 --attach './login.png#The login error state'

			# 通过重复该旗标附加多个文件
			$ gh issue comment 12 --attach ./before.png --attach ./after.png
		`),
		Args: cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			opts.RetrieveCommentable = func() (prShared.Commentable, ghrepo.Interface, error) {
				// TODO wm: 需要更多测试
				issueNumber, parsedBaseRepo, err := shared.ParseIssueFromArg(args[0])
				if err != nil {
					return nil, nil, err
				}

				// 如果参数提供了基础仓库，则直接使用它。
				var baseRepo ghrepo.Interface

				if parsedBaseRepo, present := parsedBaseRepo.Value(); present {
					baseRepo = parsedBaseRepo
				} else {
					// 支持 `-R, --repo` 覆盖
					baseRepo, err = f.BaseRepo()
					if err != nil {
						return nil, nil, err
					}
				}

				httpClient, err := f.HttpClient()
				if err != nil {
					return nil, nil, err
				}

				fields := []string{"id", "url"}
				if opts.EditLast || opts.DeleteLast {
					fields = append(fields, "comments")
				}
				if len(opts.Assets) > 0 {
					fields = append(fields, "repository")
				}

				issue, err := issueShared.FindIssueOrPR(httpClient, baseRepo, issueNumber, fields)
				if err != nil {
					return nil, nil, err
				}

				return issue, baseRepo, nil
			}
			if err := prShared.CommentablePreRun(cmd, opts, telemetry); err != nil {
				return err
			}

			// 附件不是正文，因此仅附加附件的编辑会保留
			// 正在编辑的评论。
			opts.KeepExistingBody = true

			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if bodyFile != "" {
				b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.Body = string(b)
			}

			if runF != nil {
				return runF(opts)
			}
			return prShared.CommentableRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "评论正文 `text`")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本(使用 \"-\" 从标准输入读取)")
	cmd.Flags().BoolP("editor", "e", false, "跳过提示并打开文本编辑器编写正文")
	cmd.Flags().BoolP("web", "w", false, "打开网页浏览器编写评论")
	cmd.Flags().BoolVar(&opts.EditLast, "edit-last", false, "编辑当前用户的最后一条评论")
	cmd.Flags().BoolVar(&opts.DeleteLast, "delete-last", false, "删除当前用户的最后一条评论")
	cmd.Flags().BoolVar(&opts.DeleteLastConfirmed, "yes", false, "提供 --delete-last 时跳过删除确认提示")
	cmd.Flags().BoolVar(&opts.CreateIfNone, "create-if-none", false, "如果未找到评论则创建新评论。只能与 --edit-last 一起使用")
	opts.AttachFlag = attachments.AddFlag(cmd)

	return cmd
}
