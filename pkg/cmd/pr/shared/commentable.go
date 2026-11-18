package shared

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
	"github.com/spf13/cobra"
)

var errNoUserComments = errors.New("未找到当前用户的任何评论")
var errDeleteNotConfirmed = errors.New("删除未获确认")

type InputType int

const (
	InputTypeEditor InputType = iota
	InputTypeInline
	InputTypeWeb
)

type Commentable interface {
	Link() string
	Identifier() string
	CurrentUserComments() []api.Comment
	RepositoryDatabaseID() int64
	RepositoryViewerPermission() string
}

type CommentableOptions struct {
	IO                        *iostreams.IOStreams
	HttpClient                func() (*http.Client, error)
	RetrieveCommentable       func() (Commentable, ghrepo.Interface, error)
	EditSurvey                func(string) (string, error)
	InteractiveEditSurvey     func(string) (string, error)
	ConfirmSubmitSurvey       func() (bool, error)
	ConfirmCreateIfNoneSurvey func() (bool, error)
	ConfirmDeleteLastComment  func(string) (bool, error)
	OpenInBrowser             func(string) error
	Interactive               bool
	InputType                 InputType
	Body                      string
	EditLast                  bool
	DeleteLast                bool
	DeleteLastConfirmed       bool
	CreateIfNone              bool
	Quiet                     bool
	Host                      string

	BodyDir          string
	BodyProvided     bool
	KeepExistingBody bool
	AttachFlag       *attachments.Flag
	AttachEvent      *attachments.TelemetryEvent
	Assets           []attachments.UserAsset
}

func CommentablePreRun(cmd *cobra.Command, opts *CommentableOptions, telemetry ghtelemetry.InvocationRecorder) error {
	inputFlags := 0
	if cmd.Flags().Changed("body") {
		opts.InputType = InputTypeInline
		opts.BodyProvided = true
		inputFlags++
	}
	if cmd.Flags().Changed("body-file") {
		opts.InputType = InputTypeInline
		opts.BodyProvided = true
		inputFlags++
	}
	web, _ := cmd.Flags().GetBool("web")
	if web {
		opts.InputType = InputTypeWeb
		inputFlags++
	}
	if editor, _ := cmd.Flags().GetBool("editor"); editor {
		opts.InputType = InputTypeEditor
		inputFlags++
	}

	if err := cmdutil.MutuallyExclusive(
		"`--attach` 不支持与 `--web` 同时使用",
		opts.AttachFlag.Changed(),
		web,
	); err != nil {
		return err
	}

	if err := cmdutil.MutuallyExclusive(
		"`--attach` 不支持与 `--delete-last` 同时使用",
		opts.AttachFlag.Changed(),
		opts.DeleteLast,
	); err != nil {
		return err
	}

	var err error
	opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
	opts.Assets, err = opts.AttachFlag.UserAssets()
	if err != nil {
		return err
	}

	// 附件本身就是一种正文输入，因此仅使用 `--attach shot.png`
	// 会发布一张没有文本的图片。它不属于上面的互斥组，
	// 所以只在其他输入都未提供时才计数。
	if len(opts.Assets) > 0 && inputFlags == 0 {
		opts.InputType = InputTypeInline
		inputFlags++
	}

	if opts.CreateIfNone && !opts.EditLast {
		return cmdutil.FlagErrorf("`--create-if-none` 只能与 `--edit-last` 一起使用")
	}

	if opts.DeleteLastConfirmed && !opts.DeleteLast {
		return cmdutil.FlagErrorf("`--yes` 只能与 `--delete-last` 一起使用")
	}

	if opts.DeleteLast {
		if inputFlags > 0 {
			return cmdutil.FlagErrorf("使用 `--delete-last` 时不应提供评论正文")
		}
		if opts.IO.CanPrompt() || opts.DeleteLastConfirmed {
			opts.Interactive = opts.IO.CanPrompt()
			return nil
		}
		return cmdutil.FlagErrorf("非交互模式下应提供 `--yes` 以确认删除")
	}

	if inputFlags == 0 {
		if !opts.IO.CanPrompt() {
			return cmdutil.FlagErrorf("非交互式运行时必须提供 flag")
		}
		opts.Interactive = true
	} else if inputFlags > 1 {
		return cmdutil.FlagErrorf("只能指定 `--body`、`--body-file`、`--editor` 或 `--web` 之一")
	}

	return nil
}

