package edit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/attachments"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	shared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type EditOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Finder          shared.PRFinder
	Surveyor        Surveyor
	Fetcher         EditableOptionsFetcher
	EditorRetriever EditorRetriever
	Prompter        shared.EditPrompter
	Detector        fd.Detector
	BaseRepo        func() (ghrepo.Interface, error)

	SelectorArg string
	Interactive bool

	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
	Config      func() (gh.Config, error)

	shared.Editable
}

func NewCmdEdit(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:              f.IOStreams,
		HttpClient:      f.HttpClient,
		Config:          f.Config,
		Surveyor:        surveyor{P: f.Prompter},
		Fetcher:         fetcher{},
		EditorRetriever: editorRetriever{config: f.Config},
		Prompter:        f.Prompter,
	}

	var bodyFile string
	var removeMilestone bool

	cmd := &cobra.Command{
		Use:   "edit [<number> | <url> | <branch>]",
		Short: "编辑拉取请求(PR)",
		Long: heredoc.Docf(`
			编辑拉取请求(PR)。

			若未提供参数，将选择属于当前分支的拉取请求(PR)。

			编辑拉取请求(PR)的项目(project)需要以 %[1]sproject%[1]s 权限范围(scope)授权。
			如需授权，请运行 %[1]sgh auth refresh -s project%[1]s。

			使用 %[1]s--attach%[1]s 可上传图片或视频。若未提供正文 flag，拉取请求(PR)
			保留已有正文，附件会被追加到其中。如果正文引用了某个附加文件，
			例如 %[1]s![alt](./login.png)%[1]s，该引用会被改写为指向已上传的资产(asset)。
			每条命令最多可附加 50 个文件。

			图片的替代文本(alt text)为 %[1]s#%[1]s 之后的路径内容，如
			%[1]s--attach './login.png#The login error state'%[1]s。未提供时使用文件名。
			正文中已有的引用会保留其中书写的替代文本。视频以播放器形式呈现且没有替代文本，
			因此无法为其指定。

			如果部分附件上传成功而其他失败，拉取请求(PR)仍会以成功上传的那些更新。
			随后该命令以非零状态退出，但拉取请求(PR)的 URL 仍会打印到标准输出。

			%[1]s--add-assignee%[1]s 和 %[1]s--remove-assignee%[1]s flag 均支持以下特殊取值:
			- %[1]s@me%[1]s: 指派或取消指派自己
			- %[1]s@copilot%[1]s: 指派或取消指派 Copilot (GitHub Enterprise Server 不支持)

			%[1]s--add-reviewer%[1]s 和 %[1]s--remove-reviewer%[1]s flag 支持以下特殊取值:
			- %[1]s@copilot%[1]s: 请求或移除 Copilot 的审核(review) (GitHub Enterprise Server 不支持)
		`, "`"),
		Example: heredoc.Doc(`
			# 编辑拉取请求(PR)的标题与正文
			$ gh pr edit 23 --title "I found a bug" --body "Nothing works"

			# 使用文件作为正文
			$ gh pr edit 23 --body-file body.txt

			# 将截图追加到正文，"#" 之后为替代文本(alt text)
			$ gh pr edit 23 --attach './login.png#The login error state'

			# 通过重复 flag 追加多个文件
			$ gh pr edit 23 --attach ./before.png --attach ./after.png

			# 管理标签(label)
			$ gh pr edit 23 --add-label "bug,help wanted" --remove-label "core"

			# 管理审核者(reviewer)
			$ gh pr edit 23 --add-reviewer monalisa,hubot --remove-reviewer myorg/team-name

			# 重新请求审核(review)
			$ gh pr edit 23 --add-reviewer monalisa

			# 请求 GitHub Copilot 审核(review)
			$ gh pr edit 23 --add-reviewer "@copilot"

			# 管理被指派人(assignee)
			$ gh pr edit 23 --add-assignee "@me" --remove-assignee monalisa,hubot

			# 指派 GitHub Copilot
			$ gh pr edit 23 --add-assignee "@copilot"

			# 管理项目(project)与里程碑(milestone)
			$ gh pr edit 23 --add-project "Roadmap" --remove-project v1,v2
			$ gh pr edit 23 --milestone "Version 1"
			$ gh pr edit 23 --remove-milestone
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if opts.SelectorArg != "" {
				// 如果提供了 URL，我们需要解析它以覆盖基础仓库(base repo)，
				// 尤其是主机名部分。这是因为该命令内部需要用到特性检测器(feature detector)，
				// 而它需要知道 API 主机。如果命令在 git 仓库之外运行，
				// 则除非已解析 URL，否则无法实例化该检测器。
				if baseRepo, _, _, err := shared.ParseURL(opts.SelectorArg); err == nil {
					opts.BaseRepo = func() (ghrepo.Interface, error) {
						return baseRepo, nil
					}
				}
			}

			flags := cmd.Flags()

			bodyProvided := flags.Changed("body")
			bodyFileProvided := bodyFile != ""

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--body` 或 `--body-file` 之一",
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
				"只能指定 `--milestone` 或 `--remove-milestone` 之一",
				flags.Changed("milestone"),
				removeMilestone,
			); err != nil {
				return err
			}

			if flags.Changed("title") {
				opts.Editable.Title.Edited = true
			}
			if flags.Changed("body") {
				opts.Editable.Body.Edited = true
			}
			if flags.Changed("base") {
				opts.Editable.Base.Edited = true
			}
			if flags.Changed("add-reviewer") || flags.Changed("remove-reviewer") {
				opts.Editable.Reviewers.Edited = true
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

				// 注意: 提供了 `--remove-milestone` 时，`opts.Editable.Milestone.Value`
				// 会自动为空，这将导致里程碑(milestone)关联被移除。
				// 参见 `Editable.MilestoneId` 方法。
			}

			var err error
			opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
			opts.Assets, err = opts.AttachFlag.UserAssets()
			if err != nil {
				return err
			}

			if !opts.Editable.Dirty() && len(opts.Assets) == 0 {
				opts.Interactive = true
			}

			if opts.Interactive && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须提供 --title、--body、--reviewer、--assignee、--label、--project 或 --milestone")
			}

			if runF != nil {
				return runF(opts)
			}

			return editRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Editable.Title.Value, "title", "t", "", "设置新标题。")
	cmd.Flags().StringVarP(&opts.Editable.Body.Value, "body", "b", "", "设置新正文。")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")
	cmd.Flags().StringVarP(&opts.Editable.Base.Value, "base", "B", "", "更改该拉取请求(PR)的基础分支(base `branch`)")
	cmd.Flags().StringSliceVar(&opts.Editable.Reviewers.Add, "add-reviewer", nil, "按 `login` 添加或重新请求审核者(reviewer)。使用 \"@copilot\" 请求 Copilot 审核。")
	cmd.Flags().StringSliceVar(&opts.Editable.Reviewers.Remove, "remove-reviewer", nil, "按 `login` 移除审核者(reviewer)。使用 \"@copilot\" 移除对 Copilot 的审核请求。")
	cmd.Flags().StringSliceVar(&opts.Editable.Assignees.Add, "add-assignee", nil, "按 `login` 添加被指派人。使用 \"@me\" 指派给自己，或使用 \"@copilot\" 指派 Copilot。")
	cmd.Flags().StringSliceVar(&opts.Editable.Assignees.Remove, "remove-assignee", nil, "按 `login` 移除被指派人。使用 \"@me\" 取消指派自己，或使用 \"@copilot\" 取消指派 Copilot。")
	cmd.Flags().StringSliceVar(&opts.Editable.Labels.Add, "add-label", nil, "按 `name` 添加标签(label)")
	cmd.Flags().StringSliceVar(&opts.Editable.Labels.Remove, "remove-label", nil, "按 `name` 移除标签(label)")
	cmd.Flags().StringSliceVar(&opts.Editable.Projects.Add, "add-project", nil, "按 `title` 将拉取请求(PR)添加到项目(project)")
	cmd.Flags().StringSliceVar(&opts.Editable.Projects.Remove, "remove-project", nil, "按 `title` 将拉取请求(PR)从项目(project)中移除")
	cmd.Flags().StringVarP(&opts.Editable.Milestone.Value, "milestone", "m", "", "按 `name` 编辑该拉取请求(PR)所属的里程碑(milestone)")
	cmd.Flags().BoolVar(&removeMilestone, "remove-milestone", false, "移除该拉取请求(PR)的里程碑(milestone)关联")
	opts.AttachFlag = attachments.AddFlag(cmd)

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "base")

	for _, flagName := range []string{"add-reviewer", "remove-reviewer"} {
		_ = cmd.RegisterFlagCompletionFunc(flagName, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			baseRepo, err := f.BaseRepo()
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			httpClient, err := f.HttpClient()
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			results, err := shared.RequestableReviewersForCompletion(httpClient, baseRepo)
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			return results, cobra.ShellCompDirectiveNoFileComp
		})
	}

	return cmd
}

