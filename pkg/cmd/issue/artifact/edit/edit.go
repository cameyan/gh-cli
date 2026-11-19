package edit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/issue/artifact/client"
	"github.com/cli/cli/v2/pkg/cmd/issue/artifact/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// EditOptions holds the flags and dependencies of gh issue artifact edit.
type EditOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Client     func() (client.ArtifactClient, error)

	IssueNumber    int
	ArtifactNumber int
	// Name is the new name from --name, or nil to keep the current name.
	Name *string
	// Body is the new content from --body, or nil when --body wasn't given.
	Body *string
	// BodyFile is the file --body-file reads the new content from, "-" for
	// standard input, or empty when --body-file wasn't given. Result lines
	// show it as the source.
	BodyFile string
	// RestoreVersion is the version from the edit history to restore with
	// --restore-version, or 0 when it wasn't given. A restore takes its name
	// and body from that version, so no other flag goes with it.
	RestoreVersion int

	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
}

// NewCmdEdit creates the gh issue artifact edit command.
func NewCmdEdit(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "edit {<issue> <artifact-number> | <artifact-url>}",
		Short: "编辑一个 issue 产物 (预览版)",
		Long: heredoc.Docf(`
			重命名 issue 产物或替换其内容。类型不会改变，因此链接的新内容必须是 http(s) URL 或 %[1]s.url%[1]s 互联网快捷方式文件。

			按 issue 和编号或其 URL 命名该产物。%[1]s<issue>%[1]s 是一个 issue 或 URL。

			仅使用 %[1]s--attach%[1]s，附件将被追加到当前内容中。

			使用 %[1]s--restore-version%[1]s，可以将旧版本的名称和内容保存为新版本。%[1]sgh issue artifact view%[1]s
			可查看编辑历史。
		`, "`"),
		Example: heredoc.Doc(`
			# 重命名一个产物
			$ gh issue artifact edit 142 2 --name 'OAuth callback plan v2'

			# 用文件替换文档内容
			$ gh issue artifact edit 142 2 --body-file 2-OAuth-callback-plan.md

			# 将链接指向新的 URL
			$ gh issue artifact edit 142 3 --body https://github.com/monalisa/monas-cafe/wiki/OAuth-runbook-v2

			# 在文档中添加一张图片
			$ gh issue artifact edit 142 2 --attach ./signin-flow.png

			# 恢复早期版本
			$ gh issue artifact edit 142 2 --restore-version 5
		`),
		Args: cmdutil.RangeArgs(1, 2, shared.MissingArtifactMessage),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			opts.IssueNumber, opts.BaseRepo, opts.ArtifactNumber, err = shared.ParseArtifactArg(args, f.BaseRepo)
			if err != nil {
				return cmdutil.FlagErrorWrap(err)
			}

			// An empty --body-file names no file, as in gh issue edit.
			editing := opts.Name != nil || opts.Body != nil || opts.BodyFile != "" || opts.AttachFlag.Changed()
			restoring := cmd.Flags().Changed("restore-version")
			if restoring && editing {
				return cmdutil.FlagErrorf("`--restore-version` 标志不支持与 `--name`, `--body`, `--body-file`, `--attach` 一起使用")
			}
			if err := cmdutil.MutuallyExclusive("仅指定 `--body` 或 `--body-file` 其中之一即可", opts.Body != nil, opts.BodyFile != ""); err != nil {
				return err
			}
			switch {
			case restoring && opts.RestoreVersion < 1:
				return cmdutil.FlagErrorf("无效版本: %v", opts.RestoreVersion)
			case !restoring && !editing:
				return cmdutil.FlagErrorf("至少指定以下参数之一: `--name`, `--body`, `--body-file`, `--attach`, `--restore-version`")
			case opts.Name != nil && *opts.Name == "":
				return cmdutil.FlagErrorf("`--name` 不能为空")
			case opts.Body != nil && strings.TrimSpace(*opts.Body) == "":
				return cmdutil.FlagErrorf("`--body` 不能为空")
			}

			opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
			opts.Assets, err = opts.AttachFlag.UserAssets()
			if err != nil {
				return err
			}
			opts.Client = shared.ClientFunc(f)

			if runF != nil {
				return runF(opts)
			}
			return editRun(opts)
		},
	}

	cmdutil.NilStringFlag(cmd, &opts.Body, "body", "b", "新内容；用于链接，一个 URL")
	cmd.Flags().StringVarP(&opts.BodyFile, "body-file", "F", "", "从 `file` 读取新内容(使用 \"-\" 表示从标准输入读取)")
	cmdutil.NilStringFlag(cmd, &opts.Name, "name", "", "新名称")
	opts.AttachFlag = attachments.AddFlag(cmd)
	cmd.Flags().IntVar(&opts.RestoreVersion, "restore-version", 0, "从编辑历史中恢复带有此`编号`的版本")

	return cmd
}

