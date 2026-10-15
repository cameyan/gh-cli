package api

import (
	"net/http"
	"testing"

	cliAPI "github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test/data"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/require"
)

const (
	testRepo   = "github/example"
	testOwner  = "github"
	testDigest = "sha256:12313213"
)

func NewClientWithMockGHClient(hasNextPage bool) Client {
	fetcher := mockDataGenerator{
		NumUserAttestations:   5,
		NumGitHubAttestations: 4,
	}
	l := io.NewTestHandler()

	httpClient := &mockHttpClient{}

	if hasNextPage {
		return &LiveClient{
			githubAPI: mockAPIClient{
				OnRESTWithNext: fetcher.OnRESTSuccessWithNextPage,
			},
			externalHttpClient: httpClient,
			logger:             l,
		}
	}

	return &LiveClient{
		githubAPI: mockAPIClient{
			OnRESTWithNext: fetcher.OnRESTSuccess,
		},
		externalHttpClient: httpClient,
		logger:             l,
	}
}

var testFetchParamsWithOwner = FetchParams{
	Digest:        testDigest,
	Limit:         DefaultLimit,
	Owner:         testOwner,
	PredicateType: "https://slsa.dev/provenance/v1",
	Initiator:     "user",
}
var testFetchParamsWithRepo = FetchParams{
	Digest:        testDigest,
	Limit:         DefaultLimit,
	Repo:          testRepo,
	PredicateType: "https://slsa.dev/provenance/v1",
	Initiator:     "user",
}

var testFetchParamsWithRepoWithGitHubInitiator = FetchParams{
	Digest:    testDigest,
	Limit:     DefaultLimit,
	Repo:      testRepo,
	Initiator: "github",
}

type getByTestCase struct {
	name                 string
	params               FetchParams
	limit                int
	expectedAttestations int
	hasNextPage          bool
}

var getByTestCases = []getByTestCase{
	{
		name:                 "get by digest with owner",
		params:               testFetchParamsWithOwner,
		expectedAttestations: 5,
	},
	{
		name:                 "get by digest with repo",
		params:               testFetchParamsWithRepo,
		expectedAttestations: 5,
	},
	{
		name:                 "get by digest with attestations greater than limit",
		params:               testFetchParamsWithRepo,
		limit:                3,
		expectedAttestations: 3,
	},
	{
		name:                 "get by digest with next page",
		params:               testFetchParamsWithRepo,
		expectedAttestations: 10,
		hasNextPage:          true,
	},
	{
		name:                 "greater than limit with next page",
		params:               testFetchParamsWithRepo,
		limit:                7,
		expectedAttestations: 7,
		hasNextPage:          true,
	},
	{
		name:                 "get by digest with repo and GitHub initiator",
		params:               testFetchParamsWithRepoWithGitHubInitiator,
		expectedAttestations: 4,
	},
}

func TestGetByDigest(t *testing.T) {
	for _, tc := range getByTestCases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClientWithMockGHClient(tc.hasNextPage)

			if tc.limit > 0 {
				tc.params.Limit = tc.limit
			}
			attestations, err := c.GetByDigest(tc.params)
			require.NoError(t, err)

			require.Equal(t, tc.expectedAttestations, len(attestations))
			bundle := (attestations)[0].Bundle
			require.Equal(t, bundle.GetMediaType(), "application/vnd.dev.sigstore.bundle.v0.3+json")
		})
	}
}

func TestGetByDigest_NoAttestationsFound(t *testing.T) {
	fetcher := mockDataGenerator{
		NumUserAttestations: 5,
	}

	httpClient := &mockHttpClient{}
	c := LiveClient{
		githubAPI: mockAPIClient{
			OnRESTWithNext: fetcher.OnRESTWithNextNoAttestations,
		},
		externalHttpClient: httpClient,
		logger:             io.NewTestHandler(),
	}

	attestations, err := c.GetByDigest(testFetchParamsWithRepo)
	require.Error(t, err)
	require.IsType(t, ErrNoAttestationsFound, err)
	require.Nil(t, attestations)
}

func TestGetByDigest_Error(t *testing.T) {
	fetcher := mockDataGenerator{
		NumUserAttestations: 5,
	}

	c := LiveClient{
		githubAPI: mockAPIClient{
			OnRESTWithNext: fetcher.OnRESTWithNextError,
		},
		logger: io.NewTestHandler(),
	}

	attestations, err := c.GetByDigest(testFetchParamsWithRepo)
	require.Error(t, err)
	require.Nil(t, attestations)
}

