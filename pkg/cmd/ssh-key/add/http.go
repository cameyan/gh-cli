package add

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/ssh-key/shared"
)

// 上传提供的 SSH 密钥。若密钥已上传则返回 true，否则返回 false。
func SSHKeyUpload(httpClient *http.Client, hostname string, keyFile io.Reader, title string) (bool, error) {
	u, err := safeurl.JoinPath("user", "keys")
	if err != nil {
		return false, err
	}

	keyBytes, err := io.ReadAll(keyFile)
	if err != nil {
		return false, err
	}

	fullUserKey := string(keyBytes)
	splitKey := strings.Fields(fullUserKey)
	if len(splitKey) < 2 {
		return false, errors.New("提供的密钥格式无效")
	}

	keyToCompare := splitKey[0] + " " + splitKey[1]

	keys, err := shared.UserKeys(httpClient, hostname, "")
	if err != nil {
		return false, err
	}

	for _, k := range keys {
		if k.Key == keyToCompare {
			return false, nil
		}
	}

	payload := map[string]string{
		"title": title,
		"key":   fullUserKey,
	}

	err = keyUpload(httpClient, hostname, u, payload)

	if err != nil {
		return false, err
	}

	return true, nil
}

// 上传提供的 SSH 签名密钥。若密钥已上传则返回 true，否则返回 false。
func SSHSigningKeyUpload(httpClient *http.Client, hostname string, keyFile io.Reader, title string) (bool, error) {
	u, err := safeurl.JoinPath("user", "ssh_signing_keys")
	if err != nil {
		return false, err
	}

	keyBytes, err := io.ReadAll(keyFile)
	if err != nil {
		return false, err
	}

	fullUserKey := string(keyBytes)
	splitKey := strings.Fields(fullUserKey)
	if len(splitKey) < 2 {
		return false, errors.New("提供的密钥格式无效")
	}

	keyToCompare := splitKey[0] + " " + splitKey[1]

	keys, err := shared.UserSigningKeys(httpClient, hostname, "")
	if err != nil {
		return false, err
	}

	for _, k := range keys {
		if k.Key == keyToCompare {
			return false, nil
		}
	}

	payload := map[string]string{
		"title": title,
		"key":   fullUserKey,
	}

	err = keyUpload(httpClient, hostname, u, payload)

	if err != nil {
		return false, err
	}

	return true, nil
}

func keyUpload(httpClient *http.Client, hostname string, u safeurl.SafeURL, payload map[string]string) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用点，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(hostname, http.MethodPost, u.String(), bytes.NewBuffer(payloadBytes), nil)
}
