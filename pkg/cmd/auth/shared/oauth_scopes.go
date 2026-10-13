package shared

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

type MissingScopesError struct {
	MissingScopes []string
}

func (e MissingScopesError) Error() string {
	var missing []string
	for _, s := range e.MissingScopes {
		missing = append(missing, fmt.Sprintf("'%s'", s))
	}
	scopes := strings.Join(missing, ", ")

	if len(e.MissingScopes) == 1 {
		return "缺少所需的作用域(scope) " + scopes
	}
	return "缺少所需的作用域(scopes) " + scopes
}

// GetScopes 执行一次 GitHub API 请求并返回 X-Oauth-Scopes 头的值。
//
// 显式传入令牌是因为此函数在令牌存入配置之前运行，
// 因此 transport 没有可附加的内容。transport 仅在 Authorization
// 缺失时才设置它，因此此处设置的头会生效。
func GetScopes(httpClient *http.Client, hostname, authToken string) (string, error) {
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	res, err := api.NewClientFromHTTP(httpClient).Request(hostname, http.MethodGet, safeurl.NewImmutableSafeURL("").String(), nil,
		api.WithHeader("Authorization", "token "+authToken))
	if err != nil {
		return "", err
	}

	defer func() {
		// 在重新连接之前确保响应体已被完整读取并关闭，
		// 以便我们复用同一个 TCP 连接。
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}()

	if res.StatusCode != 200 {
		return "", api.UnexpectedStatusError(res)
	}

	return res.Header.Get("X-Oauth-Scopes"), nil
}

// HasMinimumScopes 执行一次 GitHub API 请求，若请求中使用的令牌缺少
// 使用 gh 执行 API 操作所需的最低作用域(scopes)，则返回错误。
func HasMinimumScopes(httpClient *http.Client, hostname, authToken string) error {
	scopesHeader, err := GetScopes(httpClient, hostname, authToken)
	if err != nil {
		return err
	}

	return HeaderHasMinimumScopes(scopesHeader)
}

// HeaderHasMinimumScopes 解析以逗号分隔的 scopesHeader 字符串，若它缺少
// 使用 gh 执行 API 操作所需的最低作用域(scopes)，则返回错误。
func HeaderHasMinimumScopes(scopesHeader string) error {
	if scopesHeader == "" {
		// 如果令牌报告没有任何作用域，则假定它是集成令牌，
		// 并放弃检测其能力
		return nil
	}

	search := map[string]bool{
		"repo":      false,
		"read:org":  false,
		"admin:org": false,
	}
	for s := range strings.SplitSeq(scopesHeader, ",") {
		search[strings.TrimSpace(s)] = true
	}

	var missingScopes []string
	if !search["repo"] {
		missingScopes = append(missingScopes, "repo")
	}

	if !search["read:org"] && !search["write:org"] && !search["admin:org"] {
		missingScopes = append(missingScopes, "read:org")
	}

	if len(missingScopes) > 0 {
		return &MissingScopesError{MissingScopes: missingScopes}
	}
	return nil
}
