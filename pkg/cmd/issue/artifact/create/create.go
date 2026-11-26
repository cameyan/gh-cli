package create

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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

// CreateOptions holds the flags and dependencies of gh issue artifact create.
type CreateOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Client     func() (client.ArtifactClient, error)

	IssueNumber int
	// Sources are the artifacts to create, in argument order, which is the
	// order they are created in.
	Sources []source
	// Type is the type of every document, from --type. Empty gives each
	// document the type its source implies: embedded_html for a .html or .htm
	// file, and generic otherwise.
	Type string

	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
}

// sourceKind is where an artifact's content comes from.
type sourceKind int

const (
	// fromFile is a file argument, or --body-file with a path.
	fromFile sourceKind = iota
	// fromURL is a URL argument.
	fromURL
	// fromStdin is --body-file -.
	fromStdin
	// fromBodyFlag is --body.
	fromBodyFlag
)

// source is one artifact to create, as the command line gave it.
type source struct {
	kind sourceKind
	// arg is the file path, URL or "-" as typed, which result lines show as
	// the source. It is empty for --body.
	arg string
	// name is the name typed with #<name> or --name, or empty for the name gh
	// gives it.
	name string
	// body is --body's text.
	body string
	// bodyFile reports whether a file came from --body-file, which takes its
	// path as typed, so its artifact can't be named with #<name>.
	bodyFile bool
}

// isLink reports whether the source creates a link: a URL, or a .url file.
func (s source) isLink() bool {
	return s.kind == fromURL || (s.kind == fromFile && shared.IsShortcutFile(s.arg))
}

// artifactType returns the type of the source's artifact: link for a link,
// or for a document, the type from --type when typeFlag is set. Without it, a
// .html or .htm file is an HTML document, and any other document is generic.
func (s source) artifactType(typeFlag string) string {
	switch {
	case s.isLink():
		return client.TypeLink
	case typeFlag != "":
		return typeFlag
	case s.kind == fromFile && shared.IsHTMLFile(s.arg):
		return client.TypeEmbeddedHTML
	}
	return client.TypeGeneric
}

