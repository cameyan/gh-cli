package shared

import (
	"fmt"
	"slices"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/set"
)

type Editable struct {
	Title              EditableString
	Body               EditableString
	Base               EditableString
	Reviewers          EditableReviewers
	ReviewerSearchFunc func(string) prompter.MultiSelectSearchResult
	Assignees          EditableAssignees
	AssigneeSearchFunc func(string) prompter.MultiSelectSearchResult
	Labels             EditableSlice
	Projects           EditableProjects
	Milestone          EditableString
	IssueType          EditableString
	IssueTypeNameToID  map[string]string
	Metadata           api.RepoMetadataResult

	// TODO ApiActorsSupported
	// ApiActorsSupported 表示该主机支持基于 actor 的 API (github.com, ghe.com)。
	// 为 true 时，mutation 直接使用登录名(login)而不是解析节点 ID。
	// 待 GHES 支持 replaceActorsForAssignable 与 requestReviewsByLogin mutation 后，
	// 可移除该 flag(并收敛为仅 actor 路径)。
	ApiActorsSupported bool
}

type EditableString struct {
	Value   string
	Default string
	Options []string
	Edited  bool
	// Selectable 控制交互式调查(survey)是否提供该字段供用户选择编辑。
	// 仅通过 flag 修改的字段保持为 false。
	Selectable bool
}

type EditableSlice struct {
	Value   []string
	Add     []string
	Remove  []string
	Default []string
	Options []string
	Edited  bool
	// Selectable 控制交互式调查是否提供该字段供用户选择编辑。
	// 仅通过 flag 修改的字段保持为 false。
	Selectable bool
}

// EditableAssignees 是 EditableSlice 的特例。
type EditableAssignees struct {
	EditableSlice
	DefaultLogins []string // 用于区分 actor 与显示名
}

// EditableReviewers 是 EditableSlice 的特例。
type EditableReviewers struct {
	EditableSlice
	DefaultLogins []string // 用于区分 actor 与显示名
}

// ProjectsV2 mutation 需要将 item ID 映射到 project ID。
// 将该映射与标准的 EditableSlice 数据保存在一起。
type EditableProjects struct {
	EditableSlice
	ProjectItems map[string]string
}

func (e Editable) Dirty() bool {
	return e.Title.Edited ||
		e.Body.Edited ||
		e.Base.Edited ||
		e.Reviewers.Edited ||
		e.Assignees.Edited ||
		e.Labels.Edited ||
		e.Projects.Edited ||
		e.Milestone.Edited ||
		e.IssueType.Edited
}

func (e Editable) TitleValue() *string {
	if !e.Title.Edited {
		return nil
	}
	return &e.Title.Value
}

func (e Editable) BodyValue() *string {
	if !e.Body.Edited {
		return nil
	}
	return &e.Body.Value
}

func (e Editable) AssigneeIds(client *api.Client, repo ghrepo.Interface) (*[]string, error) {
	if !e.Assignees.Edited {
		return nil, nil
	}

	// 若被指派人(assignee)来自命令行 flag，则需要从默认列表
	// 精选出最终的被指派人列表。
	if len(e.Assignees.Add) != 0 || len(e.Assignees.Remove) != 0 {
		// TODO ApiActorsSupported
		replacer := NewSpecialAssigneeReplacer(client, repo.RepoHost(), e.ApiActorsSupported, true)

		assigneeSet := set.NewStringSet()

		// 下面这个检查是必需的，因为非交互流程中用户给的是登录名(login)
		// 而不是显示名(DisplayName)；而当存在 actor 被指派人时，
		// e.Assignees.Default 包含的是 DisplayNames 而非 logins
		// (这是为了在交互流程中容纳特殊的 actor 显示名)。
		// 因此这里需要添加默认 logins 而不是 DisplayNames。
		// 否则，用户提供的值在待添加/待移除集合中找不到，导致意外行为。
		// TODO ApiActorsSupported
		if e.ApiActorsSupported {
			assigneeSet.AddValues(e.Assignees.DefaultLogins)
		} else {
			assigneeSet.AddValues(e.Assignees.Default)
		}

		add, err := replacer.ReplaceSlice(e.Assignees.Add)
		if err != nil {
			return nil, err
		}
		assigneeSet.AddValues(add)

		remove, err := replacer.ReplaceSlice(e.Assignees.Remove)
		if err != nil {
			return nil, err
		}
		assigneeSet.RemoveValues(remove)

		e.Assignees.Value = assigneeSet.ToSlice()
	}
	a, err := e.Metadata.MembersToIDs(e.Assignees.Value)
	return &a, err
}

