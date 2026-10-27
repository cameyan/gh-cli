package add

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

func uploadDeployKey(httpClient *http.Client, repo ghrepo.Interface, keyFile io.Reader, title string, isWritable bool) error {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "keys")
	if err != nil {
		return err
	}

	keyBytes, err := io.ReadAll(keyFile)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"title":     title,
		"key":       string(keyBytes),
		"read_only": !isWritable,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), "POST", path.String(), bytes.NewBuffer(payloadBytes), nil)
}