func TestFetchBundleFromAttestations_BundleURL(t *testing.T) {
	httpClient := &mockHttpClient{}
	client := LiveClient{
		externalHttpClient: httpClient,
		logger:             io.NewTestHandler(),
	}

	att1 := makeTestAttestation()
	att2 := makeTestAttestation()
	attestations := []*Attestation{&att1, &att2}
	fetched, err := client.fetchBundleFromAttestations(attestations)
	require.NoError(t, err)
	require.Len(t, fetched, 2)
	require.NotNil(t, "application/vnd.dev.sigstore.bundle.v0.3+json", fetched[0].Bundle.GetMediaType())
	httpClient.AssertNumberOfCalls(t, "OnGetSuccess", 2)
}

func TestFetchBundleFromAttestations_MissingBundleAndBundleURLFields(t *testing.T) {
	httpClient := &mockHttpClient{}
	client := LiveClient{
		externalHttpClient: httpClient,
		logger:             io.NewTestHandler(),
	}

	// 如果 BundleURL 和 Bundle 字段均为空，函数应返回指示该情况的错误
	att1 := Attestation{}
	attestations := []*Attestation{&att1}
	bundles, err := client.fetchBundleFromAttestations(attestations)
	require.ErrorContains(t, err, "证明既没有 bundle 也没有 bundle URL")
	require.Nil(t, bundles, 2)
}

func TestFetchBundleFromAttestations_FailOnTheSecondAttestation(t *testing.T) {
	mockHTTPClient := &failAfterNCallsHttpClient{
		// 第一次 HTTP 请求会成功，为第一个证明(attestation)返回一个 bundle
		// 之后的所有 HTTP 请求都会失败，这意味着函数无法为第二个证明(attestation)获取 bundle，
		// 函数将返回错误
		FailOnCallN:              2,
		FailOnAllSubsequentCalls: true,
	}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	att1 := makeTestAttestation()
	att2 := makeTestAttestation()
	attestations := []*Attestation{&att1, &att2}
	bundles, err := c.fetchBundleFromAttestations(attestations)
	require.Error(t, err)
	require.Nil(t, bundles)
}

func TestFetchBundleFromAttestations_FailAfterRetrying(t *testing.T) {
	mockHTTPClient := &reqFailHttpClient{}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	a := makeTestAttestation()
	attestations := []*Attestation{&a}
	bundle, err := c.fetchBundleFromAttestations(attestations)
	require.Error(t, err)
	require.Nil(t, bundle)
	mockHTTPClient.AssertNumberOfCalls(t, "OnGetReqFail", 4)
}

func TestFetchBundleFromAttestations_FallbackToBundleField(t *testing.T) {
	mockHTTPClient := &mockHttpClient{}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	// 如果 bundle URL 为空，代码将回退到 bundle 字段
	a := Attestation{Bundle: data.SigstoreBundle(t)}
	attestations := []*Attestation{&a}
	fetched, err := c.fetchBundleFromAttestations(attestations)
	require.NoError(t, err)
	require.Equal(t, "application/vnd.dev.sigstore.bundle.v0.3+json", fetched[0].Bundle.GetMediaType())
	mockHTTPClient.AssertNotCalled(t, "OnGetSuccess")
}

// getBundle 在第一次 HTTP 请求尝试时就成功获取 bundle
func TestGetBundle(t *testing.T) {
	mockHTTPClient := &mockHttpClient{}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	b, err := c.getBundle(safeurl.NewImmutableSafeURL("https://mybundleurl.com"))
	require.NoError(t, err)
	require.Equal(t, "application/vnd.dev.sigstore.bundle.v0.3+json", b.GetMediaType())
	mockHTTPClient.AssertNumberOfCalls(t, "OnGetSuccess", 1)
}

// 当第一次 HTTP 请求返回 5XX 状态码时，getBundle 重试并成功获取
func TestGetBundle_SuccessfulRetry(t *testing.T) {
	mockHTTPClient := &failAfterNCallsHttpClient{
		FailOnCallN:              1,
		FailOnAllSubsequentCalls: false,
	}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	b, err := c.getBundle(safeurl.NewImmutableSafeURL("mybundleurl"))
	require.NoError(t, err)
	require.Equal(t, "application/vnd.dev.sigstore.bundle.v0.3+json", b.GetMediaType())
	mockHTTPClient.AssertNumberOfCalls(t, "OnGetFailAfterNCalls", 2)
}