func CommentableRun(opts *CommentableOptions) error {
	commentable, repo, err := opts.RetrieveCommentable()
	if err != nil {
		return err
	}
	opts.Host = repo.RepoHost()
	if opts.DeleteLast {
		return deleteComment(commentable, opts)
	}

	// 以是否提供附件为判断条件，因为 createComment 和 updateComment
	// 在 web 路径上会提前返回。此处不加保护的检查会让未附加任何文件的运行失败。
	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return err
		}
		uploader, err = attachments.NewUploader(httpClient, opts.Host, commentable.RepositoryDatabaseID(), commentable.RepositoryViewerPermission())
		if err != nil {
			return err
		}
	}

	// 创建新评论；在处理更新最后一条评论的复杂逻辑之前直接返回
	if !opts.EditLast {
		return createComment(commentable, opts, uploader)
	}

	// 更新最后一条评论，并相应地处理成功或意外错误
	err = updateComment(commentable, opts, uploader)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errNoUserComments) {
		return err
	}

	// 判断是否创建新评论；若为交互模式且缺少选项，则提示用户
	if !opts.CreateIfNone && opts.Interactive {
		opts.CreateIfNone, err = opts.ConfirmCreateIfNoneSurvey()
		if err != nil {
			return err
		}
	}
	if !opts.CreateIfNone {
		return errNoUserComments
	}

	// 由于更新最后一条评论因没有该用户评论而失败，因此创建新评论
	if opts.Interactive {
		fmt.Fprintln(opts.IO.ErrOut, "未找到任何评论。正在创建新评论。")
	}

	return createComment(commentable, opts, uploader)
}