func editRun(opts *EditOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	if opts.Detector == nil {
		baseRepo, err := opts.BaseRepo()
		if err != nil {
			return err
		}

		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}

	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "author", "url", "title", "body", "baseRefName", "reviewRequests", "labels", "projectCards", "projectItems", "milestone"},
		Detector: opts.Detector,
	}

	if len(opts.Assets) > 0 {
		findOptions.Fields = append(findOptions.Fields, "repository")
	}

	issueFeatures, err := opts.Detector.IssueFeatures()
	if err != nil {
		return err
	}

	// TODO ApiActorsSupported
	if issueFeatures.ApiActorsSupported {
		findOptions.Fields = append(findOptions.Fields, "assignedActors")
	} else {
		findOptions.Fields = append(findOptions.Fields, "assignees")
	}

	pr, repo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	// 在提示之前构建，使无法上传的运行可以尽早停止。
	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		cfg, err := opts.Config()
		if err != nil {
			return err
		}
		// URL 选择器指定了它自己的主机，因此这里可能与默认主机不同。
		host := repo.RepoHost()
		tokenType := cfg.Authentication().ActiveTokenType(host)
		uploader, err = attachments.NewUploader(httpClient, tokenType, host, pr.RepositoryDatabaseID(), pr.RepositoryViewerPermission())
		if err != nil {
			return err
		}
	}

	editable := opts.Editable
	editable.Reviewers.Selectable = true
	editable.Title.Default = pr.Title
	editable.Body.Default = pr.Body
	editable.Base.Default = pr.BaseRefName
	editable.Reviewers.Default = pr.ReviewRequests.DisplayNames()
	editable.Reviewers.DefaultLogins = pr.ReviewRequests.Logins()
	// TODO ApiActorsSupported
	if issueFeatures.ApiActorsSupported {
		editable.ApiActorsSupported = true
		editable.Assignees.Default = pr.AssignedActors.DisplayNames()
		editable.Assignees.DefaultLogins = pr.AssignedActors.Logins()
	} else {
		editable.Assignees.Default = pr.Assignees.Logins()
	}
	editable.Labels.Default = pr.Labels.Names()
	editable.Projects.Default = append(pr.ProjectCards.ProjectNames(), pr.ProjectItems.ProjectTitles()...)
	projectItems := map[string]string{}
	for _, n := range pr.ProjectItems.Nodes {
		projectItems[n.Project.ID] = n.ID
	}
	editable.Projects.ProjectItems = projectItems
	if pr.Milestone != nil {
		editable.Milestone.Default = pr.Milestone.Title
	}

	if opts.Interactive {
		err = opts.Surveyor.FieldsToEdit(&editable)
		if err != nil {
			return err
		}
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	// 为被指派人(assignee)与审核者(reviewer)接入搜索函数。
	// 未接入时会触发下游回退到遗留的审核者/被指派人获取方式。
	// TODO ApiActorsSupported
	if issueFeatures.ApiActorsSupported {
		editable.AssigneeSearchFunc = shared.AssigneeSearchFunc(apiClient, repo, pr.ID)
		editable.ReviewerSearchFunc = reviewerSearchFunc(apiClient, repo, &editable, pr.ID)
	}

	opts.IO.StartProgressIndicator()
	err = opts.Fetcher.EditableOptionsFetch(apiClient, repo, &editable, opts.Detector.ProjectsV1())
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if opts.Interactive {
		// 从审核者选项中移除 PR 作者；
		// 若包含作者，REST API 会报错(GraphQL 则会静默忽略)。
		if editable.Reviewers.Edited {
			s := set.NewStringSet()
			s.AddValues(editable.Reviewers.Options)
			s.Remove(pr.Author.Login)
			editable.Reviewers.Options = s.ToSlice()
		}

		editorCommand, err := opts.EditorRetriever.Retrieve()
		if err != nil {
			return err
		}
		err = opts.Surveyor.EditFields(&editable, editorCommand)
		if err != nil {
			return err
		}
	}

	var uploadErr error
	if uploader != nil {
		// 调用方提供的正文会替换拉取请求(PR)的正文，包括空正文。
		body := editable.Body.Value
		if !editable.Body.Edited {
			body = editable.Body.Default
		}

		// 在此之后不得有任何可能提示或取消的操作。
		var uploadResult attachments.UploadResult
		body, uploadResult, uploadErr = uploader.UploadAndAttach(context.Background(), body, opts.Assets)
		opts.AttachEvent.RecordOperations(uploadResult)

		// 没有上传任何文件时，即使是调用方亲手输入的正文也不会写入:
		// 其中的引用仍是本地路径，渲染出来是坏的。其余字段与上传无关，
		// 因此无论哪种情况都会继续处理。
		editable.Body.Edited = uploadResult.Uploaded > 0
		if uploadResult.Uploaded > 0 {
			editable.Body.Value = body
		}

		// 下面的 URL 是该命令成功的标志，因此什么都没写入的运行不得打印它。
		if !editable.Dirty() {
			return errors.Join(uploadErr, errors.New("拉取请求(PR)未被更改"))
		}
	}

	opts.IO.StartProgressIndicator()
	err = updatePullRequest(httpClient, repo, pr.ID, pr.Number, editable)
	opts.IO.StopProgressIndicator()
	if err != nil {
		// 写入失败不会撤销上传，因此两者都要上报。
		return errors.Join(uploadErr, err)
	}

	fmt.Fprintln(opts.IO.Out, pr.URL)

	return uploadErr
}

