package edit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type iprompter interface {
	MultiSelect(prompt string, defaults []string, options []string) ([]int, error)
	Input(string, string) (string, error)
	Confirm(string, bool) (bool, error)
	Select(string, string, []string) (int, error)
}

const (
	allowMergeCommits = "允许合并提交"
	allowSquashMerge  = "允许压缩合并"
	allowRebaseMerge  = "允许变基合并"

	squashMsgDefault            = "default"
	squashMsgPRTitle            = "pr-title"
	squashMsgPRTitleCommits     = "pr-title-commits"
	squashMsgPRTitleDescription = "pr-title-description"

	optionAllowForking      = "允许 fork"
	optionDefaultBranchName = "默认分支名称"
	optionDescription       = "描述"
	optionHomePageURL       = "主页 URL"
	optionIssues            = "议题"
	optionMergeOptions      = "合并选项"
	optionProjects          = "项目"
	optionTemplateRepo      = "模板仓库"
	optionTopics            = "主题"
	optionVisibility        = "可见性"
	optionWikis             = "维基"

	// TODO: GitHub Enterprise Server 尚不支持 has_discussions
	// optionDiscussions = "讨论"
)

type EditOptions struct {
	HTTPClient                         *http.Client
	Repository                         ghrepo.Interface
	IO                                 *iostreams.IOStreams
	Edits                              EditRepositoryInput
	AddTopics                          []string
	RemoveTopics                       []string
	AcceptVisibilityChangeConsequences bool
	InteractiveMode                    bool
	Detector                           fd.Detector
	Prompter                           iprompter
	// 当前仓库主题的缓存, 以避免在多个流程中重复获取。
	topicsCache []string
}

type EditRepositoryInput struct {
	enableAdvancedSecurity             *bool
	enableSecretScanning               *bool
	enableSecretScanningPushProtection *bool
	squashMergeCommitMsg               *string

	AllowForking             *bool                     `json:"allow_forking,omitempty"`
	AllowUpdateBranch        *bool                     `json:"allow_update_branch,omitempty"`
	DefaultBranch            *string                   `json:"default_branch,omitempty"`
	DeleteBranchOnMerge      *bool                     `json:"delete_branch_on_merge,omitempty"`
	Description              *string                   `json:"description,omitempty"`
	EnableAutoMerge          *bool                     `json:"allow_auto_merge,omitempty"`
	EnableIssues             *bool                     `json:"has_issues,omitempty"`
	EnableMergeCommit        *bool                     `json:"allow_merge_commit,omitempty"`
	EnableProjects           *bool                     `json:"has_projects,omitempty"`
	EnableDiscussions        *bool                     `json:"has_discussions,omitempty"`
	EnableRebaseMerge        *bool                     `json:"allow_rebase_merge,omitempty"`
	EnableSquashMerge        *bool                     `json:"allow_squash_merge,omitempty"`
	EnableWiki               *bool                     `json:"has_wiki,omitempty"`
	Homepage                 *string                   `json:"homepage,omitempty"`
	IsTemplate               *bool                     `json:"is_template,omitempty"`
	SecurityAndAnalysis      *SecurityAndAnalysisInput `json:"security_and_analysis,omitempty"`
	SquashMergeCommitTitle   *string                   `json:"squash_merge_commit_title,omitempty"`
	SquashMergeCommitMessage *string                   `json:"squash_merge_commit_message,omitempty"`
	Visibility               *string                   `json:"visibility,omitempty"`
}

