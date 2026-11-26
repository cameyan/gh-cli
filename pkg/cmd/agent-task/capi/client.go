package capi

import (
	"context"
	"net/http"
	"net/url"

	"github.com/cli/cli/v2/internal/gh"
)

//go:generate moq -rm -out client_mock.go . CapiClient

// CapiClient 定义调用方所使用的方法。实现可以在单元测试中被测试替身替换。
type CapiClient interface {
	ListLatestSessionsForViewer(ctx context.Context, limit int) ([]*Session, error)
	CreateJob(ctx context.Context, owner, repo, problemStatement, baseBranch string, customAgent string) (*Job, error)
	GetJob(ctx context.Context, owner, repo, jobID string) (*Job, error)
	GetSession(ctx context.Context, id string) (*Session, error)
	GetSessionLogs(ctx context.Context, id string) ([]byte, error)
	ListSessionsByResourceID(ctx context.Context, resourceType string, resourceID int64, limit int) ([]*Session, error)
	GetPullRequestDatabaseID(ctx context.Context, hostname string, owner string, repo string, number int) (int64, string, error)
}

// CAPIClient 是用于与 Copilot API 交互的客户端
type CAPIClient struct {
	httpClient  *http.Client
	host        string
	capiBaseURL string
}

// authConfig resolves the active token for a GitHub host, refreshing an expiring token when it is near expiry.
// It is satisfied by gh's AuthConfig and lets the transport obtain a fresh token on each request.
type authConfig interface {
	ActiveTokenWithRefresh(hostname string) (gh.Credential, gh.RefreshStatus, error)
}

// NewCAPIClient creates a new CAPI client. Provide an auth source, the user's
// GitHub host, the resolved Copilot API URL, and an HTTP client which will be
// used as the base transport for CAPI requests.
//
// The provided HTTP client will be mutated for use with CAPI, so it should not
// be reused elsewhere.
func NewCAPIClient(httpClient *http.Client, authCfg authConfig, host string, capiBaseURL string) *CAPIClient {
	httpClient.Transport = newCAPITransport(authCfg, host, capiBaseURL, httpClient.Transport)
	return &CAPIClient{
		httpClient:  httpClient,
		host:        host,
		capiBaseURL: capiBaseURL,
	}
}

// capiTransport 添加 Copilot 认证头
type capiTransport struct {
	rp       http.RoundTripper
	authCfg  authConfig
	host     string
	capiHost string
}

func newCAPITransport(authCfg authConfig, host string, capiBaseURL string, rp http.RoundTripper) *capiTransport {
	capiHost := ""
	if u, err := url.Parse(capiBaseURL); err == nil {
		capiHost = u.Host
	}
	return &capiTransport{
		rp:       rp,
		authCfg:  authCfg,
		host:     host,
		capiHost: capiHost,
	}
}

func (ct *capiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Resolve the token per request so an expiring token is refreshed just before use. The base transport's own
	// refresh is skipped once we set the Authorization header, so the CAPI transport must refresh here itself.
	// Refresh is best effort: a failed refresh still yields the last stored token and the API decides its validity.
	cred, _, _ := ct.authCfg.ActiveTokenWithRefresh(ct.host)
	req.Header.Set("Authorization", "Bearer "+cred.Token)

	// 由于此 RoundTrip 同时用于 Copilot API 和 GitHub API 请求，
	// 我们仅在向 Copilot API 发起请求时才添加 integration ID。
	if req.URL.Host == ct.capiHost {
		req.Header.Add("Copilot-Integration-Id", "copilot-4-cli")

		// 确保在访问 CAPI 时不会使用 GitHub API 的版本。
		req.Header.Set("X-GitHub-Api-Version", "2026-01-09")
	}
	return ct.rp.RoundTrip(req)
}