// reviewerSearchFunc 旨在作为 MultiSelectWithSearch 的参数，
// 返回潜在的审核者(reviewer)候选(用户、bot 与团队)。
// 它还会更新 editable 的元数据，供稍后进行 ID 解析。
func reviewerSearchFunc(apiClient *api.Client, repo ghrepo.Interface, editable *shared.Editable, prID string) func(string) prompter.MultiSelectSearchResult {
	searchFunc := func(input string) prompter.MultiSelectSearchResult {
		candidates, moreResults, err := api.SuggestedReviewerActors(
			apiClient,
			repo,
			prID,
			input)
		if err != nil {
			return prompter.MultiSelectSearchResult{
				Keys:        nil,
				Labels:      nil,
				MoreResults: 0,
				Err:         err,
			}
		}

		keys := make([]string, 0, len(candidates))
		labels := make([]string, 0, len(candidates))

		for _, c := range candidates {
			keys = append(keys, c.Login())
			labels = append(labels, c.DisplayName())

			// 更新 editable 结构体中的团队(teams)元数据，
			// 以便稍后更新 PR 时能够解析团队 ID。
			if team, ok := c.(api.ReviewerTeam); ok {
				editable.Metadata.Teams = append(editable.Metadata.Teams, api.OrgTeam{
					ID:   "", // REST API 的审核者(reviewer) mutation 不需要 ID
					Slug: team.Slug(),
				})
			}
		}
		return prompter.MultiSelectSearchResult{
			Keys:        keys,
			Labels:      labels,
			MoreResults: moreResults,
			Err:         nil,
		}
	}
	return searchFunc
}