// AssigneeLogins 从当前默认值加上 Add/Remove 操作计算出最终的被指派人(assignee)
// 登录名(login)列表。与 AssigneeIds 不同，它不把 logins 解析为节点 ID，
// 用于 github.com，其 ReplaceActorsForAssignable mutation 直接接受 logins。
func (e Editable) AssigneeLogins(client *api.Client, repo ghrepo.Interface) ([]string, error) {
	if !e.Assignees.Edited {
		return nil, nil
	}

	if len(e.Assignees.Add) != 0 || len(e.Assignees.Remove) != 0 {
		replacer := NewSpecialAssigneeReplacer(client, repo.RepoHost(), true, true)

		assigneeSet := set.NewStringSet()
		assigneeSet.AddValues(e.Assignees.DefaultLogins)

		add, err := replacer.ReplaceSlice(e.Assignees.Add)
		if err != nil {
			return nil, err
		}
		assigneeSet.AddValues(add)

		remove, err := replacer.ReplaceSlice(e.Assignees.Remove)
		if err != nil {
			return nil, err
		}
		assigneeSet.RemoveValues(remove)

		e.Assignees.Value = assigneeSet.ToSlice()
	}

	return e.Assignees.Value, nil
}

// SpecialAssigneeReplacer 展开登录名(login)切片中的特殊被指派人名称(@me、Copilot actor)。
// 使用 NewSpecialAssigneeReplacer 创建。
type SpecialAssigneeReplacer struct {
	meReplacer      *MeReplacer
	copilotReplacer *CopilotReplacer
	actorAssignees  bool
}

// NewSpecialAssigneeReplacer 创建一个替换器，在被指派人切片中展开 @me，
// 以及(当 actorAssignees 为 true 时的)Copilot actor 名称。
// copilotUseLogin 控制 Copilot actor 被替换为登录名(true)还是显示名(false，用于 web 模式)。
func NewSpecialAssigneeReplacer(client *api.Client, host string, actorAssignees bool, copilotUseLogin bool) *SpecialAssigneeReplacer {
	return &SpecialAssigneeReplacer{
		meReplacer:      NewMeReplacer(client, host),
		copilotReplacer: NewCopilotReplacer(copilotUseLogin),
		actorAssignees:  actorAssignees,
	}
}

func (r *SpecialAssigneeReplacer) ReplaceSlice(logins []string) ([]string, error) {
	replaced, err := r.meReplacer.ReplaceSlice(logins)
	if err != nil {
		return nil, err
	}
	if r.actorAssignees {
		replaced = r.copilotReplacer.ReplaceSlice(replaced)
	}
	return replaced, nil
}

// ProjectIds 返回包含该 issue 或 PR 需要关联的 Projects v1 ID 的切片。
func (e Editable) ProjectIds() (*[]string, error) {
	if !e.Projects.Edited {
		return nil, nil
	}
	if len(e.Projects.Add) != 0 || len(e.Projects.Remove) != 0 {
		s := set.NewStringSet()
		s.AddValues(e.Projects.Default)
		s.AddValues(e.Projects.Add)
		s.RemoveValues(e.Projects.Remove)
		e.Projects.Value = s.ToSlice()
	}
	p, _, err := e.Metadata.ProjectsTitlesToIDs(e.Projects.Value)
	return &p, err
}

