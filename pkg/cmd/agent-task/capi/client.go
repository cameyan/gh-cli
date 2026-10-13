package capi

import (
	"context"
	"net/http"
	"net/url"
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

// NewCAPIClient 创建一个新的 CAPI 客户端。需要提供 token、用户的 GitHub 主机、
// 解析后的 Copilot API URL，以及一个作为 CAPI 请求基础 transport 的 HTTP 客户端。
//
// 提供的 HTTP 客户端会被就地修改以用于 CAPI，因此不应在其他地方复用。
func NewCAPIClient(httpClient *http.Client, token string, host string, capiBaseURL string) *CAPIClient {
	httpClient.Transport = newCAPITransport(token, capiBaseURL, httpClient.Transport)
	return &CAPIClient{
		httpClient:  httpClient,
		host:        host,
		capiBaseURL: capiBaseURL,
	}
}

// capiTransport 添加 Copilot 认证头
type capiTransport struct {
	rp       http.RoundTripper
	token    string
	capiHost string
}

func newCAPITransport(token string, capiBaseURL string, rp http.RoundTripper) *capiTransport {
	capiHost := ""
	if u, err := url.Parse(capiBaseURL); err == nil {
		capiHost = u.Host
	}
	return &capiTransport{
		rp:       rp,
		token:    token,
		capiHost: capiHost,
	}
}

func (ct *capiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+ct.token)

	// 由于此 RoundTrip 同时用于 Copilot API 和 GitHub API 请求，
	// 我们仅在向 Copilot API 发起请求时才添加 integration ID。
	if req.URL.Host == ct.capiHost {
		req.Header.Add("Copilot-Integration-Id", "copilot-4-cli")

		// 确保在访问 CAPI 时不会使用 GitHub API 的版本。
		req.Header.Set("X-GitHub-Api-Version", "2026-01-09")
	}
	return ct.rp.RoundTrip(req)
}