func updatePullRequest(httpClient *http.Client, repo ghrepo.Interface, id string, number int, editable shared.Editable) error {
	var wg errgroup.Group
	wg.Go(func() error {
		return shared.UpdateIssue(httpClient, repo, id, true, editable)
	})
	if editable.Reviewers.Edited {
		wg.Go(func() error {
			return updatePullRequestReviews(httpClient, repo, id, number, editable)
		})
	}
	return wg.Wait()
}

func updatePullRequestReviews(httpClient *http.Client, repo ghrepo.Interface, prID string, number int, editable shared.Editable) error {
	if !editable.Reviewers.Edited {
		return nil
	}

	client := api.NewClientFromHTTP(httpClient)

	// 从非交互式 flag 输入重建 Value 切片。
	if len(editable.Reviewers.Add) != 0 || len(editable.Reviewers.Remove) != 0 {
		add := editable.Reviewers.Add
		remove := editable.Reviewers.Remove

		// 将 @copilot 替换为 Copilot 审核者登录名(仅在 github.com 上)。
		// 计算集合时同样使用 DefaultLogins(而非 Default 显示名)。
		var defaultLogins []string
		// TODO ApiActorsSupported
		if editable.ApiActorsSupported {
			copilotReplacer := shared.NewCopilotReviewerReplacer()
			add = copilotReplacer.ReplaceSlice(add)
			remove = copilotReplacer.ReplaceSlice(remove)
			defaultLogins = editable.Reviewers.DefaultLogins
		} else {
			// 在 GHES 上，Default 已经是登录名(没有显示名的区分)
			defaultLogins = editable.Reviewers.Default
		}

		s := set.NewStringSet()
		s.AddValues(add)
		s.AddValues(defaultLogins)
		s.RemoveValues(remove)
		editable.Reviewers.Value = s.ToSlice()
	}

	// 在 github.com 上，使用支持 bot 的新 GraphQL mutation。
	// 在 GHES 上，回退到 REST API。
	// TODO ApiActorsSupported
	if editable.ApiActorsSupported {
		return updatePullRequestReviewsGraphQL(client, repo, prID, editable)
	}
	return updatePullRequestReviewsREST(client, repo, number, editable)
}