// ProjectV2Ids 返回一对切片。
// 第一个是应添加该 item 的项目(project)。
// 第二个是应从中移除该 item 的项目。
func (e Editable) ProjectV2Ids() (*[]string, *[]string, error) {
	if !e.Projects.Edited {
		return nil, nil, nil
	}

	// 待添加项目的标题
	addTitles := set.NewStringSet()
	// 待移除项目的标题
	removeTitles := set.NewStringSet()

	if len(e.Projects.Add) != 0 || len(e.Projects.Remove) != 0 {
		// 项目通过 flag 选择。
		addTitles.AddValues(e.Projects.Add)
		removeTitles.AddValues(e.Projects.Remove)
	} else {
		// 项目通过交互选择。
		addTitles.AddValues(e.Projects.Value)
		addTitles.RemoveValues(e.Projects.Default)
		removeTitles.AddValues(e.Projects.Default)
		removeTitles.RemoveValues(e.Projects.Value)
	}

	var addIds []string
	var removeIds []string
	var err error

	if addTitles.Len() > 0 {
		_, addIds, err = e.Metadata.ProjectsTitlesToIDs(addTitles.ToSlice())
		if err != nil {
			return nil, nil, err
		}
	}

	if removeTitles.Len() > 0 {
		_, removeIds, err = e.Metadata.ProjectsTitlesToIDs(removeTitles.ToSlice())
		if err != nil {
			return nil, nil, err
		}
	}

	return &addIds, &removeIds, nil
}

func (e Editable) MilestoneId() (*string, error) {
	if !e.Milestone.Edited {
		return nil, nil
	}
	if e.Milestone.Value == noMilestone || e.Milestone.Value == "" {
		s := ""
		return &s, nil
	}
	m, err := e.Metadata.MilestoneToID(e.Milestone.Value)
	return &m, err
}

// Clone 创建一个适用于并行 goroutine 的 Editable 浅拷贝(多数字段浅复制)。
// 会被修改的字段将被复制。
func (e *Editable) Clone() Editable {
	return Editable{
		Title:              e.Title.clone(),
		Body:               e.Body.clone(),
		Base:               e.Base.clone(),
		Reviewers:          e.Reviewers.clone(),
		ReviewerSearchFunc: e.ReviewerSearchFunc,
		Assignees:          e.Assignees.clone(),
		AssigneeSearchFunc: e.AssigneeSearchFunc,
		Labels:             e.Labels.clone(),
		Projects:           e.Projects.clone(),
		Milestone:          e.Milestone.clone(),
		IssueType:          e.IssueType.clone(),
		IssueTypeNameToID:  e.IssueTypeNameToID,
		ApiActorsSupported: e.ApiActorsSupported,
		// 浅复制，因为不会被修改。
		Metadata: e.Metadata,
	}
}

func (es *EditableString) clone() EditableString {
	return EditableString{
		Value:      es.Value,
		Default:    es.Default,
		Edited:     es.Edited,
		Selectable: es.Selectable,
		// 浅复制，因为不会被修改。
		Options: es.Options,
	}
}

func (es *EditableSlice) clone() EditableSlice {
	cpy := EditableSlice{
		Edited:     es.Edited,
		Selectable: es.Selectable,
		// 浅复制，因为不会被修改。
		Options: es.Options,
		// 复制可变的字符串切片。
		Add:     make([]string, len(es.Add)),
		Remove:  make([]string, len(es.Remove)),
		Value:   make([]string, len(es.Value)),
		Default: make([]string, len(es.Default)),
	}
	copy(cpy.Add, es.Add)
	copy(cpy.Remove, es.Remove)
	copy(cpy.Value, es.Value)
	copy(cpy.Default, es.Default)
	return cpy
}

func (ea *EditableAssignees) clone() EditableAssignees {
	return EditableAssignees{
		EditableSlice: ea.EditableSlice.clone(),
		DefaultLogins: ea.DefaultLogins,
	}
}

func (er *EditableReviewers) clone() EditableReviewers {
	return EditableReviewers{
		EditableSlice: er.EditableSlice.clone(),
		DefaultLogins: er.DefaultLogins,
	}
}