func createComment(commentable Commentable, opts *CommentableOptions, uploader *attachments.Uploader) error {
	switch opts.InputType {
	case InputTypeWeb:
		openURL := commentable.Link() + "#issuecomment-new"
		if opts.IO.IsStdoutTTY() && !opts.Quiet {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.OpenInBrowser(openURL)
	case InputTypeEditor:
		var body string
		var err error
		if opts.Interactive {
			body, err = opts.InteractiveEditSurvey("")
		} else {
			body, err = opts.EditSurvey("")
		}
		if err != nil {
			return err
		}
		opts.Body = body
	}

	if opts.Interactive {
		cont, err := opts.ConfirmSubmitSurvey()
		if err != nil {
			return err
		}
		if !cont {
			return errors.New("正在丢弃...")
		}
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	body, write, uploadErr := bodyForWrite(opts, uploader)
	if !write {
		return fmt.Errorf("%w\n未发布任何评论", uploadErr)
	}

	apiClient := api.NewClientFromHTTP(httpClient)
	params := api.CommentCreateInput{Body: body, SubjectId: commentable.Identifier()}
	url, err := api.CommentCreate(apiClient, opts.Host, params)
	if err != nil {
		// 先上传: 已上传的资产(asset)比失败的写入存续更久，
		// 用户需要先知道它存在，再听到评论未保存的消息。
		return errors.Join(uploadErr, err)
	}

	if !opts.Quiet {
		fmt.Fprintln(opts.IO.Out, url)
	}

	return uploadErr
}

func updateComment(commentable Commentable, opts *CommentableOptions, uploader *attachments.Uploader) error {
	comments := commentable.CurrentUserComments()
	if len(comments) == 0 {
		return errNoUserComments
	}

	lastComment := &comments[len(comments)-1]

	switch opts.InputType {
	case InputTypeWeb:
		openURL := lastComment.Link()
		if opts.IO.IsStdoutTTY() && !opts.Quiet {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.OpenInBrowser(openURL)
	case InputTypeEditor:
		var body string
		var err error
		initialValue := lastComment.Content()
		if opts.Interactive {
			body, err = opts.InteractiveEditSurvey(initialValue)
		} else {
			body, err = opts.EditSurvey(initialValue)
		}
		if err != nil {
			return err
		}
		opts.Body = body
	case InputTypeInline:
		// 显式的 `--body ""` 仍会清空该评论。上面的编辑器分支已用该评论
		// 作为初始内容，因此无需合并。
		if opts.KeepExistingBody && !opts.BodyProvided {
			opts.Body = lastComment.Content()
		}
	}

	if opts.Interactive {
		cont, err := opts.ConfirmSubmitSurvey()
		if err != nil {
			return err
		}
		if !cont {
			return errors.New("正在丢弃...")
		}
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	body, write, uploadErr := bodyForWrite(opts, uploader)
	if !write {
		return fmt.Errorf("%w\n评论未被更改", uploadErr)
	}

	apiClient := api.NewClientFromHTTP(httpClient)
	params := api.CommentUpdateInput{Body: body, CommentId: lastComment.Identifier()}
	url, err := api.CommentUpdate(apiClient, opts.Host, params)
	if err != nil {
		// 先上传，原因与 createComment 相同。
		return errors.Join(uploadErr, err)
	}

	if !opts.Quiet {
		fmt.Fprintln(opts.IO.Out, url)
	}

	return uploadErr
}

// bodyForWrite 上传资产(asset)，并报告其产出的正文是否值得写入。
// 未能产出任何内容的失败上传什么都不写，因为没有东西可以挽救，
// 而一条声称带有附件实际却没有的评论只会让失败雪上加霜。
//
// 调用方必须在每次提示之后调用本函数，因为任何可能提示或取消的操作
// 都不得出现在上传之后。
func bodyForWrite(opts *CommentableOptions, uploader *attachments.Uploader) (body string, write bool, err error) {
	if uploader == nil {
		return opts.Body, true, nil
	}
	body, uploadResult, err := uploader.UploadAndAttach(context.Background(), opts.Body, opts.BodyDir, opts.Assets)
	opts.AttachEvent.RecordOperations(uploadResult)
	if err != nil && uploadResult.Uploaded == 0 {
		return "", false, err
	}
	return body, true, err
}

func deleteComment(commentable Commentable, opts *CommentableOptions) error {
	comments := commentable.CurrentUserComments()
	if len(comments) == 0 {
		return errNoUserComments
	}

	lastComment := comments[len(comments)-1]

	cs := opts.IO.ColorScheme()

	if opts.Interactive && !opts.DeleteLastConfirmed {
		// 这并非截断可能包含表情符号或其他宽字符的随机字符串的理想方式。
		truncated := lastComment.Body
		if len(lastComment.Body) > 40 {
			truncated = lastComment.Body[:40] + "..."
		}

		fmt.Fprintf(opts.IO.Out, "%s 已删除的评论无法恢复。\n", cs.WarningIcon())
		ok, err := opts.ConfirmDeleteLastComment(truncated)
		if err != nil {
			return err
		}
		if !ok {
			return errDeleteNotConfirmed
		}
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	apiClient := api.NewClientFromHTTP(httpClient)
	params := api.CommentDeleteInput{CommentId: lastComment.Identifier()}
	deletionErr := api.CommentDelete(apiClient, opts.Host, params)
	if deletionErr != nil {
		return deletionErr
	}

	if !opts.Quiet {
		fmt.Fprintln(opts.IO.ErrOut, "评论已删除")
	}

	return nil
}

func CommentableConfirmSubmitSurvey(p Prompt) func() (bool, error) {
	return func() (bool, error) {
		return p.Confirm("提交?", true)
	}
}

func CommentableInteractiveEditSurvey(cf func() (gh.Config, error), io *iostreams.IOStreams) func(string) (string, error) {
	return func(initialValue string) (string, error) {
		editorCommand, err := cmdutil.DetermineEditor(cf)
		if err != nil {
			return "", err
		}
		cs := io.ColorScheme()
		fmt.Fprintf(io.Out, "- %s 以在 %s 中撰写您的评论... ", cs.Bold("按 Enter"), cs.Bold(surveyext.EditorName(editorCommand)))
		_ = waitForEnter(io.In)
		return surveyext.Edit(editorCommand, "*.md", initialValue, io.In, io.Out, io.ErrOut)
	}
}

func CommentableInteractiveCreateIfNoneSurvey(p Prompt) func() (bool, error) {
	return func() (bool, error) {
		return p.Confirm("未找到任何评论。要创建一条吗?", true)
	}
}

func CommentableEditSurvey(cf func() (gh.Config, error), io *iostreams.IOStreams) func(string) (string, error) {
	return func(initialValue string) (string, error) {
		editorCommand, err := cmdutil.DetermineEditor(cf)
		if err != nil {
			return "", err
		}
		return surveyext.Edit(editorCommand, "*.md", initialValue, io.In, io.Out, io.ErrOut)
	}
}

func CommentableConfirmDeleteLastComment(p Prompt) func(string) (bool, error) {
	return func(body string) (bool, error) {
		return p.Confirm(fmt.Sprintf("删除该评论: %q?", body), true)
	}
}

func waitForEnter(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return scanner.Err()
}
