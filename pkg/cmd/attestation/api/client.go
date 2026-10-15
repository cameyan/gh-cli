package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	ioconfig "github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/klauspost/compress/snappy"
	v1 "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	DefaultLimit     = 30
	maxLimitForFlag  = 1000
	maxLimitForFetch = 100
)

// 允许在测试中注入退避(backoff)间隔。
var getAttestationRetryInterval = time.Millisecond * 200

// FetchParams 是从 GitHub API 获取证明(attestation)时使用的参数
type FetchParams struct {
	Digest        string
	Limit         int
	Owner         string
	PredicateType string
	Repo          string
	Initiator     string
}

func (p *FetchParams) Validate() error {
	if p.Digest == "" {
		return fmt.Errorf("必须提供摘要(digest)")
	}
	if p.Limit <= 0 || p.Limit > maxLimitForFlag {
		return fmt.Errorf("limit 必须大于 0 且小于等于 %d", maxLimitForFlag)
	}
	if p.Repo == "" && p.Owner == "" {
		return fmt.Errorf("必须提供 owner 或 repo")
	}
	return nil
}

// githubApiClient 负责向 GitHub API 发起 REST 调用
type githubApiClient interface {
	REST(hostname, method, p string, body io.Reader, data any) error
	RESTWithNext(hostname, method, p string, body io.Reader, data any) (string, error)
}

// httpClient 负责向所有非 GitHub API 端点发起 HTTP 调用
type httpClient interface {
	Get(url string) (*http.Response, error)
}

type Client interface {
	GetByDigest(params FetchParams) ([]*Attestation, error)
	GetTrustDomain() (string, error)
}

type LiveClient struct {
	githubAPI          githubApiClient
	externalHttpClient httpClient
	host               string
	logger             *ioconfig.Handler
}

func NewLiveClient(hc *http.Client, externalClient *http.Client, host string, l *ioconfig.Handler) *LiveClient {
	return &LiveClient{
		githubAPI:          api.NewClientFromHTTP(hc),
		host:               strings.TrimSuffix(host, "/"),
		externalHttpClient: externalClient,
		logger:             l,
	}
}

// GetByDigest 按摘要(digest)以及 owner 或 repo(取决于实际提供了哪一个)获取证明(attestation)
func (c *LiveClient) GetByDigest(params FetchParams) ([]*Attestation, error) {
	c.logger.VerbosePrintf("正在获取制品摘要(artifact digest) %s 对应的证明(attestation)\n\n", params.Digest)
	attestations, err := c.getAttestations(params)
	if err != nil {
		return nil, err
	}

	bundles, err := c.fetchBundleFromAttestations(attestations)
	if err != nil {
		return nil, fmt.Errorf("通过 URL 获取 bundle 失败: %w", err)
	}

	return bundles, nil
}

func (c *LiveClient) buildRequestURL(params FetchParams) (safeurl.SafeURL, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}

	var u *safeurl.MutableSafeURL
	if params.Repo != "" {
		// 先检查 Repo 是否已设置，因为如果 Repo 已设置，Owner 将使用 Repo 的值来设置。
		// 如果 Repo 未设置，该字段将保持为空，不会使用 Owner 的值来填充。
		owner, name, err := safeurl.RepoPartsFromNWO(params.Repo)
		if err != nil {
			return nil, err
		}
		u, err = safeurl.JoinPath("repos", owner, name, "attestations", params.Digest)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		u, err = safeurl.JoinPath("orgs", params.Owner, "attestations", params.Digest)
		if err != nil {
			return nil, err
		}
	}

	perPage := min(params.Limit, maxLimitForFetch)

	// ref: https://github.com/cli/go-gh/blob/d32c104a9a25c9de3d7c7b07a43ae0091441c858/example_gh_test.go#L96
	u.SetQuery("per_page", strconv.Itoa(perPage))
	if params.PredicateType != "" {
		u.SetQuery("predicate_type", params.PredicateType)
	}
	return u, nil
}