func (ep *EditableProjects) clone() EditableProjects {
	return EditableProjects{
		EditableSlice: ep.EditableSlice.clone(),
		ProjectItems:  ep.ProjectItems,
	}
}

type EditPrompter interface {
	Select(string, string, []string) (int, error)
	Input(string, string) (string, error)
	MarkdownEditor(string, string, bool) (string, error)
	MultiSelect(string, []string, []string) ([]int, error)
	MultiSelectWithSearch(prompt, searchPrompt string, defaults []string, persistentOptions []string, searchFunc func(string) prompter.MultiSelectSearchResult) ([]string, error)
	Confirm(string, bool) (bool, error)
}

func EditFieldsSurvey(p EditPrompter, editable *Editable, editorCommand string) error {
	var err error
	if editable.Title.Edited {
		editable.Title.Value, err = p.Input("标题", editable.Title.Default)
		if err != nil {
			return err
		}
	}
	if editable.Body.Edited {
		editable.Body.Value, err = p.MarkdownEditor("正文", editable.Body.Default, false)
		if err != nil {
			return err
		}
	}
	if editable.Reviewers.Edited {
		if editable.ReviewerSearchFunc != nil {
			editable.Reviewers.Options = []string{}
			editable.Reviewers.Value, err = p.MultiSelectWithSearch(
				"审核者",
				"搜索审核者(reviewer)",
				editable.Reviewers.DefaultLogins,
				// 无持久选项 —— 团队包含在搜索结果中
				[]string{},
				editable.ReviewerSearchFunc)
			if err != nil {
				return err
			}
		} else {
			editable.Reviewers.Value, err = multiSelectSurvey(
				p, "审核者", editable.Reviewers.Default, editable.Reviewers.Options)
			if err != nil {
				return err
			}
		}
	}
	if editable.Assignees.Edited {
		if editable.AssigneeSearchFunc != nil {
			editable.Assignees.Options = []string{}
			editable.Assignees.Value, err = p.MultiSelectWithSearch(
				"被指派人",
				"搜索被指派人(assignee)",
				editable.Assignees.DefaultLogins,
				// 此处无需持久选项，因为团队不能成为被指派人。
				[]string{},
				editable.AssigneeSearchFunc)
			if err != nil {
				return err
			}
		} else {
			editable.Assignees.Value, err = multiSelectSurvey(
				p, "被指派人", editable.Assignees.Default, editable.Assignees.Options)
			if err != nil {
				return err
			}
		}
	}
	if editable.Labels.Edited {
		editable.Labels.Add, err = multiSelectSurvey(
			p, "标签", editable.Labels.Default, editable.Labels.Options)
		if err != nil {
			return err
		}
		for _, prev := range editable.Labels.Default {
			if !slices.Contains(editable.Labels.Add, prev) {
				editable.Labels.Remove = append(editable.Labels.Remove, prev)
			}
		}
	}
	if editable.Projects.Edited {
		editable.Projects.Value, err = multiSelectSurvey(
			p, "项目", editable.Projects.Default, editable.Projects.Options)
		if err != nil {
			return err
		}
	}
	if editable.Milestone.Edited {
		editable.Milestone.Value, err = milestoneSurvey(p, editable.Milestone.Default, editable.Milestone.Options)
		if err != nil {
			return err
		}
	}
	if editable.IssueType.Edited {
		if len(editable.IssueType.Options) > 0 {
			var selected int
			selected, err = p.Select("类型", editable.IssueType.Default, editable.IssueType.Options)
			if err != nil {
				return err
			}
			editable.IssueType.Value = editable.IssueType.Options[selected]
		}
	}
	confirm, err := p.Confirm("提交?", true)
	if err != nil {
		return err
	}
	if !confirm {
		return fmt.Errorf("正在丢弃...")
	}

	return nil
}

