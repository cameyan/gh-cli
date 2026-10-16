package list

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
)

type AutolinkLister struct {
	HTTPClient *http.Client
}

func (a *AutolinkLister) List(repo ghrepo.Interface) ([]shared.Autolink, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "autolinks")
	if err != nil {
		return nil, err
	}

	var autolinks []shared.Autolink
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err = api.NewClientFromHTTP(a.HTTPClient).REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &autolinks)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("获取自动链接时出错: HTTP 404: 可能缺少仓库的管理员权限？(%s)", httpErr.RequestURL)
		}
		return nil, err
	}

	return autolinks, nil
}