func (c *LiveClient) getAttestations(params FetchParams) ([]*Attestation, error) {
	u, err := c.buildRequestURL(params)
	if err != nil {
		return nil, err
	}

	var attestations []*Attestation
	var resp AttestationsResponse
	bo := backoff.NewConstantBackOff(getAttestationRetryInterval)

	var pageURL safeurl.SafeURL = u

	// 如果没有证明(attestation)或数量未达到 limit，则继续获取
	for pageURL.String() != "" && len(attestations) < params.Limit {
		err := backoff.Retry(func() error {
			newURL, restErr := c.githubAPI.RESTWithNext(c.host, http.MethodGet, pageURL.String(), nil, &resp)
			if restErr != nil {
				if shouldRetry(restErr) {
					return restErr
				}
				return backoff.Permanent(restErr)
			}

			pageURL = safeurl.NewImmutableSafeURL(newURL)

			// 按发起者(initiator)类型过滤
			if params.Initiator != "" {
				filtered := make([]*Attestation, 0, len(resp.Attestations))
				for _, att := range resp.Attestations {
					if att.Initiator == params.Initiator {
						filtered = append(filtered, att)
					}
				}
				resp.Attestations = filtered
			}
			attestations = append(attestations, resp.Attestations...)

			return nil
		}, backoff.WithMaxRetries(bo, 3))

		// 若 RESTWithNext 出错则直接退出
		if err != nil {
			return nil, err
		}
	}

	if len(attestations) == 0 {
		return nil, ErrNoAttestationsFound
	}

	if len(attestations) > params.Limit {
		return attestations[:params.Limit], nil
	}

	return attestations, nil
}

func (c *LiveClient) fetchBundleFromAttestations(attestations []*Attestation) ([]*Attestation, error) {
	fetched := make([]*Attestation, len(attestations))
	g := errgroup.Group{}
	for i, a := range attestations {
		g.Go(func() error {
			if a.Bundle == nil && a.BundleURL == "" {
				return fmt.Errorf("证明(attestation)既没有 bundle 也没有 bundle URL")
			}

			// 目前，如果 bundle URL 为空，则回退到 bundle 字段
			if a.BundleURL == "" {
				c.logger.VerbosePrintf("Bundle URL 为空，回退到 bundle 字段\n\n")
				fetched[i] = &Attestation{
					Bundle: a.Bundle,
				}
				return nil
			}

			// 否则通过提供的 URL 获取 bundle
			b, err := c.getBundle(safeurl.NewImmutableSafeURL(a.BundleURL))
			if err != nil {
				return fmt.Errorf("通过 URL 获取 bundle 失败: %w", err)
			}
			fetched[i] = &Attestation{
				Bundle: b,
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return fetched, nil
}

func (c *LiveClient) getBundle(url safeurl.SafeURL) (*bundle.Bundle, error) {
	c.logger.VerbosePrintf("正在通过 bundle URL 获取证明(attestation)的 bundle\n\n")

	var sgBundle *bundle.Bundle
	bo := backoff.NewConstantBackOff(getAttestationRetryInterval)
	err := backoff.Retry(func() error {
		resp, err := c.externalHttpClient.Get(url.String())
		if err != nil {
			return fmt.Errorf("从 URL 获取 bundle 的请求失败: %w", err)
		}

		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			return fmt.Errorf("URL 为 %s 的证明(attestation) bundle 返回了状态码 %d", url.String(), resp.StatusCode)
		}

		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("读取 blob 存储响应体失败: %w", err)
		}

		var out []byte
		decompressed, err := snappy.Decode(out, body)
		if err != nil {
			return backoff.Permanent(fmt.Errorf("使用 snappy 解压失败: %w", err))
		}

		var pbBundle v1.Bundle
		if err = protojson.Unmarshal(decompressed, &pbBundle); err != nil {
			return backoff.Permanent(fmt.Errorf("反序列化为 bundle 失败: %w", err))
		}

		c.logger.VerbosePrintf("成功获取 bundle\n\n")

		sgBundle, err = bundle.NewBundle(&pbBundle)
		if err != nil {
			return backoff.Permanent(fmt.Errorf("创建新 bundle 失败: %w", err))
		}

		return nil
	}, backoff.WithMaxRetries(bo, 3))

	return sgBundle, err
}

func shouldRetry(err error) bool {
	if httpError, ok := errors.AsType[api.HTTPError](err); ok {
		if httpError.StatusCode >= 500 && httpError.StatusCode <= 599 {
			return true
		}
	}

	return false
}

// GetTrustDomain 返回当前的信任域(trust domain)。如果使用的是默认值，
// 则返回空字符串
func (c *LiveClient) GetTrustDomain() (string, error) {
	u, err := safeurl.JoinPath(MetaPath)
	if err != nil {
		return "", err
	}
	return c.getTrustDomain(u)
}

func (c *LiveClient) getTrustDomain(u safeurl.SafeURL) (string, error) {
	var resp MetaResponse

	bo := backoff.NewConstantBackOff(getAttestationRetryInterval)
	err := backoff.Retry(func() error {
		restErr := c.githubAPI.REST(c.host, http.MethodGet, u.String(), nil, &resp)
		if restErr != nil {
			if shouldRetry(restErr) {
				return restErr
			} else {
				return backoff.Permanent(restErr)
			}
		}

		return nil
	}, backoff.WithMaxRetries(bo, 3))

	if err != nil {
		return "", err
	}

	return resp.Domains.ArtifactAttestations.TrustDomain, nil
}