func FieldsToEditSurvey(p EditPrompter, editable *Editable) error {
	opts := []string{"标题", "正文"}
	if editable.Reviewers.Selectable {
		opts = append(opts, "审核者")
	}
	opts = append(opts, "被指派人", "标签")
	if editable.IssueType.Selectable {
		opts = append(opts, "类型")
	}
	opts = append(opts, "项目", "里程碑")
	results, err := multiSelectSurvey(p, "您想编辑哪些内容?", []string{}, opts)
	if err != nil {
		return err
	}

	if slices.Contains(results, "标题") {
		editable.Title.Edited = true
	}
	if slices.Contains(results, "正文") {
		editable.Body.Edited = true
	}
	if slices.Contains(results, "审核者") {
		editable.Reviewers.Edited = true
	}
	if slices.Contains(results, "被指派人") {
		editable.Assignees.Edited = true
	}
	if slices.Contains(results, "标签") {
		editable.Labels.Edited = true
	}
	if slices.Contains(results, "类型") {
		editable.IssueType.Edited = true
	}
	if slices.Contains(results, "项目") {
		editable.Projects.Edited = true
	}
	if slices.Contains(results, "里程碑") {
		editable.Milestone.Edited = true
	}

	return nil
}

func FetchOptions(client *api.Client, repo ghrepo.Interface, editable *Editable, projectV1Support gh.ProjectsV1Support) error {
	// 判断是否获取组织团队与审核者(reviewer)。
	// 交互式审核者编辑(Edited 为 true 但没有 Add/Remove 切片)仍然需要
	// 团队数据来渲染选择界面。非交互流程永远不需要获取团队，
	// 因为 REST API 直接接受团队 slug。
	// 若已有搜索函数，则无需获取团队/审核者，
	// 因为假定它们会在提示流程中动态获取。
	teamReviewers := false
	fetchReviewers := false
	if editable.Reviewers.Edited {
		// 这很可能是交互式流程，因为设置了 edited 但没有对 Add/Remove 切片的修改，
		// 所以需要加载团队与审核者。
		// 不过，若已有搜索函数，则跳过获取，因为它会动态完成。
		if len(editable.Reviewers.Add) == 0 && len(editable.Reviewers.Remove) == 0 && editable.ReviewerSearchFunc == nil {
			teamReviewers = true
			fetchReviewers = true
		}
		// 注意: 非交互流程(带 Add/Remove)不需要获取审核者/团队，
		// 因为 GHES 与 GitHub.com 所用的 API 都直接接受用户登录名(login)和团队 slug。
	}

	fetchAssignees := false
	if editable.Assignees.Edited {
		// 与上面类似，若未设置 Add/Remove 切片，这很可能是交互式流程。
		// 若已有搜索函数，则无需获取被指派人(assignee)，
		// 因为假定它们会在提示流程中动态获取。
		if len(editable.Assignees.Add) == 0 && len(editable.Assignees.Remove) == 0 && editable.AssigneeSearchFunc == nil {
			fetchAssignees = true
		}
		// 对于非交互式的 Add/Remove 操作，只有在需要 ID 解析的 GHES 上
		// 才需要获取被指派人。在 github.com (ApiActorsSupported)上，
		// logins 会直接传递给 mutation。
		// TODO ApiActorsSupported
		if (len(editable.Assignees.Add) > 0 || len(editable.Assignees.Remove) > 0) && !editable.ApiActorsSupported {
			fetchAssignees = true
		}
	}

	input := api.RepoMetadataInput{
		Reviewers:          fetchReviewers,
		TeamReviewers:      teamReviewers,
		Assignees:          fetchAssignees,
		ApiActorsSupported: editable.ApiActorsSupported,
		Labels:             editable.Labels.Edited,
		ProjectsV1:         editable.Projects.Edited && projectV1Support == gh.ProjectsV1Supported,
		ProjectsV2:         editable.Projects.Edited,
		Milestones:         editable.Milestone.Edited,
	}
	metadata, err := api.RepoMetadata(client, repo, input)
	if err != nil {
		return err
	}

	var users []string
	for _, u := range metadata.AssignableUsers {
		users = append(users, u.Login())
	}
	var actors []string
	for _, a := range metadata.AssignableActors {
		actors = append(actors, a.DisplayName())
	}
	var teams []string
	for _, t := range metadata.Teams {
		teams = append(teams, fmt.Sprintf("%s/%s", repo.RepoOwner(), t.Slug))
	}
	var labels []string
	for _, l := range metadata.Labels {
		labels = append(labels, l.Name)
	}
	var projects []string
	for _, p := range metadata.Projects {
		projects = append(projects, p.Name)
	}
	for _, p := range metadata.ProjectsV2 {
		projects = append(projects, p.Title)
	}
	milestones := []string{noMilestone}
	for _, m := range metadata.Milestones {
		milestones = append(milestones, m.Title)
	}

	editable.Metadata = *metadata
	editable.Reviewers.Options = append(users, teams...)
	// TODO ApiActorsSupported
	if editable.ApiActorsSupported {
		editable.Assignees.Options = actors
	} else {
		editable.Assignees.Options = users
	}
	editable.Labels.Options = labels
	editable.Projects.Options = projects
	editable.Milestone.Options = milestones

	// 若编辑类型，则获取 issue 类型
	if editable.IssueType.Edited {
		issueTypes, err := api.RepoIssueTypes(client, repo)
		if err == nil {
			typeNames := make([]string, len(issueTypes))
			ids := make(map[string]string, len(issueTypes))
			for i, t := range issueTypes {
				typeNames[i] = t.Name
				ids[t.Name] = t.ID
			}
			editable.IssueType.Options = typeNames
			editable.IssueTypeNameToID = ids
		}
	}

	return nil
}

