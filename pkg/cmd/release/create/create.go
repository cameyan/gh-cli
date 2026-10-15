package create

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
	"github.com/spf13/cobra"
)

type iprompter interface {
	Select(string, string, []string) (int, error)
	Input(string, string) (string, error)
	Confirm(string, bool) (bool, error)
}

type CreateOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	BaseRepo   func() (ghrepo.Interface, error)
	Edit       func(string, string, string, io.Reader, io.Writer, io.Writer) (string, error)
	Prompter   iprompter

	TagName      string
	Target       string
	Name         string
	Body         string
	BodyProvided bool
	Draft        bool
	Prerelease   bool
	IsLatest     *bool

	Assets []*shared.AssetForUpload

	// 交互式流程使用
	SubmitAction string
	// 交互式流程使用
	ReleaseNotesAction string
	// 来自 --repo flag 的值
	RepoOverride string
	// 同时上传的最大数量
	Concurrency        int
	DiscussionCategory string
	GenerateNotes      bool
	NotesStartTag      string
	VerifyTag          bool
	NotesFromTag       bool
	FailOnNoCommits    bool
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
		Edit:       surveyext.Edit,
	}

	var notesFile string

	cmd := &cobra.Command{
		DisableFlagsInUseLine: true,

		Use:   "create [<tag>] [<filename>... | <pattern>...]",
		Short: "创建新的发布(release)",
		Long: heredoc.Docf(`
			为仓库创建新的 GitHub Release。

			可以给定一个资产(asset)文件列表，上传到新发布中。要为资产定义显示标签(label)，
			可在文件名后追加以 %[1]s#%[1]s 开头的文本。

			如果匹配的 git 标签(tag)尚不存在，将基于默认分支的最新状态自动创建一个标签。
			使用 %[1]s--target%[1]s 可指向其他分支或提交，用于自动创建标签。
			使用 %[1]s--verify-tag%[1]s 可在标签不存在时中止发布。
			发布后要在本地获取新标签，可执行 %[1]sgit fetch --tags origin%[1]s。

			要基于附注(annotated) git 标签创建发布，请先在本地用 git 创建标签，
			将标签推送到 GitHub，然后再运行该命令。
			使用 %[1]s--notes-from-tag%[1]s 可从附注 git 标签获取发布说明。
			若标签未附注，则改用提交信息。

			使用 %[1]s--generate-notes%[1]s 可通过 GitHub Release Notes API 自动生成发布说明。
			使用自动生成的发布说明时，若未显式传入标题，也会自动生成发布标题。
			可通过 %[1]s--notes%[1]s flag 在自动生成的说明前追加额外的发布说明。

			默认情况下，即使自上个发布以来没有新提交，也会创建发布。
			这可能导致相同或重复的发布，在某些场景下可能不符合预期。
			使用 %[1]s--fail-on-no-commits%[1]s 可在没有新提交时报错。该 flag
			在不存在已有发布或这是首个发布时不生效。

			## 不可变发布(Immutable Releases)

			当仓库启用了发布不可变性(release immutability)后，将强制执行以下保护:
			- 与发布关联的 git 标签(tag)不可修改或删除。
			- 发布资产(asset)不可修改或删除。

			不可变性仅在发布(release)正式发布后才生效。草稿(draft)发布可以修改
			或删除，其关联的 git 标签也可以修改或删除。

			使用 %[1]screate%[1]s 命令向发布附加资产时，会分别发起 API 调用:
			先以草稿形式创建发布，上传资产，然后再正式发布该发布。
			不可变性保护仅在发布正式发布后才会强制执行。
		`, "`"),
		Example: heredoc.Doc(`
			# 交互式创建发布(release)
			$ gh release create

			# 基于指定标签(tag)交互式创建发布
			$ gh release create v1.2.3

			# 非交互式创建发布(release)
			$ gh release create v1.2.3 --notes "bugfix release"

			# 使用通过 GitHub Release Notes API 自动生成的发布说明
			$ gh release create v1.2.3 --generate-notes

			# 使用文件中的发布说明
			$ gh release create v1.2.3 -F release-notes.md

			# 使用标签附注(tag annotation)或关联提交信息作为发布说明
			$ gh release create v1.2.3 --notes-from-tag

			# 不将该发布标记为最新(latest)
			$ gh release create v1.2.3 --latest=false

			# 将目录中的所有 tarball 作为发布资产(asset)上传
			$ gh release create v1.2.3 ./dist/*.tgz

			# 上传带显示标签(label)的发布资产
			$ gh release create v1.2.3 '/path/to/asset.zip#My display label'

			# 创建发布并发起讨论(discussion)
			$ gh release create v1.2.3 --discussion-category "General"

			# 仅当自上个发布以来有新提交时才创建发布(release)
			$ gh release create v1.2.3 --fail-on-no-commits
		`),
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("discussion-category") && opts.Draft {
				return errors.New("不支持为草稿(draft)发布创建讨论(discussion)")
			}

			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo
			opts.RepoOverride, _ = cmd.Flags().GetString("repo")

			var err error

			if len(args) > 0 {
				opts.TagName = args[0]
				opts.Assets, err = shared.AssetsFromArgs(args[1:])
				if err != nil {
					return err
				}
			}

			if opts.TagName == "" && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须提供标签(tag)")
			}

			if opts.NotesFromTag && (opts.GenerateNotes || opts.NotesStartTag != "") {
				return cmdutil.FlagErrorf("`--notes-from-tag` 不支持与 `--generate-notes` 或 `--notes-start-tag` 同时使用")
			}

			if opts.NotesFromTag && opts.RepoOverride != "" {
				return cmdutil.FlagErrorf("`--notes-from-tag` 不支持与 `--repo` 同时使用")
			}

			opts.Concurrency = 5

			opts.BodyProvided = cmd.Flags().Changed("notes") || opts.GenerateNotes || opts.NotesFromTag
			if notesFile != "" {
				b, err := cmdutil.ReadFile(notesFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.Body = string(b)
				opts.BodyProvided = true
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Draft, "draft", "d", false, "将发布保存为草稿(draft)而不是正式发布")
	cmd.Flags().BoolVarP(&opts.Prerelease, "prerelease", "p", false, "将发布标记为预发布(pre-release)")
	cmd.Flags().StringVar(&opts.Target, "target", "", "目标 `branch` 或完整提交 SHA (默认 [main 分支])")
	cmd.Flags().StringVarP(&opts.Name, "title", "t", "", "发布标题")
	cmd.Flags().StringVarP(&opts.Body, "notes", "n", "", "发布说明(release notes)")
	cmd.Flags().StringVarP(&notesFile, "notes-file", "F", "", "从 `file` 读取发布说明 (使用 \"-\" 从标准输入读取)")
	cmd.Flags().StringVarP(&opts.DiscussionCategory, "discussion-category", "", "", "在指定分类下发起讨论(discussion)")
	cmd.Flags().BoolVarP(&opts.GenerateNotes, "generate-notes", "", false, "通过 GitHub Release Notes API 自动生成发布(release)的标题和说明")
	cmd.Flags().StringVar(&opts.NotesStartTag, "notes-start-tag", "", "用作生成发布说明起点的标签(tag)")
	cmdutil.NilBoolFlag(cmd, &opts.IsLatest, "latest", "", "将该发布标记为\"最新(Latest)\" (默认 [基于日期和版本自动判断])。--latest=false 表示显式不设为最新")
	cmd.Flags().BoolVarP(&opts.VerifyTag, "verify-tag", "", false, "若 git 标签(tag)在远端仓库中不存在则中止")
	cmd.Flags().BoolVarP(&opts.NotesFromTag, "notes-from-tag", "", false, "从标签附注(tag annotation)或标签关联提交的信息中获取发布说明")
	cmd.Flags().BoolVar(&opts.FailOnNoCommits, "fail-on-no-commits", false, "自上个发布以来没有提交时报错(对首个发布无影响)")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "target")

	return cmd
}

