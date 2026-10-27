package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
)

var ReleaseFields = []string{
	"apiUrl",
	"author",
	"assets",
	"body",
	"createdAt",
	"databaseId",
	"id",
	"isDraft",
	"isPrerelease",
	"isImmutable",
	"name",
	"publishedAt",
	"tagName",
	"tarballUrl",
	"targetCommitish",
	"uploadUrl",
	"url",
	"zipballUrl",
}

type Release struct {
	DatabaseID   int64      `json:"id"`
	ID           string     `json:"node_id"`
	TagName      string     `json:"tag_name"`
	Name         string     `json:"name"`
	Body         string     `json:"body"`
	IsDraft      bool       `json:"draft"`
	IsPrerelease bool       `json:"prerelease"`
	IsImmutable  bool       `json:"immutable"`
	CreatedAt    time.Time  `json:"created_at"`
	PublishedAt  *time.Time `json:"published_at"`

	TargetCommitish string `json:"target_commitish"`

	APIURL     string `json:"url"`
	UploadURL  string `json:"upload_url"`
	TarballURL string `json:"tarball_url"`
	ZipballURL string `json:"zipball_url"`
	URL        string `json:"html_url"`
	Assets     []ReleaseAsset

	Author struct {
		ID    string `json:"node_id"`
		Login string `json:"login"`
	}
}

type ReleaseAsset struct {
	ID     string `json:"node_id"`
	Name   string
	Label  string
	Size   int64
	Digest *string
	State  string
	APIURL string `json:"url"`

	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	DownloadCount      int       `json:"download_count"`
	ContentType        string    `json:"content_type"`
	BrowserDownloadURL string    `json:"browser_download_url"`
}

func (rel *Release) ExportData(fields []string) map[string]any {
	v := reflect.ValueOf(rel).Elem()
	fieldByName := func(v reflect.Value, field string) reflect.Value {
		return v.FieldByNameFunc(func(s string) bool {
			return strings.EqualFold(field, s)
		})
	}
	data := map[string]any{}

	for _, f := range fields {
		switch f {
		case "author":
			data[f] = map[string]any{
				"id":    rel.Author.ID,
				"login": rel.Author.Login,
			}
		case "assets":
			assets := make([]any, 0, len(rel.Assets))
			for _, a := range rel.Assets {
				assets = append(assets, map[string]any{
					"url":           a.BrowserDownloadURL,
					"apiUrl":        a.APIURL,
					"id":            a.ID,
					"name":          a.Name,
					"label":         a.Label,
					"size":          a.Size,
					"digest":        a.Digest,
					"state":         a.State,
					"createdAt":     a.CreatedAt,
					"updatedAt":     a.UpdatedAt,
					"downloadCount": a.DownloadCount,
					"contentType":   a.ContentType,
				})
			}
			data[f] = assets
		default:
			sf := fieldByName(v, f)
			data[f] = sf.Interface()
		}
	}

	return data
}

var ErrReleaseNotFound = errors.New("未找到发布(release)")

type fetchResult struct {
	release *Release
	error   error
}

func FetchRefSHA(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, tagName string) (string, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "git", "ref", fmt.Sprintf("tags/%s", tagName))
	if err != nil {
		return "", err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, repo.RepoHost(), http.MethodGet, path.String(), nil)
	if err != nil {
		if isNotFound(err) {
			return "", ErrReleaseNotFound
		}
		return "", err
	}
	defer resp.Body.Close()

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return "", fmt.Errorf("解析 ref 响应失败: %w", err)
	}

	return ref.Object.SHA, nil
}

// DigestAlgForRef 返回与给定 git ref SHA 对应的摘要(digest)算法名称。
// SHA-1 git 对象 ID 为 40 个十六进制字符，SHA-256 git 对象 ID 为 64 个
// 十六进制字符。未知长度默认返回 "sha1" 以保持向后兼容的行为。
func DigestAlgForRef(digest string) string {
	switch len(digest) {
	case 64:
		return "sha256"
	default:
		return "sha1"
	}
}