func multiSelectSurvey(p EditPrompter, message string, defaults, options []string) (results []string, err error) {
	if len(options) == 0 {
		return nil, nil
	}

	var selected []int
	selected, err = p.MultiSelect(message, defaults, options)
	if err != nil {
		return
	}

	for _, i := range selected {
		results = append(results, options[i])
	}

	return results, err
}

func milestoneSurvey(p EditPrompter, title string, opts []string) (result string, err error) {
	if len(opts) == 0 {
		return "", nil
	}
	var selected int
	selected, err = p.Select("里程碑", title, opts)
	if err != nil {
		return
	}

	result = opts[selected]
	return
}

// AssigneeSearchFunc 返回一个用于 MultiSelectWithSearch 的搜索函数，
// 它为给定的可指派(Issue/PR)节点 ID 动态获取可指派的 actor。
func AssigneeSearchFunc(apiClient *api.Client, repo ghrepo.Interface, assignableID string) func(string) prompter.MultiSelectSearchResult {
	return func(input string) prompter.MultiSelectSearchResult {
		actors, count, err := api.SuggestedAssignableActors(apiClient, repo, assignableID, input)
		if err != nil {
			return prompter.MultiSelectSearchResult{Err: err}
		}
		return actorsToSearchResult(actors, count)
	}
}

// RepoAssigneeSearchFunc 返回一个用于 MultiSelectWithSearch 的搜索函数，
// 它在仓库级别动态获取可指派的 actor。用于尚无 issue/PR 节点 ID 的创建流程。
func RepoAssigneeSearchFunc(apiClient *api.Client, repo ghrepo.Interface) func(string) prompter.MultiSelectSearchResult {
	return func(input string) prompter.MultiSelectSearchResult {
		actors, count, err := api.SearchRepoAssignableActors(apiClient, repo, input)
		if err != nil {
			return prompter.MultiSelectSearchResult{Err: err}
		}
		return actorsToSearchResult(actors, count)
	}
}

func actorsToSearchResult(actors []api.AssignableActor, totalCount int) prompter.MultiSelectSearchResult {
	logins := make([]string, 0, len(actors))
	displayNames := make([]string, 0, len(actors))

	for _, a := range actors {
		if a.Login() == "" {
			continue
		}
		logins = append(logins, a.Login())
		if a.DisplayName() != "" {
			displayNames = append(displayNames, a.DisplayName())
		} else {
			displayNames = append(displayNames, a.Login())
		}
	}
	return prompter.MultiSelectSearchResult{
		Keys:        logins,
		Labels:      displayNames,
		MoreResults: totalCount,
	}
}
