package shared

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
)

type Action int

const (
	SubmitAction Action = iota
	PreviewAction
	CancelAction
	MetadataAction
	EditCommitMessageAction
	EditCommitSubjectAction
	SubmitDraftAction

	noMilestone = "(无)"

	submitLabel      = "提交"
	submitDraftLabel = "以草稿(draft)提交"
	previewLabel     = "在浏览器中继续"
	metadataLabel    = "添加元数据"
	cancelLabel      = "取消"
)

type Prompt interface {
	Input(prompt string, defaultValue string) (string, error)
	Select(prompt string, defaultValue string, options []string) (int, error)
	MarkdownEditor(prompt string, defaultValue string, blankAllowed bool) (string, error)
	Confirm(prompt string, defaultValue bool) (bool, error)
	MultiSelect(prompt string, defaults []string, options []string) ([]int, error)
	MultiSelectWithSearch(prompt, searchPrompt string, defaults []string, persistentOptions []string, searchFunc func(string) prompter.MultiSelectSearchResult) ([]string, error)
}

func ConfirmIssueSubmission(p Prompt, allowPreview bool, allowMetadata bool) (Action, error) {
	return confirmSubmission(p, allowPreview, allowMetadata, false, false)
}

func ConfirmPRSubmission(p Prompt, allowPreview, allowMetadata, isDraft bool) (Action, error) {
	return confirmSubmission(p, allowPreview, allowMetadata, true, isDraft)
}

func confirmSubmission(p Prompt, allowPreview, allowMetadata, allowDraft, isDraft bool) (Action, error) {
	var options []string
	if !isDraft {
		options = append(options, submitLabel)
	}
	if allowDraft {
		options = append(options, submitDraftLabel)
	}
	if allowPreview {
		options = append(options, previewLabel)
	}
	if allowMetadata {
		options = append(options, metadataLabel)
	}
	options = append(options, cancelLabel)

	result, err := p.Select("下一步做什么?", "", options)
	if err != nil {
		return -1, fmt.Errorf("无法显示提示: %w", err)
	}

	switch options[result] {
	case submitLabel:
		return SubmitAction, nil
	case submitDraftLabel:
		return SubmitDraftAction, nil
	case previewLabel:
		return PreviewAction, nil
	case metadataLabel:
		return MetadataAction, nil
	case cancelLabel:
		return CancelAction, nil
	default:
		return -1, fmt.Errorf("无效的索引: %d", result)
	}
}

func BodySurvey(p Prompt, state *IssueMetadataState, templateContent string) error {
	if templateContent != "" {
		if state.Body != "" {
			// 避免默认正文与模板之间出现过多换行
			state.Body = strings.TrimRight(state.Body, "\n")
			state.Body += "\n\n"
		}
		state.Body += templateContent
	}

	result, err := p.MarkdownEditor("正文", state.Body, true)
	if err != nil {
		return err
	}

	if state.Body != result {
		state.MarkDirty()
	}

	state.Body = result

	return nil
}

func TitleSurvey(p Prompt, io *iostreams.IOStreams, state *IssueMetadataState) error {
	var err error
	result := ""
	for result == "" {
		result, err = p.Input("标题(必填)", state.Title)
		if err != nil {
			return err
		}
		if result == "" {
			fmt.Fprintf(io.ErrOut, "%s 标题不能为空\n", io.ColorScheme().FailureIcon())
		}
	}

	if result != state.Title {
		state.MarkDirty()
	}

	state.Title = result

	return nil
}

type MetadataFetcher struct {
	IO        *iostreams.IOStreams
	APIClient *api.Client
	Repo      ghrepo.Interface
	State     *IssueMetadataState
}

func (mf *MetadataFetcher) RepoMetadataFetch(input api.RepoMetadataInput) (*api.RepoMetadataResult, error) {
	mf.IO.StartProgressIndicator()
	metadataResult, err := api.RepoMetadata(mf.APIClient, mf.Repo, input)
	mf.IO.StopProgressIndicator()
	mf.State.MetadataResult = metadataResult
	return metadataResult, err
}

type RepoMetadataFetcher interface {
	RepoMetadataFetch(api.RepoMetadataInput) (*api.RepoMetadataResult, error)
}