// NewCmdCreate creates the gh issue artifact create command.
func NewCmdCreate(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	var body, bodyFile, name string

	cmd := &cobra.Command{
		Use:   "create {<issue-number> | <issue-url>} [<file>[#<name>] | <url>]...",
		Short: "创建 issue 产物 (预览版)",
		Long: heredoc.Docf(`
			为每个文件或 URL 创建一个项目。文本文件会生成一个以文件名命名的文档；若要使用其他名称，
			可添加 %[1]s#<name>%[1]s。%[1]s.html%[1]s 或 %[1]s.htm%[1]s 文件会生成 HTML
			文档。URL 或 %[1]s.url%[1]s 互联网快捷方式文件会生成链接；URL 的名称以其主机名命名。

			产物名称不能包含某些字符，例如括号。当文件名包含这些字符时，它们会被移除: %[1]splan (1).md%[1]s
			会被重命名为 %[1]splan 1.md%[1]s。

			使用 %[1]s--body%[1]s 从文本创建单个产物，或使用 %[1]s--body-file%[1]s 从文件创
			建单个产物(%[1]s-%[1]s 读取标准输入)。%[1]s--name%[1]s 用于设置其名称。

			产物保存为 %[1]sgeneric%[1]s，HTML 产物保存为 %[1]sembedded_html%[1]s。请改用
			%[1]s--type%[1]s 来设置每个产物的类型，无论文件扩展名如何。目前计划的行为与通用产物
			相同。
		`, "`"),
		Example: heredoc.Doc(`
			# 根据文件创建产物
			$ gh issue artifact create 142 signin-plan.md

			# 根据两个文件创建产物，并为第一个产物命名
			$ gh issue artifact create 142 'docs/signin-plan.md#Sign-in plan' oauth-research.md

			# 根据文件创建 HTML 产物
			$ gh issue artifact create 142 menu-prototype.html

			# 创建带名称的链接
			$ gh issue artifact create 142 https://github.com/monalisa/monas-cafe/wiki/OAuth-runbook --name 'Staging OAuth runbook'

			# 根据标准输入创建产物
			$ cat order-sync.log | gh issue artifact create 142 --body-file - --name 'Order sync log'

			# 上传产物引用的图片
			$ gh issue artifact create 142 signin-plan.md --attach ./signin-flow.png
		`),
		Aliases: []string{"new"},
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			opts.IssueNumber, opts.BaseRepo, err = shared.ParseIssueArg(args[0], f.BaseRepo)
			if err != nil {
				return cmdutil.FlagErrorWrap(err)
			}

			// An empty --body-file names no file, as in gh issue create.
			bodySet := cmd.Flags().Changed("body")
			bodyFileSet := bodyFile != ""
			nameSet := cmd.Flags().Changed("name")
			if err := cmdutil.MutuallyExclusive("仅指定 `--body` 或 `--body-file` 中的一个即可", bodySet, bodyFileSet); err != nil {
				return err
			}

			fileArgs := args[1:]
			switch {
			case len(fileArgs) == 0 && !bodySet && !bodyFileSet:
				return cmdutil.FlagErrorf("指定一个文件或 URL，或使用 `--body` 或 `--body-file`")
			case len(fileArgs) > 0 && (bodySet || bodyFileSet):
				return cmdutil.FlagErrorf("指定文件或 `--body` 和 `--body-file` 中的一个，但不能同时指定两者")
			case nameSet && len(fileArgs) > 1:
				return cmdutil.FlagErrorf("`--name` 只能在创建单个产物时使用")
			case nameSet && name == "":
				return cmdutil.FlagErrorf("`--name` 不能为空")
			}

			// Standard input is only read with --body-file -, so a stdin that
			// never closes can't hang the command.
			switch {
			case bodySet || bodyFile == "-":
				if !nameSet {
					return cmdutil.FlagErrorf("`--name` 必须与 `--body` 或 `--body-file -` 一起使用")
				}
				if !bodySet {
					opts.Sources = []source{{kind: fromStdin, arg: "-", name: name}}
					break
				}
				if strings.TrimSpace(body) == "" {
					return cmdutil.FlagErrorf("`--body` 不能为空")
				}
				opts.Sources = []source{{kind: fromBodyFlag, name: name, body: body}}
			case bodyFileSet:
				opts.Sources = []source{{kind: fromFile, arg: bodyFile, name: name, bodyFile: true}}
			default:
				for _, arg := range fileArgs {
					s, err := parseSource(arg)
					if err != nil {
						return err
					}
					opts.Sources = append(opts.Sources, s)
				}
				if nameSet {
					// Earlier validation guarantees exactly one positional source.
					if opts.Sources[0].name != "" {
						return cmdutil.FlagErrorf("使用 `#<name>` 或 `--name` 指定名称，但不能同时使用两者")
					}
					opts.Sources[0].name = name
				}
			}

			if cmd.Flags().Changed("type") && slices.ContainsFunc(opts.Sources, source.isLink) {
				return cmdutil.FlagErrorf("`--type` 仅适用于产物；链接是从 URL 和 .url 文件创建的")
			}
			// --attach needs at least one artifact whose type supports
			// attachments. Links and HTML documents don't, since --attach
			// only rewrites Markdown.
			if opts.AttachFlag.Changed() && !slices.ContainsFunc(opts.Sources, func(s source) bool { return shared.SupportsAttachments(s.artifactType(opts.Type)) }) {
				kind := opts.Sources[0].artifactType(opts.Type)
				if kind == client.TypeEmbeddedHTML {
					kind = "HTML"
				}
				return cmdutil.FlagErrorf("`--attach` 不能与 %s 产物一起使用", kind)
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
			return createRun(opts)
		},
	}

	cmd.Flags().StringVarP(&body, "body", "b", "", "单个产物的内容")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取单个产物的内容(使用 \"-\" 表示从标准输入读取)")
	cmd.Flags().StringVar(&name, "name", "", "单个产物的名称")
	cmdutil.StringEnumFlag(cmd, &opts.Type, "type", "", "", []string{client.TypeGeneric, client.TypePlan, client.TypeEmbeddedHTML}, "产物类型")
	opts.AttachFlag = attachments.AddFlag(cmd)

	return cmd
}