// updatePullRequestReviewsGraphQL 使用 RequestReviewsByLogin mutation。
// 该 mutation 会替换整个审核者(reviewer)集合(union: false)。
func updatePullRequestReviewsGraphQL(client *api.Client, repo ghrepo.Interface, prID string, editable shared.Editable) error {
	users, bots, teams := partitionReviewersByType(editable.Reviewers.Value)
	return api.RequestReviewsByLogin(client, repo, prID, users, bots, teams, false)
}

// updatePullRequestReviewsREST 使用 REST API 添加/移除审核者(reviewer)。
// 这是用于 GHES 兼容性的遗留路径。
func updatePullRequestReviewsREST(client *api.Client, repo ghrepo.Interface, number int, editable shared.Editable) error {
	addUsers, addBots, addTeams := partitionReviewersByType(editable.Reviewers.Value)
	// REST API 不区分 bot 与用户，因此需要将它们合并。
	allAddUsers := append(addUsers, addBots...)

	// 在 Default 中但不在 Value 中的审核者已被交互式移除。
	var toRemove []string
	for _, r := range editable.Reviewers.Default {
		if !slices.Contains(editable.Reviewers.Value, r) {
			toRemove = append(toRemove, r)
		}
	}
	removeUsers, removeBots, removeTeams := partitionReviewersByType(toRemove)
	allRemoveUsers := append(removeUsers, removeBots...)

	wg := errgroup.Group{}
	wg.Go(func() error {
		return api.AddPullRequestReviews(client, repo, number, allAddUsers, addTeams)
	})
	wg.Go(func() error {
		return api.RemovePullRequestReviews(client, repo, number, allRemoveUsers, removeTeams)
	})
	return wg.Wait()
}

type Surveyor interface {
	FieldsToEdit(*shared.Editable) error
	EditFields(*shared.Editable, string) error
}

type surveyor struct {
	P shared.EditPrompter
}

func (s surveyor) FieldsToEdit(editable *shared.Editable) error {
	return shared.FieldsToEditSurvey(s.P, editable)
}

func (s surveyor) EditFields(editable *shared.Editable, editorCmd string) error {
	return shared.EditFieldsSurvey(s.P, editable, editorCmd)
}

type EditableOptionsFetcher interface {
	EditableOptionsFetch(*api.Client, ghrepo.Interface, *shared.Editable, gh.ProjectsV1Support) error
}

type fetcher struct{}

func (f fetcher) EditableOptionsFetch(client *api.Client, repo ghrepo.Interface, opts *shared.Editable, projectsV1Support gh.ProjectsV1Support) error {
	return shared.FetchOptions(client, repo, opts, projectsV1Support)
}

type EditorRetriever interface {
	Retrieve() (string, error)
}

type editorRetriever struct {
	config func() (gh.Config, error)
}

func (e editorRetriever) Retrieve() (string, error) {
	return cmdutil.DetermineEditor(e.config)
}

// partitionReviewersByType 将审核者标识符拆分为用户、bot 与团队。
// 团队标识符采用 "org/slug" 形式并原样返回。
// bot 登录名(目前仅 Copilot)会被识别并单独返回。
func partitionReviewersByType(values []string) (users []string, bots []string, teams []string) {
	for _, v := range values {
		if v == "" {
			continue
		}
		if strings.ContainsRune(v, '/') {
			// 团队: org/slug 格式，原样传递
			teams = append(teams, v)
		} else if v == api.CopilotReviewerLogin {
			bots = append(bots, v)
		} else {
			users = append(users, v)
		}
	}
	return
}
