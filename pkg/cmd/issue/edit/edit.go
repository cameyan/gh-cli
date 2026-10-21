package edit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/attachments"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type EditOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   prShared.EditPrompter
	Detector   fd.Detector

	DetermineEditor    func() (string, error)
	FieldsToEditSurvey func(prShared.EditPrompter, *prShared.Editable) error
	EditFieldsSurvey   func(prShared.EditPrompter, *prShared.Editable, string) error
	FetchOptions       func(*api.Client, ghrepo.Interface, *prShared.Editable, gh.ProjectsV1Support) error

	IssueNumbers []int
	Interactive  bool

	RemoveIssueType bool

	Parent          string
	RemoveParent    bool
	AddSubIssues    []string
	RemoveSubIssues []string
	AddBlockedBy    []string
	RemoveBlockedBy []string
	AddBlocking     []string
	RemoveBlocking  []string

	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
	Config      func() (gh.Config, error)

	prShared.Editable
}

func NewCmdEdit(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:                 f.IOStreams,
		HttpClient:         f.HttpClient,
		Config:             f.Config,
		DetermineEditor:    func() (string, error) { return cmdutil.DetermineEditor(f.Config) },
		FieldsToEditSurvey: prShared.FieldsToEditSurvey,
		EditFieldsSurvey:   prShared.EditFieldsSurvey,
		FetchOptions:       prShared.FetchOptions,
		Prompter:           f.Prompter,
	}

	var bodyFile string
	var removeMilestone bool

	cmd := &cobra.Command{
		Use:   "edit {<numbers> | <urls>}",
		Short: "编辑 issue",
		Long: heredoc.Docf(`
			编辑同一仓库中的一个或多个 issue。

			编辑 issue 的项目需要 %[1]sproject%[1]s 范围的授权。如需授权，请运行 %[1]sgh auth refresh -s project%[1]s。

			使用 %[1]s--attach%[1]s 将图片或视频上传到单个 issue。如果未提供正文选项，issue 会保留其已有的正文，
			并将附件追加到正文末尾。如果正文引用了某个已附加文件，例如 %[1]s![alt](./login.png)%[1]s，该引用会被
			重写为指向已上传的资源。
			每条命令最多可附加 50 个文件。

			图片的替代文本会跟在 %[1]s#%[1]s 之后的路径中，如 %[1]s--attach './login.png#The login error state'%[1]s。
			若不提供，则使用文件名。正文中已有的引用会保留写在其处的替代文本。视频会渲染为播放器且没有替代文本，因此无法为其指定。

			如果部分附件上传成功而其他附件失败，issue 仍会使用成功的附件进行更新。随后命令会以非零状态退出，但已编辑 issue
			的 URL 仍会打印到标准输出。

			%[1]s--add-assignee%[1]s 和 %[1]s--remove-assignee%[1]s 选项均支持以下特殊值:
			- %[1]s@me%[1]s: 将自己设为负责人或取消自己的负责人身份
			- %[1]s@copilot%[1]s: 将 Copilot 设为负责人或取消其负责人身份(在 GitHub 企业版服务器上不受支持)
		`, "`"),
		Example: heredoc.Doc(`
			$ gh issue edit 23 --title "I found a bug" --body "Nothing works"
			$ gh issue edit 23 --add-label "bug,help wanted" --remove-label "core"
			$ gh issue edit 23 --add-assignee "@me" --remove-assignee monalisa,hubot
			$ gh issue edit 23 --add-assignee "@copilot"
			$ gh issue edit 23 --add-project "Roadmap" --remove-project v1,v2
			$ gh issue edit 23 --milestone "Version 1"
			$ gh issue edit 23 --remove-milestone
			$ gh issue edit 23 --body-file body.txt
			$ gh issue edit 23 --attach './login.png#The login error state'
			$ gh issue edit 23 --attach ./before.png --attach ./after.png
			$ gh issue edit 23 34 --add-label "help wanted"
			$ gh issue edit 23 --type Bug
			$ gh issue edit 23 --remove-type
			$ gh issue edit 23 --parent 100
			$ gh issue edit 23 --remove-parent
			$ gh issue edit 100 --add-sub-issue 123,124
			$ gh issue edit 123 --add-blocked-by 200 --add-blocking 300,301
		`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumbers, baseRepo, err := issueShared.ParseIssuesFromArgs(args)
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

			opts.IssueNumbers = issueNumbers

			flags := cmd.Flags()

			bodyProvided := flags.Changed("body")
			bodyFileProvided := bodyFile != ""

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--body` 或 `--body-file` 中的一个",
				bodyProvided,
				bodyFileProvided,
			); err != nil {
				return err
			}
			if bodyProvided || bodyFileProvided {
				opts.Editable.Body.Edited = true
				if bodyFileProvided {
					b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
					if err != nil {
						return err
					}
					opts.Editable.Body.Value = string(b)
				}
			}

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--milestone` 或 `--remove-milestone` 中的一个",
				flags.Changed("milestone"),
				removeMilestone,
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--type` 或 `--remove-type` 中的一个",
				flags.Changed("type"),
				opts.RemoveIssueType,
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"只能指定 --parent 或 --remove-parent 中的一个",
				flags.Changed("parent"),
				opts.RemoveParent,
			); err != nil {
				return err
			}

			if flags.Changed("title") {
				opts.Editable.Title.Edited = true
			}
			if flags.Changed("add-assignee") || flags.Changed("remove-assignee") {
				opts.Editable.Assignees.Edited = true
			}
			if flags.Changed("add-label") || flags.Changed("remove-label") {
				opts.Editable.Labels.Edited = true
			}
			if flags.Changed("add-project") || flags.Changed("remove-project") {
				opts.Editable.Projects.Edited = true
			}
			if flags.Changed("milestone") || removeMilestone {
				opts.Editable.Milestone.Edited = true

				// 注意，当提供 `--remove-milestone` 时，
				// `opts.Editable.Milestone.Value` 的值会自动为空，
				// 这会导致里程碑关联被移除。有关参考，
				// 请参见 `Editable.MilestoneId` 方法。
			}
			if flags.Changed("type") {
				opts.Editable.IssueType.Edited = true
			}

			opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
			opts.Assets, err = opts.AttachFlag.UserAssets()
			if err != nil {
				return err
			}

			// 空的 --parent 不会产生任何工作，因此仅在此处计为一次编辑，
			// 在这里只要传入了该旗标就会抑制问卷。
			hasDeferredFlags := opts.hasDeferredEdits() || flags.Changed("parent")

			// 仅当用户完全没有传入任何编辑旗标时才进入交互模式。
			if !opts.Editable.Dirty() && !hasDeferredFlags && len(opts.Assets) == 0 {
				opts.Interactive = true
			}

			if opts.Interactive && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须指定要编辑的字段选项")
			}

			if opts.Interactive && len(opts.IssueNumbers) > 1 {
				return cmdutil.FlagErrorf("无法以交互方式编辑多个 issue")
			}

			if len(opts.IssueNumbers) > 1 && len(opts.AddSubIssues) > 0 {
				return cmdutil.FlagErrorf("编辑多个 issue 时无法使用 `--add-sub-issue`")
			}

			if len(opts.IssueNumbers) > 1 && len(opts.Assets) > 0 {
				return cmdutil.FlagErrorf("编辑多个 issue 时无法使用 `--attach`")
			}

			if runF != nil {
				return runF(opts)
			}

			return editRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Editable.Title.Value, "title", "t", "", "设置新标题")
	cmd.Flags().StringVarP(&opts.Editable.Body.Value, "body", "b", "", "设置新正文")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本(使用 \"-\" 从标准输入读取)")
	cmd.Flags().StringSliceVar(&opts.Editable.Assignees.Add, "add-assignee", nil, "按 `login` 添加负责人。使用 \"@me\" 将自己设为负责人，或使用 \"@copilot\" 将 Copilot 设为负责人")
	cmd.Flags().StringSliceVar(&opts.Editable.Assignees.Remove, "remove-assignee", nil, "按 `login` 移除负责人。使用 \"@me\" 取消自己的负责人身份，或使用 \"@copilot\" 取消 Copilot 的负责人身份")
	cmd.Flags().StringSliceVar(&opts.Editable.Labels.Add, "add-label", nil, "按 `name` 添加标签")
	cmd.Flags().StringSliceVar(&opts.Editable.Labels.Remove, "remove-label", nil, "按 `name` 移除标签")
	cmd.Flags().StringSliceVar(&opts.Editable.Projects.Add, "add-project", nil, "按 `title` 将 issue 添加到项目")
	cmd.Flags().StringSliceVar(&opts.Editable.Projects.Remove, "remove-project", nil, "按 `title` 将 issue 从项目中移除")
	cmd.Flags().StringVarP(&opts.Editable.Milestone.Value, "milestone", "m", "", "按 `name` 编辑 issue 所属的里程碑")
	cmd.Flags().BoolVar(&removeMilestone, "remove-milestone", false, "移除 issue 的里程碑关联")
	cmd.Flags().StringVar(&opts.Editable.IssueType.Value, "type", "", "按 `name` 设置 issue 类型")
	cmd.Flags().BoolVar(&opts.RemoveIssueType, "remove-type", false, "移除 issue 的类型")
	cmd.Flags().StringVar(&opts.Parent, "parent", "", "按 `number` 或 URL 设置父issue")
	cmd.Flags().BoolVar(&opts.RemoveParent, "remove-parent", false, "移除父 issue")
	cmd.Flags().StringSliceVar(&opts.AddSubIssues, "add-sub-issue", nil, "按 `number` 或 URL 添加子 issue")
	cmd.Flags().StringSliceVar(&opts.RemoveSubIssues, "remove-sub-issue", nil, "按 `number` 或 URL 移除子 issue")
	cmd.Flags().StringSliceVar(&opts.AddBlockedBy, "add-blocked-by", nil, "按 issue `number` 或 URL 添加 'blocked by' 关系")
	cmd.Flags().StringSliceVar(&opts.RemoveBlockedBy, "remove-blocked-by", nil, "按issue `number` 或 URL 移除 'blocked by' 关系")
	cmd.Flags().StringSliceVar(&opts.AddBlocking, "add-blocking", nil, "按issue `number` 或 URL 添加 'blocking' 关系")
	cmd.Flags().StringSliceVar(&opts.RemoveBlocking, "remove-blocking", nil, "按 issue `number` 或 URL 移除 'blocking' 关系")
	opts.AttachFlag = attachments.AddFlag(cmd)

	return cmd
}

// hasDeferredEdits 报告本次运行是否请求了延迟更新路径会应用的编辑，
// 这些编辑是 Editable.Dirty() 无法看到的。未包含设置 --type 的情况，
// 因为它会设置 Editable.IssueType.Edited，而 Dirty() 已经覆盖了
// 这一点；只有 --remove-type 会到达这里。
func (opts *EditOptions) hasDeferredEdits() bool {
	return opts.RemoveIssueType ||
		opts.Parent != "" || opts.RemoveParent ||
		len(opts.AddSubIssues) > 0 || len(opts.RemoveSubIssues) > 0 ||
		len(opts.AddBlockedBy) > 0 || len(opts.RemoveBlockedBy) > 0 ||
		len(opts.AddBlocking) > 0 || len(opts.RemoveBlocking) > 0
}

func editRun(opts *EditOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	// 提示用户选择他们想要编辑的字段。
	editable := opts.Editable
	editable.IssueType.Selectable = true
	if opts.Interactive {
		err = opts.FieldsToEditSurvey(opts.Prompter, &editable)
		if err != nil {
			return err
		}
	}

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}

	issueFeatures, err := opts.Detector.IssueFeatures()
	if err != nil {
		return err
	}

	lookupFields := []string{"id", "number", "title", "body", "url"}
	if editable.Assignees.Edited {
		// TODO ApiActorsSupported
		if issueFeatures.ApiActorsSupported {
			editable.ApiActorsSupported = true
			lookupFields = append(lookupFields, "assignedActors")
		} else {
			lookupFields = append(lookupFields, "assignees")
		}
	}
	if editable.Labels.Edited {
		lookupFields = append(lookupFields, "labels")
	}
	if editable.Projects.Edited {
		// TODO projectsV1Deprecation
		// 移除此部分，因为我们不应再添加 projectCards
		projectsV1Support := opts.Detector.ProjectsV1()
		if projectsV1Support == gh.ProjectsV1Supported {
			lookupFields = append(lookupFields, "projectCards")
		}

		lookupFields = append(lookupFields, "projectItems")
	}
	if editable.Milestone.Edited {
		lookupFields = append(lookupFields, "milestone")
	}
	if editable.IssueType.Edited {
		lookupFields = append(lookupFields, "issueType")
	}
	if opts.Parent != "" || opts.RemoveParent {
		lookupFields = append(lookupFields, "parent")
	}
	if len(opts.Assets) > 0 {
		lookupFields = append(lookupFields, "repository")
	}

	// 获取所有指定的issue，并确保它们位于同一仓库中。
	issues, err := issueShared.FindIssuesOrPRs(httpClient, baseRepo, opts.IssueNumbers, lookupFields)
	if err != nil {
		return err
	}

	// NewCmdEdit 会拒绝针对多个issue的 --attach，因此 issues[0] 是
	// 唯一的issue。这是其仓库 id 和权限最早存在的地方，
	// 因此无法上传的运行会在任何写入之前停止。
	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		cfg, err := opts.Config()
		if err != nil {
			return err
		}
		host := baseRepo.RepoHost()
		tokenType := cfg.Authentication().ActiveTokenType(host)
		uploader, err = attachments.NewUploader(httpClient, tokenType, host, issues[0].RepositoryDatabaseID(), issues[0].RepositoryViewerPermission())
		if err != nil {
			return err
		}
	}

	// 为所有issue一次性获取可编辑的共享字段。
	apiClient := api.NewClientFromHTTP(httpClient)

	// 当 ApiActorsSupported 可用时，为负责人接入搜索函数。
	// 交互模式仅支持单个issue，因此我们使用其 ID 作为搜索查询。
	if issueFeatures.ApiActorsSupported && opts.Interactive && len(issues) == 1 {
		editable.AssigneeSearchFunc = prShared.AssigneeSearchFunc(apiClient, baseRepo, issues[0].ID)
	}

	opts.IO.StartProgressIndicatorWithLabel("正在获取仓库信息")
	err = opts.FetchOptions(apiClient, baseRepo, &editable, opts.Detector.ProjectsV1())
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	// 在非交互模式下上传之前先解析issue类型 ID。
	// 交互模式会在问卷设置该值之后再解析。
	var issueTypeID string
	if !opts.Interactive {
		issueTypeID, err = lookupIssueTypeID(&editable)
		if err != nil {
			return err
		}
	}

	var uploadErr error
	if uploader != nil {
		// 调用方提供的正文会替换issue的正文，包括空正文。
		body := editable.Body.Value
		if !editable.Body.Edited {
			body = issues[0].Body
		}

		// 此部分位于下方循环之外，因此每个文件只上传一次，而
		// Clone 会将合并后的正文带入issue。上传之后不得再有任何
		// 可能提示或取消的操作。
		var uploadResult attachments.UploadResult
		body, uploadResult, uploadErr = uploader.UploadAndAttach(context.Background(), body, opts.Assets)
		opts.AttachEvent.RecordOperations(uploadResult)

		if uploadResult.Uploaded > 0 {
			editable.Body.Value = body
			editable.Body.Edited = true
		} else {
			// 调用方的正文原本是为了携带一个从未到达的附件，
			// 因此它不会替换issue已有的正文。调用方要求的其他
			// 每个字段仍然适用。
			editable.Body.Edited = false
		}
	}

	// 并行更新所有issue。
	editedIssueChan := make(chan string, len(issues))
	failedIssueChan := make(chan string, len(issues))
	g := sync.WaitGroup{}

	// 仅在我们下方不会提示或问卷不会打断进度指示器时显示进度。
	if !opts.Interactive {
		opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在更新 %d 个 issue", len(issues)))
	}

	for _, issue := range issues {
		// 复制变量以便在下面的 go 例程中捕获。
		editable := editable.Clone()

		editable.Title.Default = issue.Title
		editable.Body.Default = issue.Body
		// 如果 Actors 在此 GitHub 主机上可分配，我们将 Actors
		// 用作默认负责人。
		// TODO ApiActorsSupported
		if editable.ApiActorsSupported {
			editable.Assignees.Default = issue.AssignedActors.DisplayNames()
			editable.Assignees.DefaultLogins = issue.AssignedActors.Logins()
		} else {
			editable.Assignees.Default = issue.Assignees.Logins()
		}
		editable.Labels.Default = issue.Labels.Names()
		editable.Projects.Default = append(issue.ProjectCards.ProjectNames(), issue.ProjectItems.ProjectTitles()...)
		projectItems := map[string]string{}
		for _, n := range issue.ProjectItems.Nodes {
			projectItems[n.Project.ID] = n.ID
		}
		editable.Projects.ProjectItems = projectItems
		if issue.Milestone != nil {
			editable.Milestone.Default = issue.Milestone.Title
		}
		if issue.IssueType != nil {
			editable.IssueType.Default = issue.IssueType.Name
		}

		// 允许对单个issue进行交互式提示；如果指定了多个issue，则前面已失败。
		if opts.Interactive {
			editorCommand, err := opts.DetermineEditor()
			if err != nil {
				return err
			}
			err = opts.EditFieldsSurvey(opts.Prompter, &editable, editorCommand)
			if err != nil {
				return err
			}
			issueTypeID, err = lookupIssueTypeID(&editable)
			if err != nil {
				return err
			}
		}

		g.Add(1)
		go func(issue *api.Issue) {
			defer g.Done()

			if err := prShared.UpdateIssue(httpClient, baseRepo, issue.ID, issue.IsPullRequest(), editable); err != nil {
				failedIssueChan <- fmt.Sprintf("更新 %s 失败: %s", issue.URL, err)
				return
			}

			mutations, err := deferredUpdateIssueOptions(apiClient, baseRepo, issue, opts, issueTypeID)
			if err != nil {
				failedIssueChan <- fmt.Sprintf("更新 %s 失败: %s", issue.URL, err)
				return
			}
			if err := api.DeferredUpdateIssue(apiClient, mutations); err != nil {
				failedIssueChan <- fmt.Sprintf("更新 %s 失败:\n%s", issue.URL, err)
				return
			}

			editedIssueChan <- issue.URL
		}(issue)
	}

	g.Wait()
	close(editedIssueChan)
	close(failedIssueChan)

	// 如果上方未启动进度，则不执行任何操作。
	opts.IO.StopProgressIndicator()

	// 将成功编辑的issue URL 排序后打印到标准输出。
	editedIssueURLs := make([]string, 0, len(issues))
	for editedIssueURL := range editedIssueChan {
		editedIssueURLs = append(editedIssueURLs, editedIssueURL)
	}

	sort.Strings(editedIssueURLs)
	// 附加是本命令在解析旗标之后唯一会丢弃的编辑，因此也是唯一
	// 能在没有写入任何内容的情况下到达此处的方式。成功放置文件的
	// 上传会将正文标记为已编辑，因此 dirty 的可编辑项意味着有写入。
	nothingWritten := len(opts.Assets) > 0 && !editable.Dirty() && !opts.hasDeferredEdits()
	if !nothingWritten {
		for _, editedIssueURL := range editedIssueURLs {
			fmt.Fprintln(opts.IO.Out, editedIssueURL)
		}
	}

	// 将失败项排序后打印到标准错误。
	failedIssueErrors := make([]string, 0, len(issues))
	for failedIssueError := range failedIssueChan {
		failedIssueErrors = append(failedIssueErrors, failedIssueError)
	}

	sort.Strings(failedIssueErrors)
	for _, failedIssueError := range failedIssueErrors {
		fmt.Fprintln(opts.IO.ErrOut, failedIssueError)
	}

	if len(failedIssueErrors) > 0 {
		// 上传失败和写入失败有不同的补救措施，因此写入失败
		// 不能同时代表两者。
		return errors.Join(uploadErr, fmt.Errorf("更新 %d 个 issue 失败", len(failedIssueErrors)))
	}

	return uploadErr
}

// lookupIssueTypeID 使用 FetchOptions 填充的映射，
// 将选定的issue类型解析为其节点 ID。
func lookupIssueTypeID(editable *prShared.Editable) (string, error) {
	if !editable.IssueType.Edited || editable.IssueType.Value == "" {
		return "", nil
	}
	id, ok := editable.IssueTypeNameToID[editable.IssueType.Value]
	if !ok {
		return "", fmt.Errorf("类型 %q 未找到，可用类型: %s",
			editable.IssueType.Value,
			strings.Join(editable.IssueType.Options, ", "))
	}
	return id, nil
}

func deferredUpdateIssueOptions(client *api.Client, baseRepo ghrepo.Interface, issue *api.Issue, editOpts *EditOptions, issueTypeID string) (api.DeferredUpdateIssueOptions, error) {
	updateOpts := api.DeferredUpdateIssueOptions{
		IssueID:               issue.ID,
		Hostname:              baseRepo.RepoHost(),
		IssueTypeID:           issueTypeID,
		RemoveIssueType:       editOpts.RemoveIssueType,
		ReplaceExistingParent: true,
	}

	if editOpts.RemoveParent {
		if issue.Parent != nil {
			updateOpts.RemoveParentID = issue.Parent.ID
		}
	} else if editOpts.Parent != "" {
		parentID, err := issueShared.ResolveIssueRef(client, baseRepo, editOpts.Parent)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --parent 引用 %q 失败: %w", editOpts.Parent, err)
		}
		updateOpts.ParentID = parentID
	}

	for _, ref := range editOpts.AddSubIssues {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --add-sub-issue 引用 %q 失败: %w", ref, err)
		}
		updateOpts.AddSubIssueIDs = append(updateOpts.AddSubIssueIDs, id)
	}
	for _, ref := range editOpts.RemoveSubIssues {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --remove-sub-issue 引用 %q 失败: %w", ref, err)
		}
		updateOpts.RemoveSubIssueIDs = append(updateOpts.RemoveSubIssueIDs, id)
	}

	for _, ref := range editOpts.AddBlockedBy {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --add-blocked-by 引用 %q 失败: %w", ref, err)
		}
		updateOpts.AddBlockedByIDs = append(updateOpts.AddBlockedByIDs, id)
	}
	for _, ref := range editOpts.RemoveBlockedBy {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --remove-blocked-by 引用 %q 失败: %w", ref, err)
		}
		updateOpts.RemoveBlockedByIDs = append(updateOpts.RemoveBlockedByIDs, id)
	}

	for _, ref := range editOpts.AddBlocking {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --add-blocking 引用 %q 失败: %w", ref, err)
		}
		updateOpts.AddBlockingIDs = append(updateOpts.AddBlockingIDs, id)
	}
	for _, ref := range editOpts.RemoveBlocking {
		id, err := issueShared.ResolveIssueRef(client, baseRepo, ref)
		if err != nil {
			return api.DeferredUpdateIssueOptions{}, fmt.Errorf("解析 --remove-blocking 引用 %q 失败: %w", ref, err)
		}
		updateOpts.RemoveBlockingIDs = append(updateOpts.RemoveBlockingIDs, id)
	}

	return updateOpts, nil
}
