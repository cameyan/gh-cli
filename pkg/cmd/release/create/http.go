package create

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/shurcooL/githubv4"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

type tag struct {
	Name string `json:"name"`
}

type releaseNotes struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

var notImplementedError = errors.New("未实现")

type errMissingRequiredWorkflowScope struct {
	Hostname string
}

func (e errMissingRequiredWorkflowScope) Error() string {
	return "可能需要 workflow 权限范围(scope)"
}

func remoteTagExists(httpClient *http.Client, repo ghrepo.Interface, tagName string) (bool, error) {
	gql := api.NewClientFromHTTP(httpClient)
	qualifiedTagName := fmt.Sprintf("refs/tags/%s", tagName)
	var query struct {
		Repository struct {
			Ref struct {
				ID string
			} `graphql:"ref(qualifiedName: $tagName)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	variables := map[string]any{
		"owner":   githubv4.String(repo.RepoOwner()),
		"name":    githubv4.String(repo.RepoName()),
		"tagName": githubv4.String(qualifiedTagName),
	}
	err := gql.Query(repo.RepoHost(), "RepositoryFindRef", &query, variables)
	return query.Repository.Ref.ID != "", err
}

func getTags(httpClient *http.Client, repo ghrepo.Interface, limit int) ([]tag, error) {
	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "tags")
	if err != nil {
		return nil, err
	}
	u.SetQuery("per_page", strconv.Itoa(limit))

	var tags []tag
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	err = api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), http.MethodGet, u.String(), nil, &tags)
	return tags, err
}

func generateReleaseNotes(httpClient *http.Client, repo ghrepo.Interface, tagName, target, previousTagName string) (*releaseNotes, error) {
	params := map[string]any{
		"tag_name": tagName,
	}
	if target != "" {
		params["target_commitish"] = target
	}
	if previousTagName != "" {
		params["previous_tag_name"] = previousTagName
	}

	bodyBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", "generate-notes")
	if err != nil {
		return nil, err
	}

	var rn releaseNotes
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	err = api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), http.MethodPost, path.String(), bytes.NewBuffer(bodyBytes), &rn)
	if err != nil {
		if httpErr, ok := errors.AsType[api.HTTPError](err); ok && httpErr.StatusCode == http.StatusNotFound {
			return nil, notImplementedError
		}
		return nil, err
	}
	return &rn, nil
}

func publishedReleaseExists(httpClient *http.Client, repo ghrepo.Interface, tagName string) (bool, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", "tags", tagName)
	if err != nil {
		return false, err
	}

	// HEAD 响应没有响应体，因此使用 Request 而非 REST，后者会尝试解码响应体。
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).Request(repo.RepoHost(), http.MethodHead, path.String(), nil)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		return false, api.UnexpectedStatusError(resp)
	}

	return true, nil
}

func createRelease(httpClient *http.Client, repo ghrepo.Interface, params map[string]any) (*shared.Release, error) {
	bodyBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases")
	if err != nil {
		return nil, err
	}

	// 检查是否在未携带 workflow 权限范围(scope)创建发布(release)时收到了 404，
	// 若是，则向用户返回一条说明可能解决方案的错误信息。
	//
	// 如果同样的文件(路径和内容均相同)存在于仓库的另一个分支上，
	// 则无需 workflow scope 即可创建包含 workflow 文件变更的发布(release)。
	// 否则，创建该发布需要 workflow scope，但 API 除了返回 404 外并未说明该判定条件。
	//
	// https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps#available-scopes
	var newRelease shared.Release
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	err = api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), http.MethodPost, path.String(), bytes.NewBuffer(bodyBytes), &newRelease)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) &&
			httpErr.StatusCode == http.StatusNotFound &&
			!tokenHasWorkflowScope(httpErr.Headers) {
			normalizedHostname := ghauth.NormalizeHostname(httpErr.RequestURL.Hostname())
			return nil, &errMissingRequiredWorkflowScope{
				Hostname: normalizedHostname,
			}
		}
		return nil, err
	}
	return &newRelease, nil
}

func publishRelease(httpClient *http.Client, host string, releaseURL safeurl.SafeURL, discussionCategory string, isLatest *bool) (*shared.Release, error) {
	params := map[string]any{"draft": false}
	if discussionCategory != "" {
		params["discussion_category_name"] = discussionCategory
	}

	if isLatest != nil {
		params["make_latest"] = fmt.Sprintf("%v", *isLatest)
	}

	bodyBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	var release shared.Release
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	err = api.NewClientFromHTTP(httpClient).REST(host, http.MethodPatch, releaseURL.String(), bytes.NewBuffer(bodyBytes), &release)
	if err != nil {
		return nil, err
	}
	return &release, nil
}

func deleteRelease(httpClient *http.Client, host string, releaseURL safeurl.SafeURL) error {
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(host, http.MethodDelete, releaseURL.String(), nil, nil)
}

// tokenHasWorkflowScope 检查响应的 token 是否具有 workflow 权限范围(scope)。
// 没有 OAuth scopes 的 token 被假定为具有 workflow scope。
func tokenHasWorkflowScope(headers http.Header) bool {
	scopes := headers.Get("X-Oauth-Scopes")

	// 无 scopes 时返回 true - 该请求头中没有 scopes
	// 意味着用户可能在使用非 OAuth 类型的 token 进行认证，
	// 我们无法得知该 token 实际具有哪些 scopes。
	if scopes == "" {
		return true
	}

	// API 返回的 scopes 以逗号加空格分隔，因此比较前必须对每个元素做去空格处理。
	for s := range strings.SplitSeq(scopes, ",") {
		if strings.TrimSpace(s) == "workflow" {
			return true
		}
	}
	return false
}

// isNewRelease 检查自最新发布(release)以来是否有新提交。
func isNewRelease(httpClient *http.Client, repo ghrepo.Interface) (bool, error) {
	ctx := context.Background()
	release, err := shared.FetchLatestRelease(ctx, httpClient, repo)
	if err != nil {
		if errors.Is(err, shared.ErrReleaseNotFound) {
			return true, nil
		} else {
			return false, err
		}
	}

	tagName := release.TagName
	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "compare", tagName+"...HEAD")
	if err != nil {
		return false, err
	}
	u.SetQuery("per_page", "1")

	var comparisonStatus struct {
		Status string `json:"status"`
	}

	apiClient := api.NewClientFromHTTP(httpClient)
	if err := apiClient.REST(repo.RepoHost(), "GET", u.String(), nil, &comparisonStatus); err != nil {
		return false, err
	}

	isNew := comparisonStatus.Status == "ahead"
	return isNew, nil
}