// 当函数因永久性退避(backoff)错误条件而失败时，getBundle 不会重试
func TestGetBundle_PermanentBackoffFail(t *testing.T) {
	mockHTTPClient := &invalidBundleClient{}
	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	b, err := c.getBundle(safeurl.NewImmutableSafeURL("mybundleurl"))
	// var permanent *backoff.PermanentError
	//require.IsType(t, &backoff.PermanentError{}, err)
	require.Error(t, err)
	require.Nil(t, b)
	mockHTTPClient.AssertNumberOfCalls(t, "OnGetInvalidBundle", 1)
}

// 当 HTTP 请求失败时，getBundle 会重试
func TestGetBundle_RequestFail(t *testing.T) {
	mockHTTPClient := &reqFailHttpClient{}

	c := &LiveClient{
		externalHttpClient: mockHTTPClient,
		logger:             io.NewTestHandler(),
	}

	b, err := c.getBundle(safeurl.NewImmutableSafeURL("mybundleurl"))
	require.Error(t, err)
	require.Nil(t, b)
	mockHTTPClient.AssertNumberOfCalls(t, "OnGetReqFail", 4)
}

func TestGetTrustDomain(t *testing.T) {
	fetcher := mockMetaGenerator{
		TrustDomain: "foo",
	}

	t.Run("返回了信任域(trust domain)的情况", func(t *testing.T) {
		c := LiveClient{
			githubAPI: mockAPIClient{
				OnREST: fetcher.OnREST,
			},
			logger: io.NewTestHandler(),
		}
		td, err := c.GetTrustDomain()
		require.Nil(t, err)
		require.Equal(t, "foo", td)

	})

	t.Run("出错的情况", func(t *testing.T) {
		c := LiveClient{
			githubAPI: mockAPIClient{
				OnREST: fetcher.OnRESTError,
			},
			logger: io.NewTestHandler(),
		}
		td, err := c.GetTrustDomain()
		require.Equal(t, "", td)
		require.ErrorContains(t, err, "测试错误")
	})

}

func TestGetAttestationsRetries(t *testing.T) {
	getAttestationRetryInterval = 0

	fetcher := mockDataGenerator{
		NumUserAttestations: 5,
	}

	c := &LiveClient{
		githubAPI: mockAPIClient{
			OnRESTWithNext: fetcher.FlakyOnRESTSuccessWithNextPageHandler(),
		},
		externalHttpClient: &mockHttpClient{},
		logger:             io.NewTestHandler(),
	}

	testFetchParamsWithRepo.Limit = 30
	attestations, err := c.GetByDigest(testFetchParamsWithRepo)
	require.NoError(t, err)

	// 断言错误路径已被执行；由于这是一个分页请求，应该出错了两次
	fetcher.AssertNumberOfCalls(t, "FlakyOnRESTSuccessWithNextPage:error", 2)

	// 但我们仍然成功获取了正确的数据
	require.Equal(t, len(attestations), 10)
	bundle := (attestations)[0].Bundle
	require.Equal(t, bundle.GetMediaType(), "application/vnd.dev.sigstore.bundle.v0.3+json")
}

func TestGetAttestationsRetriesRESTWithNextError(t *testing.T) {
	originalRetryInterval := getAttestationRetryInterval
	getAttestationRetryInterval = 0
	t.Cleanup(func() {
		getAttestationRetryInterval = originalRetryInterval
	})

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.MatchAny,
		httpmock.StatusStringResponse(http.StatusInternalServerError, `{"message":"Internal Server Error"}`),
	)
	reg.Register(
		httpmock.MatchAny,
		httpmock.JSONResponse(map[string]any{
			"attestations": []any{
				map[string]any{"bundle_url": "https://example.com/bundle"},
			},
		}),
	)

	c := &LiveClient{
		githubAPI: cliAPI.NewClientFromHTTP(&http.Client{Transport: reg}),
		host:      "github.com",
		logger:    io.NewTestHandler(),
	}
	attestations, err := c.getAttestations(FetchParams{
		Digest: testDigest,
		Limit:  1,
		Repo:   testRepo,
	})

	require.NoError(t, err)
	require.Len(t, attestations, 1)
	require.Len(t, reg.Requests, 2)
}

// 测试总重试次数
func TestGetAttestationsMaxRetries(t *testing.T) {
	getAttestationRetryInterval = 0

	fetcher := mockDataGenerator{
		NumUserAttestations: 5,
	}

	c := &LiveClient{
		githubAPI: mockAPIClient{
			OnRESTWithNext: fetcher.OnREST500ErrorHandler(),
		},
		logger: io.NewTestHandler(),
	}

	_, err := c.GetByDigest(testFetchParamsWithRepo)
	require.Error(t, err)

	fetcher.AssertNumberOfCalls(t, "OnREST500Error", 4)
}