func MetadataSurvey(p Prompt, io *iostreams.IOStreams, baseRepo ghrepo.Interface, fetcher RepoMetadataFetcher, state *IssueMetadataState, projectsV1Support gh.ProjectsV1Support, reviewerSearchFunc func(string) prompter.MultiSelectSearchResult, assigneeSearchFunc func(string) prompter.MultiSelectSearchResult) error {
	isChosen := func(m string) bool {
		return slices.Contains(state.Metadata, m)
	}

	allowReviewers := state.Type == PRMetadata

	extraFieldsOptions := []string{}
	if allowReviewers {
		extraFieldsOptions = append(extraFieldsOptions, "审核者")
	}
	extraFieldsOptions = append(extraFieldsOptions, "被指派人", "标签", "项目", "里程碑")

	selected, err := p.MultiSelect("您想添加哪些内容?", nil, extraFieldsOptions)
	if err != nil {
		return err
	}
	for _, i := range selected {
		state.Metadata = append(state.Metadata, extraFieldsOptions[i])
	}

	// 根据选中的额外字段获取并处理数据，供调查(survey)提示使用。
	// 当可使用基于搜索的选择时，跳过代价高昂的可指派用户与团队获取，
	// 因为它们可通过搜索函数动态找到。
	// TODO ApiActorsSupported
	useReviewerSearch := state.ApiActorsSupported && reviewerSearchFunc != nil
	useAssigneeSearch := state.ApiActorsSupported && assigneeSearchFunc != nil
	metadataInput := api.RepoMetadataInput{
		Reviewers:          isChosen("审核者") && !useReviewerSearch,
		TeamReviewers:      isChosen("审核者") && !useReviewerSearch,
		Assignees:          isChosen("被指派人") && !useAssigneeSearch,
		ApiActorsSupported: state.ApiActorsSupported,
		Labels:             isChosen("标签"),
		ProjectsV1:         isChosen("项目") && projectsV1Support == gh.ProjectsV1Supported,
		ProjectsV2:         isChosen("项目"),
		Milestones:         isChosen("里程碑"),
	}
	metadataResult, err := fetcher.RepoMetadataFetch(metadataInput)
	if err != nil {
		return fmt.Errorf("获取元数据选项出错: %w", err)
	}

	var reviewers []string
	if !useReviewerSearch {
		for _, u := range metadataResult.AssignableUsers {
			if u.Login() != metadataResult.CurrentLogin {
				reviewers = append(reviewers, u.DisplayName())
			}
		}
		for _, t := range metadataResult.Teams {
			reviewers = append(reviewers, fmt.Sprintf("%s/%s", baseRepo.RepoOwner(), t.Slug))
		}
	}

	// Populate the list of selectable assignees and their default selections.
	// When search-based selection is available, skip building the static list.
	var assignees []string
	var assigneesDefault []string
	if !useAssigneeSearch {
		// TODO ApiActorsSupported
		if state.ApiActorsSupported {
			for _, u := range metadataResult.AssignableActors {
				assignees = append(assignees, u.DisplayName())

				if slices.Contains(state.Assignees, u.Login()) {
					assigneesDefault = append(assigneesDefault, u.DisplayName())
				}
			}
		} else {
			for _, u := range metadataResult.AssignableUsers {
				assignees = append(assignees, u.DisplayName())

				if slices.Contains(state.Assignees, u.Login()) {
					assigneesDefault = append(assigneesDefault, u.DisplayName())
				}
			}
		}
	}
	var labels []string
	for _, l := range metadataResult.Labels {
		labels = append(labels, l.Name)
	}
	var projects []string
	for _, p := range metadataResult.Projects {
		projects = append(projects, p.Name)
	}
	for _, p := range metadataResult.ProjectsV2 {
		projects = append(projects, p.Title)
	}
	milestones := []string{noMilestone}
	for _, m := range metadataResult.Milestones {
		milestones = append(milestones, m.Title)
	}

	// 根据选中的字段提示用户输入额外的元数据
	values := struct {
		Reviewers []string
		Assignees []string
		Labels    []string
		Projects  []string
		Milestone string
	}{}

	if isChosen("审核者") {
		if useReviewerSearch {
			selectedReviewers, err := p.MultiSelectWithSearch(
				"审核者",
				"搜索审核者(reviewer)",
				state.Reviewers,
				[]string{},
				reviewerSearchFunc)
			if err != nil {
				return err
			}
			values.Reviewers = selectedReviewers
		} else if len(reviewers) > 0 {
			// TODO ApiActorsSupported
			// 待 GHES 支持 requestReviewsByLogin 且始终使用基于搜索的选择后，
			// 可移除静态 MultiSelect 路径。
			selected, err := p.MultiSelect("审核者", state.Reviewers, reviewers)
			if err != nil {
				return err
			}
			for _, i := range selected {
				values.Reviewers = append(values.Reviewers, reviewers[i])
			}
		} else {
			fmt.Fprintln(io.ErrOut, "警告: 没有可用的审核者(reviewer)")
		}
	}
	if isChosen("被指派人") {
		if useAssigneeSearch {
			selectedAssignees, err := p.MultiSelectWithSearch(
				"被指派人",
				"搜索被指派人(assignee)",
				state.Assignees,
				[]string{},
				assigneeSearchFunc)
			if err != nil {
				return err
			}
			values.Assignees = selectedAssignees
		} else if len(assignees) > 0 {
			selected, err := p.MultiSelect("被指派人", assigneesDefault, assignees)
			if err != nil {
				return err
			}
			for _, i := range selected {
				// TODO ApiActorsSupported
				if state.ApiActorsSupported {
					values.Assignees = append(values.Assignees, metadataResult.AssignableActors[i].Login())
				} else {
					values.Assignees = append(values.Assignees, metadataResult.AssignableUsers[i].Login())
				}
			}
		} else {
			fmt.Fprintln(io.ErrOut, "警告: 没有可指派的用户")
		}
	}
	if isChosen("标签") {
		if len(labels) > 0 {
			selected, err := p.MultiSelect("标签", state.Labels, labels)
			if err != nil {
				return err
			}
			for _, i := range selected {
				values.Labels = append(values.Labels, labels[i])
			}
		} else {
			fmt.Fprintln(io.ErrOut, "警告: 仓库中没有任何标签(label)")
		}
	}
	if isChosen("项目") {
		if len(projects) > 0 {
			selected, err := p.MultiSelect("项目", state.ProjectTitles, projects)
			if err != nil {
				return err
			}
			for _, i := range selected {
				values.Projects = append(values.Projects, projects[i])
			}
		} else {
			fmt.Fprintln(io.ErrOut, "警告: 没有可选择的项目(project)")
		}
	}
	if isChosen("里程碑") {
		if len(milestones) > 1 {
			var milestoneDefault string
			if len(state.Milestones) > 0 {
				milestoneDefault = state.Milestones[0]
			} else {
				milestoneDefault = milestones[1]
			}
			selected, err := p.Select("里程碑", milestoneDefault, milestones)
			if err != nil {
				return err
			}
			values.Milestone = milestones[selected]
		} else {
			fmt.Fprintln(io.ErrOut, "警告: 仓库中没有任何里程碑(milestone)")
		}
	}

	// 更新 issue / 拉取请求(PR)的元数据状态
	if isChosen("审核者") {
		var logins []string
		for _, r := range values.Reviewers {
			// 从显示名中提取用户登录名(login)
			logins = append(logins, (strings.Split(r, " "))[0])
		}
		state.Reviewers = logins
	}
	if isChosen("被指派人") {
		state.Assignees = values.Assignees
	}
	if isChosen("标签") {
		state.Labels = values.Labels
	}
	if isChosen("项目") {
		state.ProjectTitles = values.Projects
	}
	if isChosen("里程碑") {
		if values.Milestone != "" && values.Milestone != noMilestone {
			state.Milestones = []string{values.Milestone}
		} else {
			state.Milestones = []string{}
		}
	}

	return nil
}

