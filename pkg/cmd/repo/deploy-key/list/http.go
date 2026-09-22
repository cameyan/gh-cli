package list

import (
	"net/http"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

type deployKey struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	ReadOnly  bool      `json:"read_only"`
}

func repoKeys(httpClient *http.Client, repo ghrepo.Interface) ([]deployKey, error) {
	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "keys")
	if err != nil {
		return nil, err
	}
	u.SetQuery("per_page", "100")

	var keys []deployKey
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err = api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), "GET", u.String(), nil, &keys)
	if err != nil {
		return nil, err
	}

	return keys, nil
}
