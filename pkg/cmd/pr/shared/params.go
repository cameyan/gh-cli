package shared

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/google/shlex"
)

func WithPrAndIssueQueryParams(client *api.Client, baseRepo ghrepo.Interface, baseURL string, state IssueMetadataState, projectsV1Support gh.ProjectsV1Support) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if state.Title != "" {
		q.Set("title", state.Title)
	}
	// 我们始终希望发送 body 参数，即使它为空，以防止 web 界面应用默认模板。
	// 由于用户可以在终端中选择模板，这里的空正文意味着用户要么跳过了它，要么清空了其内容。
	q.Set("body", state.Body)
	if len(state.Assignees) > 0 {
		q.Set("assignees", strings.Join(state.Assignees, ","))
	}
	// 未提供 body 参数时设置 template 参数，例如 Web 模式
	if len(state.Template) > 0 && len(state.Body) == 0 {
		q.Set("template", state.Template)
	}
	if len(state.Labels) > 0 {
		q.Set("labels", strings.Join(state.Labels, ","))
	}
	if len(state.ProjectTitles) > 0 {
		projectPaths, err := api.ProjectTitlesToPaths(client, baseRepo, state.ProjectTitles, projectsV1Support)
		if err != nil {
			return "", fmt.Errorf("添加到项目(project)失败: %w", err)
		}
		q.Set("projects", strings.Join(projectPaths, ","))
	}
	if len(state.Milestones) > 0 {
		q.Set("milestone", state.Milestones[0])
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

// URL 的最大长度: 8192 字节
func ValidURL(urlStr string) bool {
	return len(urlStr) < 8192
}

func AddMetadataToIssueParams(client *api.Client, baseRepo ghrepo.Interface, params map[string]any, tb *IssueMetadataState, projectV1Support gh.ProjectsV1Support) error {
	if !tb.HasMetadata() {
		return nil
	}

	// TODO ApiActorsSupported
	// 当 ApiActorsSupported 为 true 时，我们使用基于登录名(login)的 mutation，无需解析审核者(reviewer) ID。
	needReviewerIDs := len(tb.Reviewers) > 0 && !tb.ApiActorsSupported

	// TODO ApiActorsSupported
	// 当 ApiActorsSupported 为 true 时，我们使用基于登录名的 mutation，无需解析被指派人(assignee) ID。
	needAssigneeIDs := len(tb.Assignees) > 0 && !tb.ApiActorsSupported

	// 若此前尚未通过附加元数据调查(survey)缓存，则获取解析元数据所需的最低限度信息。
	if tb.MetadataResult == nil {
		input := api.RepoMetadataInput{
			Reviewers: needReviewerIDs,
			TeamReviewers: needReviewerIDs && slices.ContainsFunc(tb.Reviewers, func(r string) bool {
				return strings.ContainsRune(r, '/')
			}),
			Assignees:  needAssigneeIDs,
			Labels:     len(tb.Labels) > 0,
			ProjectsV1: len(tb.ProjectTitles) > 0 && projectV1Support == gh.ProjectsV1Supported,
			ProjectsV2: len(tb.ProjectTitles) > 0,
			Milestones: len(tb.Milestones) > 0,
		}

		metadataResult, err := api.RepoMetadata(client, baseRepo, input)
		if err != nil {
			return err
		}
		tb.MetadataResult = metadataResult
	}

	// TODO ApiActorsSupported
	// 当 ApiActorsSupported 为 true (github.com)时，直接传递登录名(login)供
	// ReplaceActorsForAssignable mutation 使用。基于 ID 的 else 分支用于 GHES 兼容。
	if tb.ApiActorsSupported {
		params["assigneeLogins"] = tb.Assignees
	} else {
		assigneeIDs, err := tb.MetadataResult.MembersToIDs(tb.Assignees)
		if err != nil {
			return fmt.Errorf("指派用户失败: %w", err)
		}
		params["assigneeIds"] = assigneeIDs
	}

	labelIDs, err := tb.MetadataResult.LabelsToIDs(tb.Labels)
	if err != nil {
		return fmt.Errorf("添加标签(label)失败: %w", err)
	}
	params["labelIds"] = labelIDs

	projectIDs, projectV2IDs, err := tb.MetadataResult.ProjectsTitlesToIDs(tb.ProjectTitles)
	if err != nil {
		return fmt.Errorf("添加到项目(project)失败: %w", err)
	}
	params["projectIds"] = projectIDs
	params["projectV2Ids"] = projectV2IDs

	if len(tb.Milestones) > 0 {
		milestoneID, err := tb.MetadataResult.MilestoneToID(tb.Milestones[0])
		if err != nil {
			return fmt.Errorf("添加到里程碑(milestone) '%s' 失败: %w", tb.Milestones[0], err)
		}
		params["milestoneId"] = milestoneID
	}

	if len(tb.Reviewers) == 0 {
		return nil
	}

	var userReviewers []string
	var botReviewers []string
	var teamReviewers []string
	for _, r := range tb.Reviewers {
		if strings.ContainsRune(r, '/') {
			teamReviewers = append(teamReviewers, r)
		} else if r == api.CopilotReviewerLogin {
			botReviewers = append(botReviewers, r)
		} else {
			userReviewers = append(userReviewers, r)
		}
	}

	// TODO ApiActorsSupported
	// 当 ApiActorsSupported 为 true (github.com)时，直接传递登录名(login)供
	// RequestReviewsByLogin mutation 使用。基于 ID 的 else 分支可在
	// GHES 支持 requestReviewsByLogin 后移除。
	if tb.ApiActorsSupported {
		params["userReviewerLogins"] = userReviewers
		if len(botReviewers) > 0 {
			params["botReviewerLogins"] = botReviewers
		}
		params["teamReviewerSlugs"] = teamReviewers
	} else {
		userReviewerIDs, err := tb.MetadataResult.MembersToIDs(userReviewers)
		if err != nil {
			return fmt.Errorf("请求审核者(reviewer)失败: %w", err)
		}
		params["userReviewerIds"] = userReviewerIDs

		teamReviewerIDs, err := tb.MetadataResult.TeamsToIDs(teamReviewers)
		if err != nil {
			return fmt.Errorf("请求审核者(reviewer)失败: %w", err)
		}
		params["teamReviewerIds"] = teamReviewerIDs
	}

	return nil
}

type FilterOptions struct {
	Assignee   string
	Author     string
	BaseBranch string
	Draft      *bool
	Entity     string
	Fields     []string
	HeadBranch string
	IssueType  string
	Labels     []string
	Mention    string
	Milestone  string
	Repo       string
	Search     string
	State      string
}

func (opts *FilterOptions) IsDefault() bool {
	if opts.State != "open" {
		return false
	}
	if len(opts.Labels) > 0 {
		return false
	}
	if opts.Assignee != "" {
		return false
	}
	if opts.Author != "" {
		return false
	}
	if opts.BaseBranch != "" {
		return false
	}
	if opts.HeadBranch != "" {
		return false
	}
	if opts.Mention != "" {
		return false
	}
	if opts.Milestone != "" {
		return false
	}
	if opts.Search != "" {
		return false
	}
	if opts.IssueType != "" {
		return false
	}
	return true
}

func ListURLWithQuery(listURL string, options FilterOptions, advancedIssueSearchSyntax bool) (string, error) {
	u, err := url.Parse(listURL)
	if err != nil {
		return "", err
	}

	params := u.Query()
	params.Set("q", SearchQueryBuild(options, advancedIssueSearchSyntax))
	u.RawQuery = params.Encode()

	return u.String(), nil
}

func SearchQueryBuild(options FilterOptions, advancedIssueSearchSyntax bool) string {
	var is, state string
	switch options.State {
	case "open", "closed":
		state = options.State
	case "merged":
		is = "merged"
	}

	query := search.Query{
		Qualifiers: search.Qualifiers{
			Assignee:  options.Assignee,
			Author:    options.Author,
			Base:      options.BaseBranch,
			Draft:     options.Draft,
			Head:      options.HeadBranch,
			IssueType: options.IssueType,
			Label:     options.Labels,
			Mentions:  options.Mention,
			Milestone: options.Milestone,
			Repo:      []string{options.Repo},
			State:     state,
			Is:        []string{is},
			Type:      options.Entity,
		},
		ImmutableKeywords: options.Search,
	}

	if !advancedIssueSearchSyntax {
		return query.StandardSearchString()
	}
	return query.AdvancedIssueSearchString()
}

func QueryHasStateClause(searchQuery string) bool {
	argv, err := shlex.Split(searchQuery)
	if err != nil {
		return false
	}

	for _, arg := range argv {
		if arg == "is:closed" || arg == "is:merged" || arg == "state:closed" || arg == "state:merged" || strings.HasPrefix(arg, "merged:") || strings.HasPrefix(arg, "closed:") {
			return true
		}
	}

	return false
}

// MeReplacer 将 `@me` 的用法解析为当前登录用户的 handle。
type MeReplacer struct {
	apiClient *api.Client
	hostname  string
	login     string
}

func NewMeReplacer(apiClient *api.Client, hostname string) *MeReplacer {
	return &MeReplacer{
		apiClient: apiClient,
		hostname:  hostname,
	}
}

func (r *MeReplacer) currentLogin() (string, error) {
	if r.login != "" {
		return r.login, nil
	}
	login, err := api.CurrentLoginName(r.apiClient, r.hostname)
	if err != nil {
		return "", fmt.Errorf("将 `@me` 解析为您的用户 handle 失败: %w", err)
	}
	r.login = login
	return login, nil
}

func (r *MeReplacer) Replace(handle string) (string, error) {
	if handle == "@me" {
		return r.currentLogin()
	}
	return handle, nil
}

func (r *MeReplacer) ReplaceSlice(handles []string) ([]string, error) {
	res := make([]string, len(handles))
	for i, h := range handles {
		var err error
		res[i], err = r.Replace(h)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

// CopilotReplacer 将 `@copilot` 的用法解析为 Copilot 的登录名(login)或名称。
// API 调用通常需要登录名；名称用于启动 web 浏览器。
type CopilotReplacer struct {
	returnLogin bool
	// copilotLogin 是替换 @copilot 时使用的登录名。
	// 不同的 Copilot 特性使用不同的 bot 登录名。
	copilotLogin string
}

// NewCopilotReplacer 为被指派人(assignee)的 @copilot 引用创建替换器。
func NewCopilotReplacer(returnLogin bool) *CopilotReplacer {
	return &CopilotReplacer{
		returnLogin:  returnLogin,
		copilotLogin: api.CopilotAssigneeLogin,
	}
}

// NewCopilotReviewerReplacer 为审核者(reviewer)的 @copilot 引用创建替换器。
func NewCopilotReviewerReplacer() *CopilotReplacer {
	return &CopilotReplacer{
		returnLogin:  true,
		copilotLogin: api.CopilotReviewerLogin,
	}
}

func (r *CopilotReplacer) replace(handle string) string {
	if !strings.EqualFold(handle, "@copilot") {
		return handle
	}
	if r.returnLogin {
		return r.copilotLogin
	}
	return api.CopilotActorName
}

// ReplaceSlice 将切片中 `@copilot` 的用法替换为 Copilot 的登录名(login)。
func (r *CopilotReplacer) ReplaceSlice(handles []string) []string {
	res := make([]string, len(handles))
	for i, h := range handles {
		res[i] = r.replace(h)
	}
	return res
}
