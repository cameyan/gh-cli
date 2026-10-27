package list

import (
	"net/http"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/shurcooL/githubv4"
)

var releaseFields = []string{
	"name",
	"tagName",
	"isDraft",
	"isLatest",
	"isPrerelease",
	"isImmutable",
	"createdAt",
	"publishedAt",
}

type Release struct {
	Name         string
	TagName      string
	IsDraft      bool
	IsImmutable  bool `graphql:"immutable"`
	IsLatest     bool
	IsPrerelease bool
	CreatedAt    time.Time
	PublishedAt  time.Time
}

func (r *Release) ExportData(fields []string) map[string]any {
	return cmdutil.StructExportData(r, fields)
}

func fetchReleases(httpClient *http.Client, repo ghrepo.Interface, limit int, excludeDrafts bool, excludePreReleases bool, order string, releaseFeatures fd.ReleaseFeatures) ([]Release, error) {
	// TODO: immutableReleaseFullSupport
	// 这是一个临时解决方案，直到所有受支持的 GHES 版本都完全支持
	// 不可变发布(immutable releases)，大概要等 GHES 3.18 EOL 之后。
	// 届时可以移除这个 if 语句。
	//
	// 注 1: 本可以用两种独立的查询类型，甚至直接用纯文本/字符串查询来实现。
	// 但这两种方式以后都需要我们重构回之前的单一、强类型查询方式。
	// 因此，暂时复制整个函数似乎是较小代价的做法，
	// 未来可以在近期以更快、风险更小的方式完成清理。
	//
	// 注 2: 这里无法使用 `@include(condition)` 或 `@skip(condition)` 这类 GraphQL 指令，
	// 因为只要字段在 schema 中不存在，无论条件是否满足，整个查询都会失败。
	// TODO immutableReleaseFullSupport
	if !releaseFeatures.ImmutableReleases {
		return fetchReleasesWithoutImmutableReleases(httpClient, repo, limit, excludeDrafts, excludePreReleases, order)
	}

	type responseData struct {
		Repository struct {
			Releases struct {
				Nodes    []Release
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
			} `graphql:"releases(first: $perPage, orderBy: {field: CREATED_AT, direction: $direction}, after: $endCursor)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	perPage := min(limit, 100)

	variables := map[string]any{
		"owner":     githubv4.String(repo.RepoOwner()),
		"name":      githubv4.String(repo.RepoName()),
		"perPage":   githubv4.Int(perPage),
		"endCursor": (*githubv4.String)(nil),
		"direction": githubv4.OrderDirection(strings.ToUpper(order)),
	}

	gql := api.NewClientFromHTTP(httpClient)

	var releases []Release
loop:
	for {
		var query responseData
		err := gql.Query(repo.RepoHost(), "RepositoryReleaseList", &query, variables)
		if err != nil {
			return nil, err
		}

		for _, r := range query.Repository.Releases.Nodes {
			if excludeDrafts && r.IsDraft {
				continue
			}
			if excludePreReleases && r.IsPrerelease {
				continue
			}
			releases = append(releases, r)
			if len(releases) == limit {
				break loop
			}
		}

		if !query.Repository.Releases.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = githubv4.String(query.Repository.Releases.PageInfo.EndCursor)
	}

	return releases, nil
}

// TODO: immutableReleaseFullSupport
// 这是一个临时解决方案，直到所有受支持的 GHES 版本都完全支持
// 不可变发布(immutable releases)，大概要等 GHES 3.18 EOL 之后。
// 届时可以移除此函数。
func fetchReleasesWithoutImmutableReleases(httpClient *http.Client, repo ghrepo.Interface, limit int, excludeDrafts bool, excludePreReleases bool, order string) ([]Release, error) {
	type releaseOld struct {
		Name         string
		TagName      string
		IsDraft      bool
		IsLatest     bool
		IsPrerelease bool
		CreatedAt    time.Time
		PublishedAt  time.Time
	}

	fromReleaseOld := func(old releaseOld) Release {
		return Release{
			Name:         old.Name,
			TagName:      old.TagName,
			IsDraft:      old.IsDraft,
			IsLatest:     old.IsLatest,
			IsPrerelease: old.IsPrerelease,
			CreatedAt:    old.CreatedAt,
			PublishedAt:  old.PublishedAt,
		}
	}

	type responseData struct {
		Repository struct {
			Releases struct {
				Nodes    []releaseOld
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
			} `graphql:"releases(first: $perPage, orderBy: {field: CREATED_AT, direction: $direction}, after: $endCursor)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	perPage := min(limit, 100)

	variables := map[string]any{
		"owner":     githubv4.String(repo.RepoOwner()),
		"name":      githubv4.String(repo.RepoName()),
		"perPage":   githubv4.Int(perPage),
		"endCursor": (*githubv4.String)(nil),
		"direction": githubv4.OrderDirection(strings.ToUpper(order)),
	}

	gql := api.NewClientFromHTTP(httpClient)

	var releases []Release
loop:
	for {
		var query responseData
		err := gql.Query(repo.RepoHost(), "RepositoryReleaseList", &query, variables)
		if err != nil {
			return nil, err
		}

		for _, r := range query.Repository.Releases.Nodes {
			if excludeDrafts && r.IsDraft {
				continue
			}
			if excludePreReleases && r.IsPrerelease {
				continue
			}
			releases = append(releases, fromReleaseOld(r))
			if len(releases) == limit {
				break loop
			}
		}

		if !query.Repository.Releases.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = githubv4.String(query.Repository.Releases.PageInfo.EndCursor)
	}

	return releases, nil
}
