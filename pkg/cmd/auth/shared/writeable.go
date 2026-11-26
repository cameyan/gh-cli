package shared

import (
	"strings"

	"github.com/cli/cli/v2/internal/gh"
)

// AuthTokenRefreshable 报告令牌是否由 gh 存储并且可以通过
// `gh auth refresh` 更新。
//
// TODO: 它自行匹配令牌前缀。本可以改问
// gh.AuthConfig.ActiveTokenType。
func AuthTokenRefreshable(token, src string) bool {
	return token != "" && !strings.HasSuffix(src, "_TOKEN") && strings.HasPrefix(token, "gho_")
}

func AuthTokenWriteable(authCfg gh.AuthConfig, hostname string) (string, bool) {
	cred := authCfg.ActiveToken(hostname)
	return cred.Source, (cred.Token == "" || !strings.HasSuffix(cred.Source, "_TOKEN"))
}