func createRun(opts *CreateOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.FailOnNoCommits {
		isNew, err := isNewRelease(httpClient, baseRepo)
		if err != nil {
			return fmt.Errorf("检查自上个发布(release)以来是否有新提交失败: %v", err)
		}
		if !isNew {
			return fmt.Errorf("自上个发布(release)以来没有新提交")
		}
	}

	var existingTag bool
	if opts.TagName == "" {
		tags, err := getTags(httpClient, baseRepo, 5)
		if err != nil {
			return err
		}

		if len(tags) != 0 {
			options := make([]string, len(tags))
			for i, tag := range tags {
				options[i] = tag.Name
			}
			createNewTagOption := "创建新标签(tag)"
			options = append(options, createNewTagOption)
			selected, err := opts.Prompter.Select("选择一个标签(tag)", options[0], options)
			if err != nil {
				return fmt.Errorf("无法显示提示: %w", err)
			}
			tag := options[selected]
			if tag != createNewTagOption {
				existingTag = true
				opts.TagName = tag
			}
		}

		if opts.TagName == "" {
			opts.TagName, err = opts.Prompter.Input("标签(tag)名", "")
			if err != nil {
				return fmt.Errorf("无法显示提示: %w", err)
			}
			opts.TagName = strings.TrimSpace(opts.TagName)
		}
	}

	if opts.VerifyTag && !existingTag {
		remoteTagPresent, err := remoteTagExists(httpClient, baseRepo, opts.TagName)
		if err != nil {
			return err
		}
		if !remoteTagPresent {
			return fmt.Errorf("标签(tag) %s 在仓库 %s 中不存在，因 --verify-tag flag 而中止",
				opts.TagName, ghrepo.FullName(baseRepo))
		}
	}

	var tagDescription string
	if opts.RepoOverride == "" {
		tagDescription, _ = gitTagInfo(opts.GitClient, opts.TagName)

		if opts.NotesFromTag && tagDescription == "" {
			return fmt.Errorf("无法从标签(tag) %s 生成发布说明，因为它在本地不存在",
				opts.TagName)
		}

		// 如果存在与指定名称相同的本地标签(tag)，
		// 用户可能不希望在远端创建新标签，因为本地标签可能是附注的或已签名的。
		// 如果用户指定了 target，则视为显式指示，无论本地标签状态如何，
		// 都在远端创建指向该 target 的新标签。
		// 如果远端已存在与指定名称相同的标签(tag)，则不会创建新标签，因此忽略本地标签状态。
		if tagDescription != "" && !existingTag && opts.Target == "" && !opts.VerifyTag {
			remoteExists, err := remoteTagExists(httpClient, baseRepo, opts.TagName)
			if err != nil {
				return err
			}
			if !remoteExists {
				return fmt.Errorf("标签(tag) %s 已存在于本地但尚未推送到 %s，请先推送它，或指定 `--target` flag 以创建新标签",
					opts.TagName, ghrepo.FullName(baseRepo))
			}
		}
	}

	if !opts.BodyProvided && opts.IO.CanPrompt() {
		editorCommand, err := cmdutil.DetermineEditor(opts.Config)
		if err != nil {
			return err
		}

		var generatedNotes *releaseNotes
		var generatedChangelog string

		generatedNotes, err = generateReleaseNotes(httpClient, baseRepo, opts.TagName, opts.Target, opts.NotesStartTag)
		if err != nil && !errors.Is(err, notImplementedError) {
			return err
		}

		if opts.RepoOverride == "" {
			headRef := opts.TagName
			if tagDescription == "" {
				if opts.Target != "" {
				// TODO: 使用分支 ref 的远端跟踪(remote-tracking)版本
				headRef = opts.Target
				} else {
					headRef = "HEAD"
				}
			}
			if generatedNotes == nil {
				if opts.NotesStartTag != "" {
					commits, _ := changelogForRange(opts.GitClient, fmt.Sprintf("%s..%s", opts.NotesStartTag, headRef))
					generatedChangelog = generateChangelog(commits)
				} else if prevTag, err := detectPreviousTag(opts.GitClient, headRef); err == nil {
					commits, _ := changelogForRange(opts.GitClient, fmt.Sprintf("%s..%s", prevTag, headRef))
					generatedChangelog = generateChangelog(commits)
				}
			}
		}

		editorOptions := []string{"自己编写"}
		if generatedNotes != nil {
			editorOptions = append(editorOptions, "以生成的发布说明为模板编写")
		}
		if generatedChangelog != "" {
			editorOptions = append(editorOptions, "以提交日志为模板编写")
		}
		if tagDescription != "" {
			editorOptions = append(editorOptions, "以 git 标签(tag)信息为模板编写")
		}
		editorOptions = append(editorOptions, "留空")

		defaultName := opts.Name
		if defaultName == "" && generatedNotes != nil {
			defaultName = generatedNotes.Name
		}

		opts.Name, err = opts.Prompter.Input("标题(可选)", defaultName)
		if err != nil {
			return fmt.Errorf("无法显示提示: %w", err)
		}

		selected, err := opts.Prompter.Select("发布说明(release notes)", "", editorOptions)
		if err != nil {
			return fmt.Errorf("无法显示提示: %w", err)
		}
		opts.ReleaseNotesAction = editorOptions[selected]

		var openEditor bool
		var editorContents string

		switch opts.ReleaseNotesAction {
		case "自己编写":
			openEditor = true
		case "以生成的发布说明为模板编写":
			openEditor = true
			editorContents = generatedNotes.Body
		case "以提交日志为模板编写":
			openEditor = true
			editorContents = generatedChangelog
		case "以 git 标签(tag)信息为模板编写":
			openEditor = true
			editorContents = tagDescription
		case "留空":
			openEditor = false
		default:
			return fmt.Errorf("无效的操作: %v", opts.ReleaseNotesAction)
		}

		if openEditor {
			text, err := opts.Edit(editorCommand, "*.md", editorContents,
				opts.IO.In, opts.IO.Out, opts.IO.ErrOut)
			if err != nil {
				return err
			}
			opts.Body = text
		}

		saveAsDraft := "保存为草稿(draft)"
		publishRelease := "正式发布(release)"
		defaultSubmit := publishRelease
		if opts.Draft {
			defaultSubmit = saveAsDraft
		}

		opts.Prerelease, err = opts.Prompter.Confirm("这是预发布(pre-release)吗?", opts.Prerelease)
		if err != nil {
			return err
		}

		options := []string{publishRelease, saveAsDraft, "取消"}
		selected, err = opts.Prompter.Select("提交?", defaultSubmit, options)
		if err != nil {
			return fmt.Errorf("无法显示提示: %w", err)
		}

		opts.SubmitAction = options[selected]

		switch opts.SubmitAction {
		case "正式发布(release)":
			opts.Draft = false
		case "保存为草稿(draft)":
			opts.Draft = true
		case "取消":
			return cmdutil.CancelError
		default:
			return fmt.Errorf("无效的操作: %v", opts.SubmitAction)
		}
	}

	params := map[string]any{
		"tag_name":   opts.TagName,
		"draft":      opts.Draft,
		"prerelease": opts.Prerelease,
	}
	if opts.Name != "" {
		params["name"] = opts.Name
	}
	if opts.Body != "" {
		params["body"] = opts.Body
	}
	if opts.Target != "" {
		params["target_commitish"] = opts.Target
	}
	if opts.IsLatest != nil {
		// 有效取值: true/false/legacy
		params["make_latest"] = fmt.Sprintf("%v", *opts.IsLatest)
	}
	if opts.DiscussionCategory != "" {
		params["discussion_category_name"] = opts.DiscussionCategory
	}
	if opts.GenerateNotes {
		if opts.NotesStartTag != "" {
			generatedNotes, err := generateReleaseNotes(httpClient, baseRepo, opts.TagName, opts.Target, opts.NotesStartTag)
			if err != nil && !errors.Is(err, notImplementedError) {
				return err
			}
			if generatedNotes != nil {
				if opts.Body == "" {
					params["body"] = generatedNotes.Body
				} else {
					params["body"] = fmt.Sprintf("%s\n%s", opts.Body, generatedNotes.Body)
				}
				if opts.Name == "" {
					params["name"] = generatedNotes.Name
				}
			}
		} else {
			params["generate_release_notes"] = true
		}
	}
	if opts.NotesFromTag {
		if opts.Body == "" {
			params["body"] = tagDescription
		} else {
			params["body"] = fmt.Sprintf("%s\n%s", opts.Body, tagDescription)
		}
	}

	hasAssets := len(opts.Assets) > 0
	draftWhileUploading := false

	if hasAssets && !opts.Draft {
		// 检查是否已有发布(release)
		if opts.TagName != "" {
			if ok, err := publishedReleaseExists(httpClient, baseRepo, opts.TagName); err != nil {
				return fmt.Errorf("检查已有发布(release)出错: %w", err)
			} else if ok {
				return fmt.Errorf("已存在同名标签(tag)的发布(release): %s", opts.TagName)
			}
		}
		// 先将发布(release)保存为草稿(draft)，待所有资产(asset)上传完成后再正式发布
		draftWhileUploading = true
		params["draft"] = true
	}

	newRelease, err := createRelease(httpClient, baseRepo, params)

	if errMissingRequiredWorkflowScope, ok := errors.AsType[*errMissingRequiredWorkflowScope](err); ok {
		host := errMissingRequiredWorkflowScope.Hostname
		refreshInstructions := fmt.Sprintf("gh auth refresh -h %[1]s -s workflow", host)
		cs := opts.IO.ColorScheme()

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s 创建发布(release)失败，可能需要 \"workflow\" 权限范围(scope)。\n", cs.WarningIcon()))
		sb.WriteString(fmt.Sprintf("如需请求该权限，请运行:\n%s\n", cs.Bold(refreshInstructions)))
		fmt.Fprint(opts.IO.ErrOut, sb.String())

		return cmdutil.SilentError
	}

	if err != nil {
		return err
	}

	cleanupDraftRelease := func(err error) error {
		if !draftWhileUploading {
			return err
		}
		if cleanupErr := deleteRelease(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(newRelease.APIURL)); cleanupErr != nil {
			return fmt.Errorf("%w\n清理草稿(draft)失败: %v", err, cleanupErr)
		}
		return err
	}

	if hasAssets {
		uploadURL := newRelease.UploadURL
		if idx := strings.IndexRune(uploadURL, '{'); idx > 0 {
			uploadURL = uploadURL[:idx]
		}

		opts.IO.StartProgressIndicator()
		err = shared.ConcurrentUpload(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(uploadURL), opts.Concurrency, opts.Assets)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return cleanupDraftRelease(err)
		}

		if draftWhileUploading {
			rel, err := publishRelease(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(newRelease.APIURL), opts.DiscussionCategory, opts.IsLatest)
			if err != nil {
				return cleanupDraftRelease(err)
			}
			newRelease = rel
		}
	}

	fmt.Fprintf(opts.IO.Out, "%s\n", newRelease.URL)

	return nil
}