// FetchRelease 按标签(tag)名查找已发布的仓库发布(release)，或按待定标签名查找草稿(draft)发布。
func FetchRelease(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, tagName string) (*Release, error) {
	publishedPath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", "tags", tagName)
	if err != nil {
		return nil, err
	}

	cc, cancel := context.WithCancel(ctx)
	results := make(chan fetchResult, 2)

	// 已发布发布(release)的查找
	go func() {
		release, err := fetchReleasePath(cc, httpClient, repo.RepoHost(), publishedPath)
		results <- fetchResult{release: release, error: err}
	}()

	// 草稿(draft)发布的查找
	go func() {
		release, err := fetchDraftRelease(cc, httpClient, repo, tagName)
		results <- fetchResult{release: release, error: err}
	}()

	// 优先采用任一查找成功找到的发布(release)。单个查找失败(例如未认证时的草稿查找)
	// 不得掩盖另一个查找的结果；只有两者都失败时才报告错误。
	first := <-results
	if first.error == nil {
		cancel()
		<-results // 清空 channel
		return first.release, nil
	}

	second := <-results
	cancel() // 即使已无 goroutine 在运行，也调用 cancel 以满足 linter
	if second.error == nil {
		return second.release, nil
	}

	// 两次查找都失败；优先报告为未找到发布(release)。
	if errors.Is(second.error, ErrReleaseNotFound) {
		return nil, second.error
	}
	return nil, first.error
}

// FetchLatestRelease 查找仓库最新发布的发布(release)。
func FetchLatestRelease(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface) (*Release, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", "latest")
	if err != nil {
		return nil, err
	}
	return fetchReleasePath(ctx, httpClient, repo.RepoHost(), path)
}

// fetchDraftRelease 返回第一个以待定标签名 tagName 作为待定标签(tag)的草稿(draft)发布。
func fetchDraftRelease(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, tagName string) (*Release, error) {
	// 先使用 GraphQL 按待定标签名查找草稿(draft)发布，因为 REST 不具备此能力。
	var query struct {
		Repository struct {
			Release *struct {
				DatabaseID int64
				IsDraft    bool
			} `graphql:"release(tagName: $tagName)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"owner":   githubv4.String(repo.RepoOwner()),
		"name":    githubv4.String(repo.RepoName()),
		"tagName": githubv4.String(tagName),
	}

	gql := api.NewClientFromHTTP(httpClient)
	if err := gql.QueryWithContext(ctx, repo.RepoHost(), "RepositoryReleaseByTag", &query, variables); err != nil {
		return nil, err
	}

	if query.Repository.Release == nil || !query.Repository.Release.IsDraft {
		return nil, ErrReleaseNotFound
	}

	// 然后，使用 REST 获取草稿(draft)发布的信息。理论上可以通过 GraphQL 获取全部所需信息，
	// 但出于向后兼容的考虑，使用 REST 更安全。
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", strconv.FormatInt(query.Repository.Release.DatabaseID, 10))
	if err != nil {
		return nil, err
	}
	return fetchReleasePath(ctx, httpClient, repo.RepoHost(), path)
}

func fetchReleasePath(ctx context.Context, httpClient *http.Client, host string, path safeurl.SafeURL) (*Release, error) {
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, host, http.MethodGet, path.String(), nil)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrReleaseNotFound
		}
		return nil, err
	}
	defer resp.Body.Close()

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// isNotFound 判断 err 是否为携带 404 状态码的 API 错误。发布(release)查找将
// 发布缺失视为哨兵(sentinel)值而非错误，而 api client 通过 HTTPError 而非
// 调用点可检查的响应来上报状态。
func isNotFound(err error) bool {
	httpErr, ok := errors.AsType[api.HTTPError](err)
	return ok && httpErr.StatusCode == http.StatusNotFound
}

func StubFetchRelease(t *testing.T, reg *httpmock.Registry, owner, repoName, tagName, responseBody string) {
	path := "repos/OWNER/REPO/releases/tags/v1.2.3"
	if tagName == "" {
		path = "repos/OWNER/REPO/releases/latest"
	}

	reg.Register(httpmock.REST("GET", path), httpmock.StringResponse(responseBody))

	if tagName != "" {
		reg.Register(
			httpmock.GraphQL(`query RepositoryReleaseByTag\b`),
			httpmock.GraphQLQuery(`{ "data": { "repository": { "release": null }}}`,
				func(q string, vars map[string]any) {
					assert.Equal(t, owner, vars["owner"])
					assert.Equal(t, repoName, vars["name"])
					assert.Equal(t, tagName, vars["tagName"])
				}),
		)
	}
}

func StubFetchRefSHA(t *testing.T, reg *httpmock.Registry, owner, repoName, tagName, sha string) {
	path := fmt.Sprintf("repos/%s/%s/git/ref/tags%%2F%s", owner, repoName, tagName)
	reg.Register(
		httpmock.REST("GET", path),
		httpmock.StringResponse(fmt.Sprintf(`{"object": {"sha": "%s"}}`, sha)),
	)
}