func NewCmdEdit(f *cmdutil.Factory, runF func(options *EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:       f.IOStreams,
		Prompter: f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "edit [<repository>]",
		Short: "编辑仓库设置",
		Annotations: map[string]string{
			"help:arguments": heredoc.Doc(`
				可以通过以下任意格式在参数中提供仓库:
				- "OWNER/REPO"
				- 通过 URL, 例如 "https://github.com/OWNER/REPO"
			`),
		},
		Long: heredoc.Docf(`
			编辑仓库设置。

			若要关闭某个设置, 请使用 %[1]s--<flag>=false%[1]s 语法。

			更改仓库可见性可能带来意想不到的后果, 包括但不限于:

			- 失去 Star 与关注者, 影响仓库排名
			- 将公开的 fork 仓库从网络中分离
			- 禁用推送规则集
			- 允许访问 GitHub Actions 历史记录与日志

			使用 %[1]s--visibility%[1]s 选项时, 必须同时使用 %[1]s--accept-visibility-change-consequences%[1]s 选项。

			有关所有潜在后果的信息, 请参见:
			<https://gh.io/setting-repository-visibility>。

			使用 %[1]s--enable-squash-merge%[1]s 选项时, 可使用 %[1]s--squash-merge-commit-message%[1]s
			来更改默认的压缩合并(squash merge)提交信息行为:

			- %[1]s%[2]s%[1]s: 单个提交时使用提交标题与信息, 两个及以上提交时使用拉取请求标题与提交列表
			- %[1]s%[3]s%[1]s: 使用拉取请求标题
			- %[1]s%[4]s%[1]s: 使用拉取请求标题与提交列表
			- %[1]s%[5]s%[1]s: 使用拉取请求标题与描述
		`, "`", squashMsgDefault, squashMsgPRTitle, squashMsgPRTitleCommits, squashMsgPRTitleDescription),
		Args: cobra.MaximumNArgs(1),
		Example: heredoc.Doc(`
			# 启用 issue 与 wiki
			$ gh repo edit --enable-issues --enable-wiki

			# 禁用项目
			$ gh repo edit --enable-projects=false
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				var err error
				opts.Repository, err = ghrepo.FromFullName(args[0])
				if err != nil {
					return err
				}
			} else {
				var err error
				opts.Repository, err = f.BaseRepo()
				if err != nil {
					return err
				}
			}

			if httpClient, err := f.HttpClient(); err == nil {
				opts.HTTPClient = httpClient
			} else {
				return err
			}

			if cmd.Flags().NFlag() == 0 {
				opts.InteractiveMode = true
			}

			if opts.InteractiveMode && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互模式下运行时, 请指定要编辑的属性")
			}

			if opts.Edits.Visibility != nil && !opts.AcceptVisibilityChangeConsequences {
				return cmdutil.FlagErrorf("使用 --visibility 选项时需要 --accept-visibility-change-consequences 选项")
			}

			if opts.Edits.squashMergeCommitMsg != nil {
				if opts.Edits.EnableSquashMerge == nil {
					return cmdutil.FlagErrorf("--squash-merge-commit-message 需要 --enable-squash-merge")
				}
				if !*opts.Edits.EnableSquashMerge {
					return cmdutil.FlagErrorf("--enable-squash-merge=false 时不能使用 --squash-merge-commit-message")
				}
				if err := validateSquashMergeCommitMsg(*opts.Edits.squashMergeCommitMsg); err != nil {
					return err
				}
				transformSquashMergeOpts(&opts.Edits)
			}

			if hasSecurityEdits(opts.Edits) {
				opts.Edits.SecurityAndAnalysis = transformSecurityAndAnalysisOpts(opts)
			}

			if runF != nil {
				return runF(opts)
			}
			return editRun(cmd.Context(), opts)
		},
	}

	cmdutil.NilStringFlag(cmd, &opts.Edits.Description, "description", "d", "仓库描述")
	cmdutil.NilStringFlag(cmd, &opts.Edits.Homepage, "homepage", "h", "仓库主页 `URL`")
	cmdutil.NilStringFlag(cmd, &opts.Edits.DefaultBranch, "default-branch", "", "设置仓库的默认分支名称")
	cmdutil.NilStringFlag(cmd, &opts.Edits.Visibility, "visibility", "", "将仓库可见性更改为 {public,private,internal}")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.IsTemplate, "template", "", "将仓库作为模板仓库提供")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableIssues, "enable-issues", "", "在仓库中启用 issue")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableProjects, "enable-projects", "", "在仓库中启用项目")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableWiki, "enable-wiki", "", "在仓库中启用 wiki")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableDiscussions, "enable-discussions", "", "在仓库中启用讨论")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableMergeCommit, "enable-merge-commit", "", "启用通过合并提交(merge commit)合并 PR")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableSquashMerge, "enable-squash-merge", "", "启用通过压缩提交(squashed commit)合并 PR")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableRebaseMerge, "enable-rebase-merge", "", "启用通过变基(rebase)合并 PR")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.EnableAutoMerge, "enable-auto-merge", "", "启用自动合并功能")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.enableAdvancedSecurity, "enable-advanced-security", "", "在仓库中启用高级安全功能")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.enableSecretScanning, "enable-secret-scanning", "", "在仓库中启用密钥扫描")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.enableSecretScanningPushProtection, "enable-secret-scanning-push-protection", "", "在仓库中启用密钥扫描推送保护(必须先启用密钥扫描)")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.DeleteBranchOnMerge, "delete-branch-on-merge", "", "PR 合并后删除源分支")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.AllowForking, "allow-forking", "", "允许 fork 组织仓库")
	cmdutil.NilBoolFlag(cmd, &opts.Edits.AllowUpdateBranch, "allow-update-branch", "", "允许更新落后于其基础分支的 PR 源分支")
	cmdutil.NilStringFlag(cmd, &opts.Edits.squashMergeCommitMsg, "squash-merge-commit-message", "", "压缩合并提交信息的默认值: {default|pr-title|pr-title-commits|pr-title-description}")
	cmd.Flags().StringSliceVar(&opts.AddTopics, "add-topic", nil, "添加仓库主题")
	cmd.Flags().StringSliceVar(&opts.RemoveTopics, "remove-topic", nil, "移除仓库主题")
	cmd.Flags().BoolVar(&opts.AcceptVisibilityChangeConsequences, "accept-visibility-change-consequences", false, "接受更改仓库可见性所带来的后果")

	return cmd
}

func editRun(ctx context.Context, opts *EditOptions) error {
	repo := opts.Repository

	if opts.InteractiveMode {
		detector := opts.Detector
		if detector == nil {
			cachedClient := api.NewCachedHTTPClient(opts.HTTPClient, time.Hour*24)
			detector = fd.NewDetector(cachedClient, repo.RepoHost())
		}
		repoFeatures, err := detector.RepositoryFeatures()
		if err != nil {
			return err
		}

		apiClient := api.NewClientFromHTTP(opts.HTTPClient)
		fieldsToRetrieve := []string{
			"defaultBranchRef",
			"deleteBranchOnMerge",
			"description",
			"hasIssuesEnabled",
			"hasProjectsEnabled",
			"hasWikiEnabled",
			// TODO: GitHub Enterprise Server does not support has_discussions yet
			// "hasDiscussionsEnabled",
			"homepageUrl",
			"isInOrganization",
			"isTemplate",
			"mergeCommitAllowed",
			"rebaseMergeAllowed",
			"repositoryTopics",
			"stargazerCount",
			"squashMergeAllowed",
			"watchers",
		}
		// TODO repoFeaturesCleanup
		if repoFeatures.VisibilityField {
			fieldsToRetrieve = append(fieldsToRetrieve, "visibility")
		}
		// TODO repoFeaturesCleanup
		if repoFeatures.AutoMerge {
			fieldsToRetrieve = append(fieldsToRetrieve, "autoMergeAllowed")
		}

		opts.IO.StartProgressIndicator()
		fetchedRepo, err := api.FetchRepository(apiClient, opts.Repository, fieldsToRetrieve)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}
		err = interactiveRepoEdit(opts, fetchedRepo)
		if err != nil {
			return err
		}
	}

	if opts.Edits.SecurityAndAnalysis != nil {
		apiClient := api.NewClientFromHTTP(opts.HTTPClient)
		repo, err := api.FetchRepository(apiClient, opts.Repository, []string{"viewerCanAdminister"})
		if err != nil {
			return err
		}
		if !repo.ViewerCanAdminister {
			return fmt.Errorf("您没有足够的权限编辑仓库的安全与分析功能")
		}
	}

	apiPath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName())
	if err != nil {
		return err
	}

	body := &bytes.Buffer{}
	enc := json.NewEncoder(body)
	if err := enc.Encode(opts.Edits); err != nil {
		return err
	}

	g := errgroup.Group{}

	if body.Len() > 3 {
		g.Go(func() error {
			apiClient := api.NewClientFromHTTP(opts.HTTPClient)
			_, err := api.CreateRepoTransformToV4(apiClient, repo.RepoHost(), "PATCH", apiPath, body)
			return err
		})
	}

	if len(opts.AddTopics) > 0 || len(opts.RemoveTopics) > 0 {
		g.Go(func() error {
			// opts.topicsCache gets populated in interactive mode
			if !opts.InteractiveMode {
				var err error
				opts.topicsCache, err = getTopics(ctx, opts.HTTPClient, repo)
				if err != nil {
					return err
				}
			}
			oldTopics := set.NewStringSet()
			oldTopics.AddValues(opts.topicsCache)

			newTopics := set.NewStringSet()
			newTopics.AddValues(opts.topicsCache)
			newTopics.AddValues(opts.AddTopics)
			newTopics.RemoveValues(opts.RemoveTopics)

			if oldTopics.Equal(newTopics) {
				return nil
			}
			return setTopics(ctx, opts.HTTPClient, repo, newTopics.ToSlice())
		})
	}

	err = g.Wait()
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out,
			"%s 已编辑仓库 %s\n",
			cs.SuccessIcon(),
			ghrepo.FullName(repo))
	}

	return nil
}

func interactiveChoice(p iprompter, r *api.Repository) ([]string, error) {
	options := []string{
		optionDefaultBranchName,
		optionDescription,
		optionHomePageURL,
		optionIssues,
		optionMergeOptions,
		optionProjects,
		// TODO: GitHub Enterprise Server does not support has_discussions yet
		// optionDiscussions,
		optionTemplateRepo,
		optionTopics,
		optionVisibility,
		optionWikis,
	}
	if r.IsInOrganization {
		options = append(options, optionAllowForking)
	}
	var answers []string
	selected, err := p.MultiSelect("您想编辑什么?", nil, options)
	if err != nil {
		return nil, err
	}
	for _, i := range selected {
		answers = append(answers, options[i])
	}

	return answers, err
}

func interactiveRepoEdit(opts *EditOptions, r *api.Repository) error {
	for _, v := range r.RepositoryTopics.Nodes {
		opts.topicsCache = append(opts.topicsCache, v.Topic.Name)
	}
	p := opts.Prompter
	choices, err := interactiveChoice(p, r)
	if err != nil {
		return err
	}
	for _, c := range choices {
		switch c {
		case optionDescription:
			answer, err := p.Input("仓库描述", r.Description)
			if err != nil {
				return err
			}
			opts.Edits.Description = &answer
		case optionHomePageURL:
			a, err := p.Input("仓库主页 URL", r.HomepageURL)
			if err != nil {
				return err
			}
			opts.Edits.Homepage = &a
		case optionTopics:
			addTopics, err := p.Input("添加主题?(csv 格式)", "")
			if err != nil {
				return err
			}
			if len(strings.TrimSpace(addTopics)) > 0 {
				opts.AddTopics = parseTopics(addTopics)
			}

			if len(opts.topicsCache) > 0 {
				selected, err := p.MultiSelect("移除主题", nil, opts.topicsCache)
				if err != nil {
					return err
				}
				for _, i := range selected {
					opts.RemoveTopics = append(opts.RemoveTopics, opts.topicsCache[i])
				}
			}
		case optionDefaultBranchName:
			name, err := p.Input("默认分支名称", r.DefaultBranchRef.Name)
			if err != nil {
				return err
			}
			opts.Edits.DefaultBranch = &name
		case optionWikis:
			c, err := p.Confirm("是否启用 wiki ?", r.HasWikiEnabled)
			if err != nil {
				return err
			}
			opts.Edits.EnableWiki = &c
		case optionIssues:
			a, err := p.Confirm("是否启用 issue ?", r.HasIssuesEnabled)
			if err != nil {
				return err
			}
			opts.Edits.EnableIssues = &a
		case optionProjects:
			a, err := p.Confirm("是否启用项目?", r.HasProjectsEnabled)
			if err != nil {
				return err
			}
			opts.Edits.EnableProjects = &a
		case optionVisibility:
			cs := opts.IO.ColorScheme()
			fmt.Fprintf(opts.IO.ErrOut, "%s 危险区域: 更改仓库可见性可能带来意想不到的后果；请在继续之前查阅 https://gh.io/setting-repository-visibility。\n", cs.WarningIcon())

			visibilityOptions := []string{"public", "private", "internal"}
			selected, err := p.Select("可见性", strings.ToLower(r.Visibility), visibilityOptions)
			if err != nil {
				return err
			}
			selectedVisibility := visibilityOptions[selected]

			if selectedVisibility != r.Visibility && (r.StargazerCount > 0 || r.Watchers.TotalCount > 0) {
				fmt.Fprintf(opts.IO.ErrOut, "%s 将仓库可见性更改为 %s 将导致永久失去 %s 和 %s\n", cs.WarningIcon(), selectedVisibility, text.Pluralize(r.StargazerCount, "star"), text.Pluralize(r.Watchers.TotalCount, "watcher"))
			}

			confirmed, err := p.Confirm(fmt.Sprintf("是否要将可见性更改为 %s?", selectedVisibility), false)
			if err != nil {
				return err
			}
			if confirmed {
				opts.Edits.Visibility = &selectedVisibility
			}
		case optionMergeOptions:
			var defaultMergeOptions []string
			var selectedMergeOptions []string
			if r.MergeCommitAllowed {
				defaultMergeOptions = append(defaultMergeOptions, allowMergeCommits)
			}
			if r.SquashMergeAllowed {
				defaultMergeOptions = append(defaultMergeOptions, allowSquashMerge)
			}
			if r.RebaseMergeAllowed {
				defaultMergeOptions = append(defaultMergeOptions, allowRebaseMerge)
			}
			mergeOpts := []string{allowMergeCommits, allowSquashMerge, allowRebaseMerge}
			selected, err := p.MultiSelect(
				"允许的合并策略",
				defaultMergeOptions,
				mergeOpts)
			if err != nil {
				return err
			}
			for _, i := range selected {
				selectedMergeOptions = append(selectedMergeOptions, mergeOpts[i])
			}
			enableMergeCommit := isIncluded(allowMergeCommits, selectedMergeOptions)
			opts.Edits.EnableMergeCommit = &enableMergeCommit
			enableSquashMerge := isIncluded(allowSquashMerge, selectedMergeOptions)
			opts.Edits.EnableSquashMerge = &enableSquashMerge
			enableRebaseMerge := isIncluded(allowRebaseMerge, selectedMergeOptions)
			opts.Edits.EnableRebaseMerge = &enableRebaseMerge
			if !enableMergeCommit && !enableSquashMerge && !enableRebaseMerge {
				return fmt.Errorf("您需要至少允许一种合并策略")
			}

			if enableSquashMerge {
				squashMsgOptions := validSquashMsgValues
				idx, err := p.Select(
					"默认的压缩合并提交信息",
					squashMsgDefault,
					squashMsgOptions)
				if err != nil {
					return err
				}
				selected := squashMsgOptions[idx]
				opts.Edits.squashMergeCommitMsg = &selected
				transformSquashMergeOpts(&opts.Edits)
			}

			opts.Edits.EnableAutoMerge = &r.AutoMergeAllowed
			c, err := p.Confirm("是否启用自动合并?", r.AutoMergeAllowed)
			if err != nil {
				return err
			}
			opts.Edits.EnableAutoMerge = &c

			opts.Edits.DeleteBranchOnMerge = &r.DeleteBranchOnMerge
			c, err = p.Confirm(
				"合并后是否自动删除源分支?", r.DeleteBranchOnMerge)
			if err != nil {
				return err
			}
			opts.Edits.DeleteBranchOnMerge = &c
		case optionTemplateRepo:
			c, err := p.Confirm("是否转换为模板仓库?", r.IsTemplate)
			if err != nil {
				return err
			}
			opts.Edits.IsTemplate = &c
		case optionAllowForking:
			c, err := p.Confirm(
				"是否允许 fork(组织仓库)?",
				r.ForkingAllowed)
			if err != nil {
				return err
			}
			opts.Edits.AllowForking = &c
		}
	}
	return nil
}

func parseTopics(s string) []string {
	topics := strings.Split(s, ",")
	for i, topic := range topics {
		topics[i] = strings.TrimSpace(topic)
	}
	return topics
}

func getTopics(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface) ([]string, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "topics")
	if err != nil {
		return nil, err
	}

	// 某些 GitHub Enterprise 版本仍需要 "mercy-preview"
	// TODO(api-client-rollout)
	// 这行代码是 API 客户端机械化推广的一部分。
	// 作为后续工作, 考虑是否可以在此调用点注入 API 客户端, 而不是在此构造
	res, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, repo.RepoHost(), http.MethodGet, path.String(), nil,
		api.WithHeader("Accept", "application/vnd.github.mercy-preview+json"))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, api.UnexpectedStatusError(res)
	}

	var responseData struct {
		Names []string `json:"names"`
	}
	dec := json.NewDecoder(res.Body)
	err = dec.Decode(&responseData)
	return responseData.Names, err
}

func setTopics(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, topics []string) error {
	payload := struct {
		Names []string `json:"names"`
	}{
		Names: topics,
	}
	body := &bytes.Buffer{}
	dec := json.NewEncoder(body)
	if err := dec.Encode(&payload); err != nil {
		return err
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "topics")
	if err != nil {
		return err
	}

	// 某些 GitHub Enterprise 版本仍需要 "mercy-preview"
	// TODO(api-client-rollout)
	// 这行代码是 API 客户端机械化推广的一部分。
	// 作为后续工作, 考虑是否可以在此调用点注入 API 客户端, 而不是在此构造
	res, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, repo.RepoHost(), http.MethodPut, path.String(), body,
		api.WithHeader("Content-Type", "application/json"),
		api.WithHeader("Accept", "application/vnd.github.mercy-preview+json"))
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return api.UnexpectedStatusError(res)
	}

	if res.Body != nil {
		_, _ = io.Copy(io.Discard, res.Body)
	}

	return nil
}

func isIncluded(value string, opts []string) bool {
	for _, opt := range opts {
		if strings.EqualFold(opt, value) {
			return true
		}
	}
	return false
}

func boolToStatus(status bool) *string {
	var result string
	if status {
		result = "enabled"
	} else {
		result = "disabled"
	}
	return &result
}

func hasSecurityEdits(edits EditRepositoryInput) bool {
	return edits.enableAdvancedSecurity != nil || edits.enableSecretScanning != nil || edits.enableSecretScanningPushProtection != nil
}

type SecurityAndAnalysisInput struct {
	EnableAdvancedSecurity             *SecurityAndAnalysisStatus `json:"advanced_security,omitempty"`
	EnableSecretScanning               *SecurityAndAnalysisStatus `json:"secret_scanning,omitempty"`
	EnableSecretScanningPushProtection *SecurityAndAnalysisStatus `json:"secret_scanning_push_protection,omitempty"`
}

type SecurityAndAnalysisStatus struct {
	Status *string `json:"status,omitempty"`
}

// 转换安全与分析参数, 以正确序列化 EditRepositoryInput
// 参见 API 文档: https://docs.github.com/en/rest/repos/repos?apiVersion=2022-11-28#update-a-repository
func transformSecurityAndAnalysisOpts(opts *EditOptions) *SecurityAndAnalysisInput {
	securityOptions := &SecurityAndAnalysisInput{}
	if opts.Edits.enableAdvancedSecurity != nil {
		securityOptions.EnableAdvancedSecurity = &SecurityAndAnalysisStatus{
			Status: boolToStatus(*opts.Edits.enableAdvancedSecurity),
		}
	}
	if opts.Edits.enableSecretScanning != nil {
		securityOptions.EnableSecretScanning = &SecurityAndAnalysisStatus{
			Status: boolToStatus(*opts.Edits.enableSecretScanning),
		}
	}
	if opts.Edits.enableSecretScanningPushProtection != nil {
		securityOptions.EnableSecretScanningPushProtection = &SecurityAndAnalysisStatus{
			Status: boolToStatus(*opts.Edits.enableSecretScanningPushProtection),
		}
	}
	return securityOptions
}

var validSquashMsgValues = []string{squashMsgDefault, squashMsgPRTitle, squashMsgPRTitleCommits, squashMsgPRTitleDescription}

func validateSquashMergeCommitMsg(value string) error {
	if slices.Contains(validSquashMsgValues, value) {
		return nil
	}
	return cmdutil.FlagErrorf("--squash-merge-commit-message 的值无效: %q，有效值为: %s", value, strings.Join(validSquashMsgValues, ", "))
}

// transformSquashMergeOpts 将面向用户的压缩合并提交信息选项
// 映射到两个 API 字段: squash_merge_commit_title 和 squash_merge_commit_message。
func transformSquashMergeOpts(edits *EditRepositoryInput) {
	if edits.squashMergeCommitMsg == nil {
		return
	}
	var title, message string
	switch *edits.squashMergeCommitMsg {
	case squashMsgDefault:
		title = "COMMIT_OR_PR_TITLE"
		message = "COMMIT_MESSAGES"
	case squashMsgPRTitle:
		title = "PR_TITLE"
		message = "BLANK"
	case squashMsgPRTitleCommits:
		title = "PR_TITLE"
		message = "COMMIT_MESSAGES"
	case squashMsgPRTitleDescription:
		title = "PR_TITLE"
		message = "PR_BODY"
	default:
		return
	}
	edits.SquashMergeCommitTitle = &title
	edits.SquashMergeCommitMessage = &message
}
