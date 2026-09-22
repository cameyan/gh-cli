package delete

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

func deleteDeployKey(httpClient *http.Client, repo ghrepo.Interface, id string) error {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "keys", id)
	if err != nil {
		return err
	}
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), "DELETE", path.String(), nil, nil)
}
