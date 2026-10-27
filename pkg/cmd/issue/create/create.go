package create

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/browser"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/text"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/spf13/cobra"
)

type CreateOptions struct {
	HttpClient       func() (*http.Client, error)
	IO               *iostreams.IOStreams
	BaseRepo         func() (ghrepo.Interface, error)
	Browser          browser.Browser
	Prompter         prShared.Prompt
	Detector         fd.Detector
	TitledEditSurvey func(string, string) (string, string, error)

	RootDirOverride string

	HasRepoOverride bool
	EditorMode      bool
	WebMode         bool
	RecoverFile     string

	Title       string
	Body        string
	Interactive bool

	Assignees []string
	Labels    []string
	Projects  []string
	Milestone string
	Template  string

	IssueType   string
	issueTypeID string // 在交互式流程中解析，以避免重复的 API 调用
	Parent      string
	BlockedBy   []string
	Blocking    []string

	BodyDir     string
	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
}

func NewCmdCreate(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Prompter:   f.Prompter,

		TitledEditSurvey: prShared.TitledEditSurvey(&prShared.UserEditor{Config: f.Config, IO: f.IOStreams}),
	}

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建新 issue",
		Long: heredoc.Docf(`
			在 GitHub 上创建 issue。

			Use %[1]s--attach%[1]s to upload an image or video. The attachment is appended to the
			body. If the body references an attached file, such as %[1]s![alt](./login.png)%[1]s, that
			reference is rewritten to point at the uploaded asset instead. Paths in a
			%[1]s--body-file%[1]s are relative to its directory, falling back to the working directory.
			You can attach up to 50 files per command.

			图片的替代文本会跟在 %[1]s#%[1]s 之后的路径中，如 %[1]s--attach './login.png#The login error state'%[1]s。
			若不提供，则使用文件名。正文中已有的引用会保留写在其处的替代文本。视频会渲染为播放器且没有替代文本，因此无法为其指定。

			如果部分附件上传成功、部分失败，仍会用成功的附件创建 issue。命令随后会以非零状态退出，但新 issue 的 URL 仍会打印到
			标准输出。

			将 issue 添加到项目需要具备 %[1]sproject%[1]s 范围的授权。如需授权，请运行 %[1]sgh auth refresh -s project%[1]s。

			%[1]s--assignee%[1]s 选项支持以下特殊值:
			- %[1]s@me%[1]s: 将自己指派为负责人
			- %[1]s@copilot%[1]s: 指派 Copilot(在 GitHub 企业版服务器上不受支持)
		`, "`"),
		Example: heredoc.Doc(`
			$ gh issue create --title "I found a bug" --body "Nothing works"
			$ gh issue create --attach './login.png#The login error state'
			$ gh issue create --attach ./before.png --attach ./after.png
			$ gh issue create --label "bug,help wanted"
			$ gh issue create --label bug --label "help wanted"
			$ gh issue create --assignee monalisa,hubot
			$ gh issue create --assignee "@me"
			$ gh issue create --assignee "@copilot"
			$ gh issue create --project "Roadmap"
			$ gh issue create --template "Bug Report"
			$ gh issue create --type Bug
			$ gh issue create --parent 100
			$ gh issue create --parent https://github.com/cli/go-gh/issues/42
			$ gh issue create --blocked-by 200,201 --blocking 300
		`),
		Args:    cmdutil.NoArgsQuoteReminder,
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			// 支持 `-R, --repo` 覆盖
			opts.BaseRepo = f.BaseRepo
			opts.HasRepoOverride = cmd.Flags().Changed("repo")

			var err error
			opts.EditorMode, err = prShared.InitEditorMode(f, opts.EditorMode, opts.WebMode, opts.IO.CanPrompt())
			if err != nil {
				return err
			}

			titleProvided := cmd.Flags().Changed("title")
			bodyProvided := cmd.Flags().Changed("body")
			if bodyFile != "" {
				opts.Body, opts.BodyDir, err = cmdutil.ReadMarkdownBodyFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				bodyProvided = true
			}

			if !opts.IO.CanPrompt() && opts.RecoverFile != "" {
				return cmdutil.FlagErrorf("`--recover` 仅在交互式运行时受支持")
			}

			if opts.Template != "" && bodyProvided {
				return errors.New("使用 `--body` 或 `--body-file` 时不支持 `--template`")
			}

			opts.Interactive = !opts.EditorMode && !(titleProvided && bodyProvided)

			if opts.Interactive && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须提供 `--title` 和 `--body`")
			}

			if err := cmdutil.MutuallyExclusive(
				"使用 `--web` 时不支持 `--attach`",
				opts.AttachFlag.Changed(),
				opts.WebMode,
			); err != nil {
				return err
			}

			opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
			opts.Assets, err = opts.AttachFlag.UserAssets()
			if err != nil {
				return err
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "提供标题，否则会提示输入一个")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "提供正文，否则会提示输入一个")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本(使用 \"-\" 从标准输入读取)")
	cmd.Flags().BoolVarP(&opts.EditorMode, "editor", "e", false, "跳过提示并打开文本编辑器编写标题和正文。第一行是标题，其余文本是正文")
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "打开浏览器创建 issue")
	cmd.Flags().StringSliceVarP(&opts.Assignees, "assignee", "a", nil, "按 `login` 指派负责人，使用 \"@me\" 指派自己")
	cmd.Flags().StringSliceVarP(&opts.Labels, "label", "l", nil, "按 `name` 添加标签")
	cmd.Flags().StringSliceVarP(&opts.Projects, "project", "p", nil, "按 `title` 将 issue 添加到项目")
	cmd.Flags().StringVarP(&opts.Milestone, "milestone", "m", "", "按 `name` 将 issue 添加到里程碑")
	cmd.Flags().StringVar(&opts.RecoverFile, "recover", "", "从创建命令的失败运行中恢复输入")
	cmd.Flags().StringVarP(&opts.Template, "template", "T", "", "用作起始正文的模板 `name`")
	cmd.Flags().StringVar(&opts.IssueType, "type", "", "按 `name` 设置 issue 类型")
	cmd.Flags().StringVar(&opts.Parent, "parent", "", "将新 issue 添加为指定父 issue `number` 或 URL 的子议题")
	cmd.Flags().StringSliceVar(&opts.BlockedBy, "blocked-by", nil, "将新 issue 标记为被这些 issue `numbers` 或 URL 阻塞")
	cmd.Flags().StringSliceVar(&opts.Blocking, "blocking", nil, "将新 issue 标记为阻塞这些 issue `numbers` 或 URL")
	opts.AttachFlag = attachments.AddFlag(cmd)

	return cmd
}