// parseSource reads one file or URL argument. An http:// or https:// argument
// is always a link, and any `#` stays part of the URL. Like every link, a URL
// is read with its surrounding whitespace trimmed, which also keeps it on one
// result line. Any other argument is a file, and a name can follow it after
// `#`, split the way --attach splits its alt text.
func parseSource(arg string) (source, error) {
	trimmed := strings.TrimSpace(arg)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return source{kind: fromURL, arg: trimmed}, nil
	}

	path, name := attachments.SplitFileArg(arg)
	// Only --body-file reads standard input, and "-" is how a result line
	// names it.
	if path == "-" {
		return source{}, cmdutil.FlagErrorf("无法从 `-` 创建产物；请使用 `--body-file -` 读取标准输入")
	}
	if path != arg && name == "" {
		return source{}, cmdutil.FlagErrorf("`#` 后的产物名称在 %q 中不能为空", arg)
	}
	return source{kind: fromFile, arg: path, name: name}, nil
}

// artifact is one artifact ready to create.
type artifact struct {
	source       source
	artifactType string
	name         string
	body         string
	// dir is the directory of the file body was read from, which --attach
	// resolves the body's Markdown references against first, or "" for
	// content from no file.
	dir string
	// attachErr is why some of the document's --attach files didn't upload.
	// skip is set when none of them did, so the document isn't saved.
	attachErr error
	skip      bool
}

func createRun(opts *CreateOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}
	if err := shared.CheckHost(repo.RepoHost()); err != nil {
		return err
	}

	// Every artifact is read and checked before any request, so a file that
	// can't be used fails the whole command and nothing is created.
	artifacts := make([]artifact, 0, len(opts.Sources))
	for _, s := range opts.Sources {
		a, err := readSource(s, opts.IO.In, opts.Type)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, a)
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}

	if len(opts.Assets) > 0 {
		uploader, err := shared.NewUploader(c, repo, opts.IssueNumber, opts.HttpClient)
		if err != nil {
			return err
		}
		if err := attach(opts, uploader, artifacts); err != nil {
			return err
		}
	} else if err := shared.CheckIssue(c, repo, opts.IssueNumber); err != nil {
		return err
	}

	issueRef := fmt.Sprintf("%s#%d", ghrepo.FullName(repo), opts.IssueNumber)
	cs := opts.IO.ColorScheme()
	failed := false
	for _, a := range artifacts {
		result := shared.Result{Name: text.RemoveExcessiveWhitespace(a.name), Source: a.source.arg}
		if a.skip {
			shared.PrintFailure(opts.IO, result, createAction(a), a.attachErr)
			failed = true
			continue
		}

		created, err := c.Create(repo, opts.IssueNumber, a.artifactType, a.name, a.body)
		if err != nil {
			shared.PrintFailure(opts.IO, result, createAction(a), err)
			failed = true
			continue
		}

		result.Number = created.Number
		result.Name = text.RemoveExcessiveWhitespace(created.Name)
		message := fmt.Sprintf("在 %s 上创建了产物 %d (%s) on %s", issueRef, created.Number, result.Name)
		if a.attachErr != nil {
			shared.PrintPartialFailure(opts.IO, "created", result, message, a.attachErr)
			failed = true
			continue
		}
		shared.PrintSuccess(opts.IO, "created", result, cs.SuccessIcon(), message)
	}

	if failed {
		return cmdutil.SilentError
	}
	return nil
}

// readSource reads an artifact's content and works out its type and name.
// typeFlag is the type from --type, or empty without it.
func readSource(s source, stdin io.ReadCloser, typeFlag string) (artifact, error) {
	a := artifact{source: s, artifactType: s.artifactType(typeFlag), name: s.name}
	switch s.kind {
	case fromBodyFlag:
		a.body = s.body
	case fromStdin:
		body, _, err := shared.ReadContent(s.arg, stdin)
		if err != nil {
			return a, fmt.Errorf("无法从标准输入读取产物内容: %w", err)
		}
		a.body = body
	case fromURL:
		u, ok := shared.HTTPURL(s.arg)
		if !ok {
			return a, fmt.Errorf("无效的链接 URL %q: 必须是一个包含主机的 http(s) URL，且只能使用可打印的 ASCII 字符，不能包含空格。", s.arg)
		}
		a.body = u
		if a.name == "" {
			// The server rejects the ":" and "/" of a whole URL in a name.
			parsed, err := url.Parse(u)
			if err != nil {
				return a, err
			}
			a.name = parsed.Hostname()
		}
	case fromFile:
		content, dir, err := shared.ReadContent(s.arg, stdin)
		if err != nil {
			return a, fmt.Errorf("无法从 %s 读取产物内容: %w", s.arg, err)
		}
		if shared.IsShortcutFile(s.arg) {
			u, err := shared.ShortcutURL(content)
			if errors.Is(err, shared.ErrNoShortcutSection) {
				return a, fmt.Errorf("%s: %w；改为将 URL 作为参数传递", s.arg, err)
			} else if err != nil {
				return a, fmt.Errorf("%s: %w", s.arg, err)
			}
			a.body = u
		} else {
			a.body, a.dir = content, dir
		}
		if a.name == "" {
			a.name = nameFromFile(s.arg)
			if a.name == "" {
				hint := "使用 `#<name>` 或 `--name` 为其命名"
				if s.bodyFile {
					hint = "使用 `--name` 命名"
				}
				return a, fmt.Errorf("无法从 %s 中推导出产物名称: 文件名中没有字母或数字；%s", s.arg, hint)
			}
		}
	}
	return a, nil
}

