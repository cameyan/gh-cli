package delete

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

func deleteRepo(httpClient *http.Client, repo ghrepo.Interface) error {
	p, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName())
	if err != nil {
		return err
	}

	// 若仓库已被更名(而我们仍在使用旧名称), API 将返回 HTTP 307(临时重定向),
	// 这是一种保留 HTTP 方法的重定向。也就是说, 收到 307 后, HTTP 客户端会
	// 针对新位置重复相同的 HTTP 方法(此处为 DELETE)。我们不希望这种隐式行为,
	// 因此传入 `api.WithoutFollowingRedirects` 以确保返回错误。


	resp, err := api.NewClientFromHTTP(httpClient).Request(repo.RepoHost(), http.MethodDelete, p.String(), nil,
		api.WithEndpointScopes("delete_repo"),
		api.WithoutFollowingRedirects(),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