type Editor interface {
	Edit(filename, initialValue string) (string, error)
}

type UserEditor struct {
	IO     *iostreams.IOStreams
	Config func() (gh.Config, error)
}

func (e *UserEditor) Edit(filename, initialValue string) (string, error) {
	editorCommand, err := cmdutil.DetermineEditor(e.Config)
	if err != nil {
		return "", err
	}
	return surveyext.Edit(editorCommand, filename, initialValue, e.IO.In, e.IO.Out, e.IO.ErrOut)
}

const editorHintMarker = "------------------------ >8 ------------------------"
const editorHint = `
请在第一行输入标题，正文写在后续行中。
虚线以下的内容将被忽略，标题为空将中止创建过程。`

func TitledEditSurvey(editor Editor) func(string, string) (string, string, error) {
	return func(initialTitle, initialBody string) (string, string, error) {
		initialValue := strings.Join([]string{initialTitle, initialBody, editorHintMarker, editorHint}, "\n")
		titleAndBody, err := editor.Edit("*.md", initialValue)
		if err != nil {
			return "", "", err
		}

		titleAndBody = strings.ReplaceAll(titleAndBody, "\r\n", "\n")
		titleAndBody, _, _ = strings.Cut(titleAndBody, editorHintMarker)
		title, body, _ := strings.Cut(titleAndBody, "\n")
		return title, strings.TrimSuffix(body, "\n"), nil
	}
}

func InitEditorMode(f *cmdutil.Factory, editorMode bool, webMode bool, canPrompt bool) (bool, error) {
	if err := cmdutil.MutuallyExclusive(
		"只能指定 `--editor` 或 `--web` 之一",
		editorMode,
		webMode,
	); err != nil {
		return false, err
	}

	config, err := f.Config()
	if err != nil {
		return false, err
	}

	editorMode = !webMode && (editorMode || config.PreferEditorPrompt("").Value == "enabled")

	if editorMode && !canPrompt {
		return false, errors.New("--editor 或已启用的 prefer_editor_prompt 配置在非 tty 模式下不受支持")
	}

	return editorMode, nil
}