func gitTagInfo(client *git.Client, tagName string) (string, error) {
	contentCmd, err := client.Command(context.Background(), "tag", "--list", tagName, "--format=%(contents)")
	if err != nil {
		return "", err
	}
	content, err := contentCmd.Output()
	if err != nil {
		return "", err
	}

	// If there is a signature, we should strip it from the end of the content.
	// Note that, we can achieve this by looking for markers like "-----BEGIN PGP
	// SIGNATURE-----" and cut the remaining text from the content, but this is
	// not a safe approach, because, although unlikely, the content can contain
	// a signature-like section which we shouldn't leave it as is. So, we need
	// to get the tag signature as a whole, if any, and remote it from the content.
	signatureCmd, err := client.Command(context.Background(), "tag", "--list", tagName, "--format=%(contents:signature)")
	if err != nil {
		return "", err
	}
	signature, err := signatureCmd.Output()
	if err != nil {
		return "", err
	}

	if len(signature) == 0 {
		// The tag annotation content has no trailing signature to strip out,
		// so we return the entire content.
		return string(content), nil
	}

	body, _ := strings.CutSuffix(string(content), "\n"+string(signature))
	return body, nil
}

func detectPreviousTag(client *git.Client, headRef string) (string, error) {
	cmd, err := client.Command(context.Background(), "describe", "--tags", "--abbrev=0", fmt.Sprintf("%s^", headRef))
	if err != nil {
		return "", err
	}
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

type logEntry struct {
	Subject string
	Body    string
}

func changelogForRange(client *git.Client, refRange string) ([]logEntry, error) {
	cmd, err := client.Command(context.Background(), "-c", "log.ShowSignature=false", "log", "--first-parent", "--reverse", "--pretty=format:%B%x00", refRange)
	if err != nil {
		return nil, err
	}
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var entries []logEntry
	for cb := range bytes.SplitSeq(b, []byte{'\000'}) {
		c := strings.ReplaceAll(string(cb), "\r\n", "\n")
		c = strings.TrimPrefix(c, "\n")
		if len(c) == 0 {
			continue
		}
		parts := strings.SplitN(c, "\n\n", 2)
		var body string
		subject := strings.ReplaceAll(parts[0], "\n", " ")
		if len(parts) > 1 {
			body = parts[1]
		}
		entries = append(entries, logEntry{
			Subject: subject,
			Body:    body,
		})
	}

	return entries, nil
}

func generateChangelog(commits []logEntry) string {
	var parts []string
	for _, c := range commits {
		// TODO: consider rendering "Merge pull request #123 from owner/branch" differently
		parts = append(parts, fmt.Sprintf("* %s", c.Subject))
		if c.Body != "" {
			parts = append(parts, text.Indent(c.Body, "  "))
		}
	}
	return strings.Join(parts, "\n\n")
}
