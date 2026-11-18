package shared

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/shurcooL/githubv4"
	"golang.org/x/sync/errgroup"
)

func UpdateIssue(httpClient *http.Client, repo ghrepo.Interface, id string, isPR bool, options Editable) error {
	var wg errgroup.Group

	// 标签通过离散的 mutation 更新，以避免替换整个标签列表并引发竞态条件。
	if options.Labels.Edited {
		if len(options.Labels.Add) > 0 {
			wg.Go(func() error {
				addedLabelIds, err := options.Metadata.LabelsToIDs(options.Labels.Add)
				if err != nil {
					return err
				}
				return addLabels(httpClient, id, repo, addedLabelIds)
			})
		}
		if len(options.Labels.Remove) > 0 {
			wg.Go(func() error {
				removeLabelIds, err := options.Metadata.LabelsToIDs(options.Labels.Remove)
				if err != nil {
					return err
				}
				return removeLabels(httpClient, id, repo, removeLabelIds)
			})
		}
	}

	// updateIssue mutation 不支持 ProjectsV2，因此需要在单独的请求中处理。
	if options.Projects.Edited {
		wg.Go(func() error {
			apiClient := api.NewClientFromHTTP(httpClient)
			addIds, removeIds, err := options.ProjectV2Ids()
			if err != nil {
				return err
			}
			if addIds == nil && removeIds == nil {
				return nil
			}
			toAdd := make(map[string]string, len(*addIds))
			toRemove := make(map[string]string, len(*removeIds))
			for _, p := range *addIds {
				toAdd[p] = id
			}
			for _, p := range *removeIds {
				toRemove[p] = options.Projects.ProjectItems[p]
			}
			return api.UpdateProjectV2Items(apiClient, repo, toAdd, toRemove)
		})
	}

	if dirtyExcludingLabels(options) {
		wg.Go(func() error {
			// updateIssue mutation 不支持 Actor，因此当被指派人(assignee)是 Actor 时，
			// 指派需要放在单独的请求中完成。
			// 注意: 这里有意与其他 issue 字段更新同步执行，
			// 以确保与遗留用户被指派人的处理方式保持一致。
			// https://github.com/cli/cli/pull/10960#discussion_r2086725348
			// TODO ApiActorsSupported
			if options.Assignees.Edited && options.ApiActorsSupported {
				apiClient := api.NewClientFromHTTP(httpClient)
				logins, err := options.AssigneeLogins(apiClient, repo)
				if err != nil {
					return err
				}

				err = api.ReplaceActorsForAssignableByLogin(apiClient, repo, id, logins)
				if err != nil {
					return err
				}
			}
			err := replaceIssueFields(httpClient, repo, id, isPR, options)
			if err != nil {
				return err
			}

			return nil
		})
	}

	return wg.Wait()
}

func replaceIssueFields(httpClient *http.Client, repo ghrepo.Interface, id string, isPR bool, options Editable) error {
	apiClient := api.NewClientFromHTTP(httpClient)

	projectIds, err := options.ProjectIds()
	if err != nil {
		return err
	}

	var assigneeIds *[]string
	// TODO ApiActorsSupported
	if !options.ApiActorsSupported {
		assigneeIds, err = options.AssigneeIds(apiClient, repo)
		if err != nil {
			return err
		}
	}

	milestoneId, err := options.MilestoneId()
	if err != nil {
		return err
	}

	if isPR {
		params := githubv4.UpdatePullRequestInput{
			PullRequestID: id,
			Title:         ghString(options.TitleValue()),
			Body:          ghString(options.BodyValue()),
			AssigneeIDs:   ghIds(assigneeIds),
			ProjectIDs:    ghIds(projectIds),
			MilestoneID:   ghId(milestoneId),
		}
		if options.Base.Edited {
			params.BaseRefName = ghString(&options.Base.Value)
		}
		return updatePullRequest(httpClient, repo, params)
	}

	params := githubv4.UpdateIssueInput{
		ID:          id,
		Title:       ghString(options.TitleValue()),
		Body:        ghString(options.BodyValue()),
		AssigneeIDs: ghIds(assigneeIds),
		ProjectIDs:  ghIds(projectIds),
		MilestoneID: ghId(milestoneId),
	}
	return updateIssue(httpClient, repo, params)
}

func dirtyExcludingLabels(e Editable) bool {
	return e.Title.Edited ||
		e.Body.Edited ||
		e.Base.Edited ||
		e.Reviewers.Edited ||
		e.Assignees.Edited ||
		e.Projects.Edited ||
		e.Milestone.Edited
}

func addLabels(httpClient *http.Client, id string, repo ghrepo.Interface, labels []string) error {
	params := githubv4.AddLabelsToLabelableInput{
		LabelableID: id,
		LabelIDs:    *ghIds(&labels),
	}

	var mutation struct {
		AddLabelsToLabelable struct {
			Typename string `graphql:"__typename"`
		} `graphql:"addLabelsToLabelable(input: $input)"`
	}

	variables := map[string]any{"input": params}
	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "LabelAdd", &mutation, variables)
}

func removeLabels(httpClient *http.Client, id string, repo ghrepo.Interface, labels []string) error {
	params := githubv4.RemoveLabelsFromLabelableInput{
		LabelableID: id,
		LabelIDs:    *ghIds(&labels),
	}

	var mutation struct {
		RemoveLabelsFromLabelable struct {
			Typename string `graphql:"__typename"`
		} `graphql:"removeLabelsFromLabelable(input: $input)"`
	}

	variables := map[string]any{"input": params}
	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "LabelRemove", &mutation, variables)
}

func updateIssue(httpClient *http.Client, repo ghrepo.Interface, params githubv4.UpdateIssueInput) error {
	var mutation struct {
		UpdateIssue struct {
			Typename string `graphql:"__typename"`
		} `graphql:"updateIssue(input: $input)"`
	}
	variables := map[string]any{"input": params}
	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "IssueUpdate", &mutation, variables)
}

func updatePullRequest(httpClient *http.Client, repo ghrepo.Interface, params githubv4.UpdatePullRequestInput) error {
	var mutation struct {
		UpdatePullRequest struct {
			Typename string `graphql:"__typename"`
		} `graphql:"updatePullRequest(input: $input)"`
	}
	variables := map[string]any{"input": params}
	gql := api.NewClientFromHTTP(httpClient)
	err := gql.Mutate(repo.RepoHost(), "PullRequestUpdate", &mutation, variables)
	return err
}

func ghIds(s *[]string) *[]githubv4.ID {
	if s == nil {
		return nil
	}
	ids := make([]githubv4.ID, len(*s))
	for i, v := range *s {
		ids[i] = v
	}
	return &ids
}

func ghId(s *string) *githubv4.ID {
	if s == nil {
		return nil
	}
	if *s == "" {
		r := githubv4.ID(nil)
		return &r
	}
	r := githubv4.ID(*s)
	return &r
}

func ghString(s *string) *githubv4.String {
	if s == nil {
		return nil
	}
	r := githubv4.String(*s)
	return &r
}
