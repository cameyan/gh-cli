package view

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
)

type AutolinkViewer struct {
	HTTPClient *http.Client
}

func (a *AutolinkViewer) View(repo ghrepo.Interface, id string) (*shared.Autolink, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "autolinks", id)
	if err != nil {
		return nil, err
	}

	var autolink shared.Autolink
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err = api.NewClientFromHTTP(a.HTTPClient).REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &autolink)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("HTTP 404: 可能缺少仓库的管理员权限？(%s)", httpErr.RequestURL)
		}
		return nil, err
	}

	return &autolink, nil
}
