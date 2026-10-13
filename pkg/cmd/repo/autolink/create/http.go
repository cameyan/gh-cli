package create

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
)

type AutolinkCreator struct {
	HTTPClient *http.Client
}

type AutolinkCreateRequest struct {
	IsAlphanumeric bool   `json:"is_alphanumeric"`
	KeyPrefix      string `json:"key_prefix"`
	URLTemplate    string `json:"url_template"`
}

func (a *AutolinkCreator) Create(repo ghrepo.Interface, request AutolinkCreateRequest) (*shared.Autolink, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "autolinks")
	if err != nil {
		return nil, err
	}

	requestByte, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	requestBody := bytes.NewReader(requestByte)

	var autolink shared.Autolink
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	err = api.NewClientFromHTTP(a.HTTPClient).REST(repo.RepoHost(), http.MethodPost, path.String(), requestBody, &autolink)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			httpErr.Message = "必须拥有仓库的管理员(admin)权限。"
			return nil, httpErr
		}
		return nil, err
	}

	return &autolink, nil
}