func createRun(opts *CreateOptions) (err error) {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return
	}

	// TODO projectsV1Deprecation
	// 删除此部分，因为我们不再需要检测
	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}

	projectsV1Support := opts.Detector.ProjectsV1()
	issueFeatures, err := opts.Detector.IssueFeatures()
	if err != nil {
		return err
	}

	isTerminal := opts.IO.IsStdoutTTY()

	var milestones []string
	if opts.Milestone != "" {
		milestones = []string{opts.Milestone}
	}

	// 替换负责人中的特殊值
	// 对于 web 模式，@copilot 应替换为 name；否则替换为 login。
	assigneeReplacer := prShared.NewSpecialAssigneeReplacer(apiClient, baseRepo.RepoHost(), issueFeatures.ApiActorsSupported, !opts.WebMode)
	assignees, err := assigneeReplacer.ReplaceSlice(opts.Assignees)
	if err != nil {
		return err
	}
	assigneeSet := set.NewStringSet()
	assigneeSet.AddValues(assignees)

	tb := prShared.IssueMetadataState{
		Type:               prShared.IssueMetadata,
		ApiActorsSupported: issueFeatures.ApiActorsSupported, // TODO ApiActorsSupported
		Assignees:          assigneeSet.ToSlice(),
		Labels:             opts.Labels,
		ProjectTitles:      opts.Projects,
		Milestones:         milestones,
		Title:              opts.Title,
		Body:               opts.Body,
	}

	if opts.RecoverFile != "" {
		err = prShared.FillFromJSON(opts.IO, opts.RecoverFile, &tb)
		if err != nil {
			err = fmt.Errorf("恢复输入失败: %w", err)
			return
		}
	}

	tpl := prShared.NewTemplateManager(httpClient, baseRepo, opts.Prompter, opts.RootDirOverride, !opts.HasRepoOverride, false)

	if opts.WebMode {
		var openURL string
		if opts.Title != "" || opts.Body != "" || tb.HasMetadata() {
			openURL, err = generatePreviewURL(apiClient, baseRepo, tb, projectsV1Support)
			if err != nil {
				return
			}
			if !prShared.ValidURL(openURL) {
				err = fmt.Errorf("无法在浏览器中打开: 超出最大 URL 长度")
				return
			}
		} else if ok, _ := tpl.HasTemplates(); ok {
			openURL = ghrepo.GenerateRepoURL(baseRepo, "issues/new/choose")
		} else {
			openURL = ghrepo.GenerateRepoURL(baseRepo, "issues/new")
		}
		if isTerminal {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	if isTerminal {
		fmt.Fprintf(opts.IO.ErrOut, "\n正在 %s 中创建 issue\n\n", ghrepo.FullName(baseRepo))
	}

	repo, err := api.IssueRepoInfo(apiClient, baseRepo)
	if err != nil {
		return
	}
	if !repo.HasIssuesEnabled {
		err = fmt.Errorf("仓库 '%s' 已禁用 issue", ghrepo.FullName(baseRepo))
		return
	}

	// 在第一次调查之前构建，以便无法上传的运行尽早停止。
	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		uploader, err = attachments.NewUploader(httpClient, baseRepo.RepoHost(), repo.DatabaseID, repo.ViewerPermission)
		if err != nil {
			return
		}
	}

	action := prShared.SubmitAction
	templateNameForSubmit := ""
	var openURL string

	if opts.Interactive {
		defer prShared.PreserveInput(opts.IO, &tb, &err)()

		if opts.Title == "" {
			err = prShared.TitleSurvey(opts.Prompter, opts.IO, &tb)
			if err != nil {
				return
			}
		}

		if opts.Body == "" {
			templateContent := ""

			if opts.RecoverFile == "" {
				var template prShared.Template

				if opts.Template != "" {
					template, err = tpl.Select(opts.Template)
					if err != nil {
						return
					}
				} else {
					template, err = tpl.Choose()
					if err != nil {
						return
					}
				}

				if template != nil {
					templateContent = string(template.Body())
					templateNameForSubmit = template.NameForSubmit()
				} else {
					templateContent = string(tpl.LegacyBody())
				}
			}

			err = prShared.BodySurvey(opts.Prompter, &tb, templateContent)
			if err != nil {
				return
			}
		}

		// 交互式议题类型选择
		if opts.IssueType == "" {
			issueTypes, typesErr := api.RepoIssueTypes(apiClient, baseRepo)
			if typesErr == nil && len(issueTypes) > 0 {
				typeNames := make([]string, len(issueTypes))
				for i, t := range issueTypes {
					typeNames[i] = t.Name
				}
				var selected int
				selected, err = opts.Prompter.Select("issue 类型", "", typeNames)
				if err != nil {
					return
				}
				opts.IssueType = typeNames[selected]
				opts.issueTypeID = issueTypes[selected].ID
			}
		}

		openURL, err = generatePreviewURL(apiClient, baseRepo, tb, projectsV1Support)
		if err != nil {
			return
		}

		// 预览会打开浏览器，而浏览器无法携带上传内容。
		allowPreview := !tb.HasMetadata() && prShared.ValidURL(openURL) && len(opts.Assets) == 0
		action, err = prShared.ConfirmIssueSubmission(opts.Prompter, allowPreview, repo.ViewerCanTriage())
		if err != nil {
			err = fmt.Errorf("无法确认: %w", err)
			return
		}

		if action == prShared.MetadataAction {
			fetcher := &prShared.MetadataFetcher{
				IO:        opts.IO,
				APIClient: apiClient,
				Repo:      baseRepo,
				State:     &tb,
			}
			var assigneeSearchFunc func(string) prompter.MultiSelectSearchResult
			if issueFeatures.ApiActorsSupported {
				assigneeSearchFunc = prShared.RepoAssigneeSearchFunc(apiClient, baseRepo)
			}
			err = prShared.MetadataSurvey(opts.Prompter, opts.IO, baseRepo, fetcher, &tb, projectsV1Support, nil, assigneeSearchFunc)
			if err != nil {
				return
			}

			action, err = prShared.ConfirmIssueSubmission(opts.Prompter, !tb.HasMetadata() && len(opts.Assets) == 0, false)
			if err != nil {
				return
			}
		}

		if action == prShared.CancelAction {
			fmt.Fprintln(opts.IO.ErrOut, "已丢弃")
			err = cmdutil.CancelError
			return
		}
	} else {
		if opts.EditorMode {
			if opts.Template != "" {
				var template prShared.Template
				template, err = tpl.Select(opts.Template)
				if err != nil {
					return
				}
				if tb.Title == "" {
					tb.Title = template.Title()
				}
				templateNameForSubmit = template.NameForSubmit()
				tb.Body = string(template.Body())
			}

			tb.Title, tb.Body, err = opts.TitledEditSurvey(tb.Title, tb.Body)
			if err != nil {
				return
			}
		}
		if tb.Title == "" {
			err = fmt.Errorf("标题不能为空")
			return
		}
	}

	if action == prShared.PreviewAction {
		if isTerminal {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	} else if action == prShared.SubmitAction {
		params := map[string]any{
			"title": tb.Title,
			"body":  tb.Body,
		}
		if templateNameForSubmit != "" {
			params["issueTemplate"] = templateNameForSubmit
		}

		err = prShared.AddMetadataToIssueParams(apiClient, baseRepo, params, &tb, projectsV1Support)
		if err != nil {
			return
		}

		// 在未上传任何内容时，正文会承诺一个并不存在的附件，
		// 因此不会创建议题。一旦有任何内容上传成功，就会创建
		// 议题并报告失败。
		if uploader != nil {
			body, uploadResult, uploadErr := uploader.UploadAndAttach(context.Background(), tb.Body, opts.BodyDir, opts.Assets)
			opts.AttachEvent.RecordOperations(uploadResult)
			if uploadErr != nil && uploadResult.Uploaded == 0 {
				err = uploadErr
				return
			}
			params["body"] = body
			if uploadErr != nil {
				defer func() { err = errors.Join(uploadErr, err) }()
			}
		}

		var newIssue *api.Issue
		newIssue, err = api.IssueCreate(apiClient, repo, params)
		if err != nil {
			return
		}

		var updateOpts api.DeferredUpdateIssueOptions
		updateOpts, err = deferredUpdateIssueOptions(apiClient, baseRepo, newIssue, opts)
		if err != nil {
			return
		}
		if err = api.DeferredUpdateIssue(apiClient, updateOpts); err != nil {
			return
		}

		fmt.Fprintln(opts.IO.Out, newIssue.URL)
	} else {
		panic("不可达状态")
	}

	return
}

func generatePreviewURL(apiClient *api.Client, baseRepo ghrepo.Interface, tb prShared.IssueMetadataState, projectsV1Support gh.ProjectsV1Support) (string, error) {
	openURL := ghrepo.GenerateRepoURL(baseRepo, "issues/new")
	return prShared.WithPrAndIssueQueryParams(apiClient, baseRepo, openURL, tb, projectsV1Support)
}

// deferredUpdateIssueOptions 将用户提供的 --type / --parent /
// --blocked-by / --blocking 旗标解析为 DeferredUpdateIssue
// 所需的 ID。
func deferredUpdateIssueOptions(client *api.Client, baseRepo ghrepo.Interface, issue *api.Issue, opts *CreateOptions) (api.DeferredUpdateIssueOptions, error) {
	updateOpts := api.DeferredUpdateIssueOptions{
		IssueID:  issue.ID,
		Hostname: baseRepo.RepoHost(),
	}

	if opts.IssueType != "" {
		typeID := opts.issueTypeID
		if typeID == "" {
			var err error
			typeID, err = issueShared.ResolveIssueTypeName(client, baseRepo, opts.IssueType)
			if err != nil {
				return api.DeferredUpdateIssueOptions{}, err
			}
		}
		updateOpts.IssueTypeID = typeID
	}

	if opts.Parent != "" {
		parentID, err := issueShared.ResolveIssueRef(client, baseRepo, opts.Parent)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --parent 引用 %q 失败: %w", opts.Parent, err)
		}
		updateOpts.ParentID = parentID
	}

	for _, ref := range opts.BlockedBy {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --blocked-by 引用 %q 失败: %w", ref, err)
		}
		updateOpts.AddBlockedByIDs = append(updateOpts.AddBlockedByIDs, id)
	}

	for _, ref := range opts.Blocking {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --blocking 引用 %q 失败: %w", ref, err)
		}
		updateOpts.AddBlockingIDs = append(updateOpts.AddBlockingIDs, id)
	}

	return updateOpts, nil
}
