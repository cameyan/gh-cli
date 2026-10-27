package delete

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

type AutolinkDeleter struct {
	HTTPClient *http.Client
}

func (a *AutolinkDeleter) Delete(repo ghrepo.Interface, id string) error {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "autolinks", id)
	if err != nil {
		return err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err = api.NewClientFromHTTP(a.HTTPClient).REST(repo.RepoHost(), http.MethodDelete, path.String(), nil, nil)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("删除自动链接时出错: HTTP 404: 可能缺少仓库的管理员权限？(%s)", httpErr.RequestURL)
		}
		return err
	}

	return nil
}