func editRun(opts *EditOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}
	if err := shared.CheckHost(repo.RepoHost()); err != nil {
		return err
	}

	// The new content is read and checked before any request, so a file that
	// can't be used fails without one.
	content, dir, err := readContent(opts)
	if err != nil {
		return err
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}

	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		uploader, err = shared.NewUploader(c, repo, opts.IssueNumber, opts.HttpClient)
		if err != nil {
			return err
		}
	} else if err := shared.CheckIssue(c, repo, opts.IssueNumber); err != nil {
		return err
	}

	action := fmt.Sprintf("更新产物 %d", opts.ArtifactNumber)
	if opts.RestoreVersion > 0 {
		action = fmt.Sprintf("恢复产物 %d", opts.ArtifactNumber)
	}
	result := shared.Result{Number: opts.ArtifactNumber, Source: opts.BodyFile}
	current, err := c.Get(repo, opts.IssueNumber, opts.ArtifactNumber)
	if err != nil {
		shared.PrintFailure(opts.IO, result, action, err)
		return cmdutil.SilentError
	}
	result.Name = text.RemoveExcessiveWhitespace(current.Name)

	// The type decides what the new content can be, so every check that
	// depends on it runs before anything is uploaded or saved.
	if err := shared.CheckType(current.Artifact); err != nil {
		return err
	}
	if opts.RestoreVersion > 0 {
		return restoreVersion(opts, c, repo, current, result, action)
	}
	body, err := newBody(opts, current.Artifact, content)
	if err != nil {
		return err
	}

	var uploadErr error
	if uploader != nil {
		// A --body-file's references resolve against its own directory first.
		// Other content has no directory of its own, so its references
		// resolve against the working directory.
		md, uploadResult, err := uploader.UploadAndAttach(context.Background(), *body, dir, opts.Assets)
		opts.AttachEvent.RecordOperations(uploadResult)
		// Uploads can't be undone, so the artifact is saved if any file
		// uploaded, even when another failed. It isn't saved when none did.
		if err != nil && uploadResult.Uploaded == 0 {
			shared.PrintFailure(opts.IO, result, action, err)
			return cmdutil.SilentError
		}
		body, uploadErr = &md, err
	}

	updated, err := c.Update(repo, opts.IssueNumber, opts.ArtifactNumber, opts.Name, body)
	if err != nil {
		shared.PrintFailure(opts.IO, result, action, err)
		return cmdutil.SilentError
	}

	result.Name = text.RemoveExcessiveWhitespace(updated.Name)
	message := fmt.Sprintf("已更新产物 %d (%s) 在 %s#%d", opts.ArtifactNumber, result.Name, ghrepo.FullName(repo), opts.IssueNumber)
	if uploadErr != nil {
		shared.PrintPartialFailure(opts.IO, "updated", result, message, uploadErr)
		return cmdutil.SilentError
	}
	shared.PrintSuccess(opts.IO, "updated", result, opts.IO.ColorScheme().SuccessIcon(), message)
	return nil
}