// nameFromFile returns the name gh gives an artifact from a file: its base
// name, as typed rather than a symlink's target, cleaned up to fit the
// server's rule for names, since the user didn't type it. Names may contain
// letters, numbers, accent marks, spaces, ".", "-" and "_", and must start
// and end with a letter or number. Other characters are removed, then the
// ends are trimmed, so "plan (1).md" becomes "plan 1.md" and "_draft.md"
// becomes "draft.md". The result is empty when the file name has no letters
// or numbers.
func nameFromFile(path string) string {
	name := strings.Map(func(r rune) rune {
		if isLetterOrNumber(r) || unicode.IsMark(r) || strings.ContainsRune(" .-_", r) {
			return r
		}
		return -1
	}, filepath.Base(path))
	return strings.TrimFunc(name, func(r rune) bool { return !isLetterOrNumber(r) })
}

func isLetterOrNumber(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// attach uploads the --attach files, each once, and points every Markdown
// document at the files it references. Artifacts whose type doesn't support
// attachments, such as links and HTML documents, are left out. With one
// Markdown document, the files it doesn't reference are appended to it.
//
// Uploads can't be undone, so a document is saved if any of its files
// uploaded, even when another failed, and that failure is the reason on its
// result line. A document none of whose files uploaded isn't saved.
func attach(opts *CreateOptions, uploader *attachments.Uploader, artifacts []artifact) error {
	var docs []attachments.Document
	var indexes []int
	for i, a := range artifacts {
		if !shared.SupportsAttachments(a.artifactType) {
			continue
		}
		docs = append(docs, attachments.Document{Markdown: a.body, Dir: a.dir})
		indexes = append(indexes, i)
	}

	results, err := uploader.UploadAndAttachDocuments(context.Background(), docs, opts.Assets)
	var operations attachments.UploadResult
	for _, r := range results {
		operations.AppendOperations += r.AppendOperations
		operations.ReplaceOperations += r.ReplaceOperations
	}
	opts.AttachEvent.RecordOperations(operations)

	// Without results, the files were refused before anything uploaded.
	if len(results) == 0 {
		if unreferenced, ok := errors.AsType[*attachments.UnreferencedError](err); ok {
			return unreferencedError(unreferenced.Paths)
		}
		return err
	}

	for j, r := range results {
		a := &artifacts[indexes[j]]
		a.body = r.Markdown
		a.attachErr = r.Err
		a.skip = r.Err != nil && len(r.Uploaded) == 0
	}
	return nil
}

// unreferencedError explains attached files that no document references,
// when there are several documents and none to append them to.
func unreferencedError(paths []string) error {
	if len(paths) == 1 {
		return fmt.Errorf("无文件引用 %s；在文件中引用它，或在创建单个产物时将其附加", paths[0])
	}
	list := paths[0] + " or " + paths[1]
	if len(paths) > 2 {
		list = strings.Join(paths[:len(paths)-1], ", ") + ", or " + paths[len(paths)-1]
	}
	return fmt.Errorf("无文件引用 %s；在文件中引用它们，或在创建单个产物时将其附加", list)
}

// createAction names an artifact in its failure line by what it was created
// from, or by its name when it came from --body.
func createAction(a artifact) string {
	switch a.source.kind {
	case fromStdin:
		return "从标准输入创建产物"
	case fromBodyFlag:
		return fmt.Sprintf("创建产物 (%s)", text.RemoveExcessiveWhitespace(a.name))
	}
	return "从 " + a.source.arg + "创建产物"
}
