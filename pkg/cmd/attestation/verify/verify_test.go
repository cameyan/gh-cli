package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"

	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
)

const (
	SigstoreSanValue = "https://github.com/sigstore/sigstore-js/.github/workflows/release.yml@refs/heads/main"
	SigstoreSanRegex = "^https://github.com/sigstore/sigstore-js/"
)

var (
	artifactPath = test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz")
	bundlePath   = test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json")
)

func TestNewVerifyCmd(t *testing.T) {
	testIO, _, _, _ := iostreams.Test()
	var testReg httpmock.Registry
	var metaResp = api.MetaResponse{
		Domains: api.Domain{
			ArtifactAttestations: api.ArtifactAttestations{
				TrustDomain: "foo",
			},
		},
	}
	testReg.Register(httpmock.REST(http.MethodGet, "meta"),
		httpmock.StatusJSONResponse(200, &metaResp))

	f := &cmdutil.Factory{
		IOStreams: testIO,
		HttpClient: func() (*http.Client, error) {
			reg := &testReg
			client := &http.Client{}
			httpmock.ReplaceTripper(client, reg)
			return client, nil
		},
		ExternalHttpClient: func() (*http.Client, error) {
			return nil, nil
		},
	}

	testcases := []struct {
		name          string
		cli           string
		wants         Options
		wantsErr      bool
		wantsExporter bool
	}{
		{
			name: "无效的 digest-alg 标志",
			cli:  fmt.Sprintf("%s --bundle %s --digest-alg sha384 --owner sigstore", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				BundlePath:       test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json"),
				DigestAlgorithm:  "sha384",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "使用默认 digest-alg 值",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				BundlePath:       test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json"),
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: false,
		},
		{
			name: "自定义主机",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore --hostname foo.ghe.com", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				BundlePath:       test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json"),
				DigestAlgorithm:  "sha256",
				Hostname:         "foo.ghe.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: false,
		},
		{
			name: "无效的自定义主机",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore --hostname foo.bar.com", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				BundlePath:       test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json"),
				DigestAlgorithm:  "sha256",
				Hostname:         "foo.ghe.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "使用自定义 digest-alg 值",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore --digest-alg sha512", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				BundlePath:       test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0-bundle.json"),
				DigestAlgorithm:  "sha512",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: false,
		},
		{
			name: "缺少 owner 和 repo 标志",
			cli:  artifactPath,
			wants: Options{
				ArtifactPath:     test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz"),
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SANRegex:         "(?i)^https://github.com/sigstore/",
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "同时提供 owner 和 repo 标志",
			cli:  fmt.Sprintf("%s --owner sigstore --repo sigstore/sigstore-js", artifactPath),
			wants: Options{
				ArtifactPath:     artifactPath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				Repo:             "sigstore/sigstore-js",
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "使用默认 limit 标志",
			cli:  fmt.Sprintf("%s --owner sigstore", artifactPath),
			wants: Options{
				ArtifactPath:     artifactPath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: false,
		},
		{
			name: "使用自定义 limit 标志",
			cli:  fmt.Sprintf("%s --owner sigstore --limit 101", artifactPath),
			wants: Options{
				ArtifactPath:     artifactPath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            101,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: false,
		},
		{
			name: "使用无效的 limit 标志",
			cli:  fmt.Sprintf("%s --owner sigstore --limit 0", artifactPath),
			wants: Options{
				ArtifactPath:     artifactPath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            0,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SANRegex:         "(?i)^https://github.com/sigstore/",
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "同时提供 cert-identity 和 cert-identity-regex 标志",
			cli:  fmt.Sprintf("%s --owner sigstore --cert-identity https://github.com/sigstore/ --cert-identity-regex ^https://github.com/sigstore/", artifactPath),
			wants: Options{
				ArtifactPath:     artifactPath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SAN:              "https://github.com/sigstore/",
				SANRegex:         "(?i)^https://github.com/sigstore/",
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsErr: true,
		},
		{
			name: "以 JSON 格式打印输出",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore --format json", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     artifactPath,
				BundlePath:       bundlePath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    verification.SLSAPredicateV1,
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsExporter: true,
		},
		{
			name: "使用指定的谓词类型",
			cli:  fmt.Sprintf("%s --bundle %s --owner sigstore --predicate-type https://spdx.dev/Document/v2.3 --format json", artifactPath, bundlePath),
			wants: Options{
				ArtifactPath:     artifactPath,
				BundlePath:       bundlePath,
				DigestAlgorithm:  "sha256",
				Hostname:         "github.com",
				Limit:            30,
				OIDCIssuer:       verification.GitHubOIDCIssuer,
				Owner:            "sigstore",
				PredicateType:    "https://spdx.dev/Document/v2.3",
				SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
			},
			wantsExporter: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			var opts *Options
			cmd := NewVerifyCmd(f, func(o *Options) error {
				opts = o
				return nil
			})

			argv := strings.Split(tc.cli, " ")
			cmd.SetArgs(argv)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			_, err := cmd.ExecuteC()
			if tc.wantsErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			assert.Equal(t, tc.wants.ArtifactPath, opts.ArtifactPath)
			assert.Equal(t, tc.wants.BundlePath, opts.BundlePath)
			assert.Equal(t, tc.wants.DenySelfHostedRunner, opts.DenySelfHostedRunner)
			assert.Equal(t, tc.wants.DigestAlgorithm, opts.DigestAlgorithm)
			assert.Equal(t, tc.wants.Hostname, opts.Hostname)
			assert.Equal(t, tc.wants.Limit, opts.Limit)
			assert.Equal(t, tc.wants.NoPublicGood, opts.NoPublicGood)
			assert.Equal(t, tc.wants.OIDCIssuer, opts.OIDCIssuer)
			assert.Equal(t, tc.wants.Owner, opts.Owner)
			assert.Equal(t, tc.wants.PredicateType, opts.PredicateType)
			assert.Equal(t, tc.wants.Repo, opts.Repo)
			assert.Equal(t, tc.wants.SAN, opts.SAN)
			assert.Equal(t, tc.wants.SANRegex, opts.SANRegex)
			assert.Equal(t, tc.wants.TrustedRoot, opts.TrustedRoot)
			assert.NotNil(t, opts.APIClient)
			assert.NotNil(t, opts.Logger)
			assert.NotNil(t, opts.OCIClient)
			assert.Equal(t, tc.wantsExporter, opts.exporter != nil)
		})
	}
}

func TestVerifyCmdAuthChecks(t *testing.T) {
	f := &cmdutil.Factory{}

	t.Run("默认情况下需要身份认证检查", func(t *testing.T) {
		cmd := NewVerifyCmd(f, func(o *Options) error {
			return nil
		})

		// IsAuthCheckEnabled 假定被测命令是子命令
		parent := &cobra.Command{Use: "root"}
		parent.AddCommand(cmd)

		require.NoError(t, cmd.ParseFlags([]string{}))
		require.True(t, cmdutil.IsAuthCheckEnabled(cmd), "期望需要身份认证检查")
	})

	t.Run("提供 --bundle 标志时不需要身份认证检查", func(t *testing.T) {
		cmd := NewVerifyCmd(f, func(o *Options) error {
			return nil
		})

		// IsAuthCheckEnabled 假定被测命令是子命令
		parent := &cobra.Command{Use: "root"}
		parent.AddCommand(cmd)

		require.NoError(t, cmd.ParseFlags([]string{"--bundle", "not-important"}))
		require.False(t, cmdutil.IsAuthCheckEnabled(cmd), "由于提供了 --bundle 标志，期望不需要身份认证检查")
	})
}

func TestJSONOutput(t *testing.T) {
	testIO, _, out, _ := iostreams.Test()
	opts := Options{
		ArtifactPath:     artifactPath,
		BundlePath:       bundlePath,
		DigestAlgorithm:  "sha512",
		APIClient:        api.NewTestClient(),
		Logger:           io.NewHandler(testIO),
		OCIClient:        oci.MockClient{},
		OIDCIssuer:       verification.GitHubOIDCIssuer,
		Owner:            "sigstore",
		PredicateType:    verification.SLSAPredicateV1,
		SANRegex:         "^https://github.com/sigstore/",
		SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
		exporter:         cmdutil.NewJSONExporter(),
	}
	require.NoError(t, runVerify(&opts))

	var target []*verification.AttestationProcessingResult
	err := json.Unmarshal(out.Bytes(), &target)
	require.NoError(t, err)
}

func TestRunVerify(t *testing.T) {
	logger := io.NewTestHandler()

	publicGoodOpts := Options{
		ArtifactPath:     artifactPath,
		BundlePath:       bundlePath,
		DigestAlgorithm:  "sha512",
		APIClient:        api.NewTestClient(),
		Logger:           logger,
		OCIClient:        oci.MockClient{},
		OIDCIssuer:       verification.GitHubOIDCIssuer,
		Owner:            "sigstore",
		PredicateType:    verification.SLSAPredicateV1,
		SANRegex:         "^https://github.com/sigstore/",
		SigstoreVerifier: verification.NewMockSigstoreVerifier(t),
	}

	t.Run("制品和 bundle 有效的情况", func(t *testing.T) {
		require.NoError(t, runVerify(&publicGoodOpts))
	})

	t.Run("OCI 制品获取失败的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.ArtifactPath = "oci://ghcr.io/github/test"
		opts.OCIClient = oci.ReferenceFailClient{}

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "解析引用失败")
	})

	t.Run("缺少制品路径的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.ArtifactPath = "../test/data/non-existent-artifact.zip"
		require.Error(t, runVerify(&opts))
	})

	t.Run("缺少 bundle 路径的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = "../test/data/non-existent-sigstoreBundle.json"
		require.Error(t, runVerify(&opts))
	})

	t.Run("提供 owner 的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Owner = "sigstore"

		require.NoError(t, runVerify(&opts))
	})

	t.Run("owner 与 SourceRepositoryOwnerURI 不匹配的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Owner = "owner"

		err := runVerify(&opts)
		require.ErrorContains(t, err, "期望 SourceRepositoryOwnerURI 为 https://github.com/owner，实际为 https://github.com/sigstore")
	})

	t.Run("提供 repo 的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Repo = "sigstore/sigstore-js"

		require.Nil(t, runVerify(&opts))
	})

	// 使用错误的租户(tenancy)进行测试
	t.Run("租户无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Repo = "sigstore/sigstore-js"
		opts.Tenant = "foo"

		err := runVerify(&opts)
		require.ErrorContains(t, err, "期望 SourceRepositoryOwnerURI 为 https://foo.ghe.com/sigstore，实际为 https://github.com/sigstore")
	})

	t.Run("repo 与 SourceRepositoryURI 不匹配的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Repo = "sigstore/wrong"

		err := runVerify(&opts)
		require.ErrorContains(t, err, "期望 SourceRepositoryURI 为 https://github.com/sigstore/wrong，实际为 https://github.com/sigstore/sigstore-js")
	})

	t.Run("repo 无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.Repo = "wrong/example"
		opts.APIClient = api.NewFailTestClient()

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "从 wrong/example 获取证明失败")
	})

	t.Run("owner 无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.BundlePath = ""
		opts.APIClient = api.NewFailTestClient()
		opts.Owner = "wrong-owner"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "从 wrong-owner 获取证明失败")
	})

	t.Run("缺少 API 客户端的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.APIClient = nil
		customOpts.BundlePath = ""
		require.Error(t, runVerify(&customOpts))
	})

	t.Run("OCI 制品有效的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.ArtifactPath = "oci://ghcr.io/github/test"
		customOpts.BundlePath = ""

		require.Nil(t, runVerify(&customOpts))
	})

	t.Run("OCI 制品有效且提供 UseBundleFromRegistry 标志的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.ArtifactPath = "oci://ghcr.io/github/test"
		customOpts.BundlePath = ""
		customOpts.UseBundleFromRegistry = true

		require.Nil(t, runVerify(&customOpts))
	})

	t.Run("OCI 制品有效、提供 UseBundleFromRegistry 标志且谓词类型未知的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.ArtifactPath = "oci://ghcr.io/github/test"
		customOpts.BundlePath = ""
		customOpts.UseBundleFromRegistry = true
		customOpts.PredicateType = "https://predicate.type"

		err := runVerify(&customOpts)
		require.Error(t, err)
		require.ErrorContains(t, err, "未找到具有谓词类型的证明")
	})

	t.Run("OCI 制品有效、提供 UseBundleFromRegistry 标志但仓库未返回 bundle 的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.ArtifactPath = "oci://ghcr.io/github/test"
		customOpts.BundlePath = ""
		customOpts.UseBundleFromRegistry = true
		customOpts.OCIClient = oci.NoAttestationsClient{}

		require.ErrorContains(t, runVerify(&customOpts), "在 OCI 镜像仓库中未找到证明。请在不带 --bundle-from-oci 标志的情况下重试该命令，以检查 GitHub 上的证明")
	})

	t.Run("OCI 制品有效、提供 UseBundleFromRegistry 标志但从仓库获取 bundle 失败的情况", func(t *testing.T) {
		customOpts := publicGoodOpts
		customOpts.ArtifactPath = "oci://ghcr.io/github/test"
		customOpts.BundlePath = ""
		customOpts.UseBundleFromRegistry = true
		customOpts.OCIClient = oci.NoAttestationsClient{}

		require.ErrorContains(t, runVerify(&customOpts), "在 OCI 镜像仓库中未找到证明。请在不带 --bundle-from-oci 标志的情况下重试该命令，以检查 GitHub 上的证明")
	})
}