// restoreVersion saves the name and body of version opts.RestoreVersion as a
// new version. They are sent as the API returned them, so the server checks
// them like any other edit. Every save adds a version to the edit history,
// even when nothing changed, so nothing is saved when the artifact already
// matches the version.
func restoreVersion(opts *EditOptions, c client.ArtifactClient, repo ghrepo.Interface, current *client.ArtifactWithVersions, result shared.Result, action string) error {
	v, err := shared.FindVersion(current, opts.RestoreVersion)
	if err != nil {
		return err
	}
	// FindVersion returns nil for the current version, which the artifact
	// matches.
	if v == nil || (v.Name == current.Name && v.Body == current.Body) {
		message := fmt.Sprintf("产物 %d (%s) 在 %s#%d 上已匹配版本 %d", opts.ArtifactNumber, result.Name, ghrepo.FullName(repo), opts.IssueNumber, opts.RestoreVersion)
		shared.PrintUnchanged(opts.IO, result, message, fmt.Sprintf("已匹配版本 %d", opts.RestoreVersion))
		return nil
	}

	updated, err := c.Update(repo, opts.IssueNumber, opts.ArtifactNumber, &v.Name, &v.Body)
	if err != nil {
		shared.PrintFailure(opts.IO, result, action, err)
		return cmdutil.SilentError
	}

	result.Name = text.RemoveExcessiveWhitespace(updated.Name)
	message := fmt.Sprintf("已经产物 %[1]d (%[2]s) 恢复到 %[4]s#%[5]d 上的 %[3]d 版本", opts.ArtifactNumber, result.Name, opts.RestoreVersion, ghrepo.FullName(repo), opts.IssueNumber)
	shared.PrintSuccess(opts.IO, "restored", result, opts.IO.ColorScheme().SuccessIcon(), message)
	return nil
}

// readContent returns the new content from --body or --body-file, or nil
// when neither was given, and the directory a --body-file's references
// resolve against, which is empty for any other content. Standard input is
// only read with --body-file -, so a stdin that never closes can't hang the
// command.
func readContent(opts *EditOptions) (*string, string, error) {
	if opts.BodyFile == "" {
		return opts.Body, "", nil
	}
	content, dir, err := shared.ReadContent(opts.BodyFile, opts.IO.In)
	if err != nil {
		source := opts.BodyFile
		if source == "-" {
			source = "standard input"
		}
		return nil, "", fmt.Errorf("从 %[2]s 读取产物 %[1]d 的新内容失败: %[3]w", opts.ArtifactNumber, source, err)
	}
	return &content, dir, nil
}

// newBody returns the body to save, checked against the artifact's type, or
// nil to keep the current body. Only a type that supports attachments takes
// --attach, and links and HTML documents don't. A link's new content must be
// an http(s) URL: a .url file is read as a shortcut, and anything else must be
// a bare URL, with its surrounding whitespace trimmed. Any other artifact is a
// Markdown or HTML document, which never takes a .url file. With only
// --attach, the files are appended to a Markdown document's current body.
func newBody(opts *EditOptions, a client.Artifact, content *string) (*string, error) {
	if len(opts.Assets) > 0 && !shared.SupportsAttachments(a.Type) {
		if a.Type == client.TypeEmbeddedHTML {
			return nil, errors.New("`--attach` 不能与 HTML 工件一起使用")
		}
		return nil, fmt.Errorf("产物 %d 是一个 %s；`--attach` 不能与 %s 类型的产物一起使用", a.Number, a.Type, a.Type)
	}
	if a.Type != client.TypeLink {
		if shared.IsShortcutFile(opts.BodyFile) {
			return nil, fmt.Errorf("产物 %d 不是链接；.url 文件只能更新链接产物", a.Number)
		}
		if content == nil && len(opts.Assets) > 0 {
			return &a.Body, nil
		}
		return content, nil
	}

	if content == nil {
		return nil, nil
	}
	if shared.IsShortcutFile(opts.BodyFile) {
		u, err := shared.ShortcutURL(*content)
		if errors.Is(err, shared.ErrNoShortcutSection) {
			return nil, fmt.Errorf("%s: %w；使用 `--body` 传递 URL 代替", opts.BodyFile, err)
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", opts.BodyFile, err)
		}
		return &u, nil
	}
	u, ok := shared.HTTPURL(*content)
	if !ok {
		return nil, fmt.Errorf("产物 %d 是一个链接；请使用 `--body` 传递 http(s) URL，或使用 `--body-file` 传递 .url 文件", a.Number)
	}
	return &u, nil
}
