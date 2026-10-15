//go:build integration

package verify

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/telemetry"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	"github.com/cli/cli/v2/pkg/iostreams"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/stretchr/testify/require"
)

func TestVerifyIntegration(t *testing.T) {
	logger := io.NewTestHandler()

	sigstoreConfig := verification.SigstoreConfig{
		ExternalHttpClient: http.DefaultClient,
		Logger:             logger,
		TUFMetadataDir:     o.Some(t.TempDir()),
	}

	ios, _, _, _ := iostreams.Test()
	hc, err := factory.HttpClientFunc(
		func() (gh.Config, error) { return config.NewMockConfig(), nil },
		ios,
		"test",
		"",
		&telemetry.NoOpService{},
	)()
	require.NoError(t, err)

	host, _ := auth.DefaultHost()

	sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(sigstoreConfig)
	require.NoError(t, err)
	publicGoodOpts := Options{
		APIClient:        api.NewLiveClient(hc, http.DefaultClient, host, logger),
		ArtifactPath:     artifactPath,
		BundlePath:       bundlePath,
		DigestAlgorithm:  "sha512",
		Logger:           logger,
		OCIClient:        oci.NewLiveClient(),
		OIDCIssuer:       verification.GitHubOIDCIssuer,
		Owner:            "sigstore",
		PredicateType:    verification.SLSAPredicateV1,
		SANRegex:         "^https://github.com/sigstore/",
		SigstoreVerifier: sigstoreVerifier,
	}

	t.Run("owner 有效的情况", func(t *testing.T) {
		err := runVerify(&publicGoodOpts)
		require.NoError(t, err)
	})

	t.Run("repo 有效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.Repo = "sigstore/sigstore-js"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("owner 有效且 repo 无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.Repo = "sigstore/fakerepo"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "期望 SourceRepositoryURI 为 https://github.com/sigstore/fakerepo，实际为 https://github.com/sigstore/sigstore-js")
	})

	t.Run("owner 无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.Owner = "fakeowner"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "期望 SourceRepositoryOwnerURI 为 https://github.com/fakeowner，实际为 https://github.com/sigstore")
	})

	t.Run("没有匹配的 OIDC 签发者(issuer)的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.OIDCIssuer = "some-other-issuer"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "期望 Issuer 为 some-other-issuer，实际为 https://token.actions.githubusercontent.com")
	})

	t.Run("SAN 无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.SAN = "fake san"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "使用签发者(issuer) \"sigstore.dev\" 进行验证时出错")
	})

	t.Run("SAN 正则(regex)无效的情况", func(t *testing.T) {
		opts := publicGoodOpts
		opts.SANRegex = "^https://github.com/sigstore/not-real/"

		err := runVerify(&opts)
		require.Error(t, err)
		require.ErrorContains(t, err, "使用签发者(issuer) \"sigstore.dev\" 进行验证时出错")
	})

	t.Run("来自 OCI 镜像仓库(registry)的 bundle 的情况", func(t *testing.T) {
		sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(sigstoreConfig)
		require.NoError(t, err)
		opts := Options{
			APIClient:             api.NewLiveClient(hc, http.DefaultClient, host, logger),
			ArtifactPath:          "oci://ghcr.io/github/artifact-attestations-helm-charts/policy-controller:v0.10.0-github9",
			UseBundleFromRegistry: true,
			DigestAlgorithm:       "sha256",
			Logger:                logger,
			OCIClient:             oci.NewLiveClient(),
			OIDCIssuer:            verification.GitHubOIDCIssuer,
			Owner:                 "github",
			PredicateType:         verification.SLSAPredicateV1,
			SANRegex:              "^https://github.com/github/",
			SigstoreVerifier:      sigstoreVerifier,
		}

		err = runVerify(&opts)
		require.NoError(t, err)
	})
}

func TestVerifyIntegrationCustomIssuer(t *testing.T) {
	artifactPath := test.NormalizeRelativePath("../test/data/custom-issuer-artifact")
	bundlePath := test.NormalizeRelativePath("../test/data/custom-issuer.sigstore.json")

	logger := io.NewTestHandler()

	sigstoreConfig := verification.SigstoreConfig{
		ExternalHttpClient: http.DefaultClient,
		Logger:             logger,
		TUFMetadataDir:     o.Some(t.TempDir()),
	}

	ios, _, _, _ := iostreams.Test()
	hc, err := factory.HttpClientFunc(
		func() (gh.Config, error) { return config.NewMockConfig(), nil },
		ios,
		"test",
		"",
		&telemetry.NoOpService{},
	)()
	require.NoError(t, err)

	host, _ := auth.DefaultHost()

	sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(sigstoreConfig)
	require.NoError(t, err)
	baseOpts := Options{
		APIClient:        api.NewLiveClient(hc, http.DefaultClient, host, logger),
		ArtifactPath:     artifactPath,
		BundlePath:       bundlePath,
		DigestAlgorithm:  "sha256",
		Logger:           logger,
		OCIClient:        oci.NewLiveClient(),
		OIDCIssuer:       "https://token.actions.githubusercontent.com/hammer-time",
		PredicateType:    verification.SLSAPredicateV1,
		SigstoreVerifier: sigstoreVerifier,
	}

	t.Run("owner 和有效工作流(workflow) SAN 的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "too-legit"
		opts.SAN = "https://github.com/too-legit/attest/.github/workflows/integration.yml@refs/heads/main"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("owner 和有效工作流(workflow) SAN 正则(regex)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "too-legit"
		opts.SANRegex = "^https://github.com/too-legit/attest"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("repo 和有效工作流(workflow) SAN 的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "too-legit"
		opts.Repo = "too-legit/attest"
		opts.SAN = "https://github.com/too-legit/attest/.github/workflows/integration.yml@refs/heads/main"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("repo 和有效工作流(workflow) SAN 正则(regex)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "too-legit"
		opts.Repo = "too-legit/attest"
		opts.SANRegex = "^https://github.com/too-legit/attest"

		err := runVerify(&opts)
		require.NoError(t, err)
	})
}

func TestVerifyIntegrationReusableWorkflow(t *testing.T) {
	artifactPath := test.NormalizeRelativePath("../test/data/reusable-workflow-artifact")
	bundlePath := test.NormalizeRelativePath("../test/data/reusable-workflow-attestation.sigstore.json")

	logger := io.NewTestHandler()

	sigstoreConfig := verification.SigstoreConfig{
		ExternalHttpClient: http.DefaultClient,
		Logger:             logger,
		TUFMetadataDir:     o.Some(t.TempDir()),
	}

	cfg := config.NewMockConfig()
	ios, _, _, _ := iostreams.Test()
	hc, err := factory.HttpClientFunc(
		func() (gh.Config, error) { return cfg, nil },
		ios,
		"test",
		"",
		&telemetry.NoOpService{},
	)()
	require.NoError(t, err)

	host, _ := auth.DefaultHost()

	sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(sigstoreConfig)
	require.NoError(t, err)
	baseOpts := Options{
		APIClient:        api.NewLiveClient(hc, http.DefaultClient, host, logger),
		ArtifactPath:     artifactPath,
		BundlePath:       bundlePath,
		DigestAlgorithm:  "sha256",
		Logger:           logger,
		OCIClient:        oci.NewLiveClient(),
		OIDCIssuer:       verification.GitHubOIDCIssuer,
		PredicateType:    verification.SLSAPredicateV1,
		SigstoreVerifier: sigstoreVerifier,
	}

	t.Run("owner 和有效可复用工作流(reusable workflow) SAN 的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.SAN = "https://github.com/github/artifact-attestations-workflows/.github/workflows/attest.yml@09b495c3f12c7881b3cc17209a327792065c1a1d"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("owner 和有效可复用工作流(reusable workflow) SAN 正则(regex)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.SANRegex = "^https://github.com/github/artifact-attestations-workflows/"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("owner 和有效可复用签名仓库(signer repo)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.SignerRepo = "github/artifact-attestations-workflows"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("repo 和有效可复用工作流(reusable workflow) SAN 的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.Repo = "malancas/attest-demo"
		opts.SAN = "https://github.com/github/artifact-attestations-workflows/.github/workflows/attest.yml@09b495c3f12c7881b3cc17209a327792065c1a1d"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("repo 和有效可复用工作流(reusable workflow) SAN 正则(regex)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.Repo = "malancas/attest-demo"
		opts.SANRegex = "^https://github.com/github/artifact-attestations-workflows/"

		err := runVerify(&opts)
		require.NoError(t, err)
	})

	t.Run("repo 和有效可复用签名仓库(signer repo)的情况", func(t *testing.T) {
		opts := baseOpts
		opts.Owner = "malancas"
		opts.Repo = "malancas/attest-demo"
		opts.SignerRepo = "github/artifact-attestations-workflows"

		err := runVerify(&opts)
		require.NoError(t, err)
	})
}

func TestVerifyIntegrationReusableWorkflowSignerWorkflow(t *testing.T) {
	artifactPath := test.NormalizeRelativePath("../test/data/reusable-workflow-artifact")
	bundlePath := test.NormalizeRelativePath("../test/data/reusable-workflow-attestation.sigstore.json")

	logger := io.NewTestHandler()

	sigstoreConfig := verification.SigstoreConfig{
		ExternalHttpClient: http.DefaultClient,
		Logger:             logger,
		TUFMetadataDir:     o.Some(t.TempDir()),
	}

	cfg := config.NewMockConfig()
	ios, _, _, _ := iostreams.Test()
	hc, err := factory.HttpClientFunc(
		func() (gh.Config, error) { return cfg, nil },
		ios,
		"test",
		"",
		&telemetry.NoOpService{},
	)()
	require.NoError(t, err)

	host, _ := auth.DefaultHost()

	sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(sigstoreConfig)
	require.NoError(t, err)
	baseOpts := Options{
		APIClient:    api.NewLiveClient(hc, http.DefaultClient, host, logger),
		ArtifactPath: artifactPath,
		BundlePath:   bundlePath,
		Config: func() (gh.Config, error) {
			return cfg, nil
		},
		DigestAlgorithm:  "sha256",
		Logger:           logger,
		OCIClient:        oci.NewLiveClient(),
		OIDCIssuer:       verification.GitHubOIDCIssuer,
		Owner:            "malancas",
		PredicateType:    verification.SLSAPredicateV1,
		Repo:             "malancas/attest-demo",
		SigstoreVerifier: sigstoreVerifier,
	}

	type testcase struct {
		name           string
		signerWorkflow string
		expectErr      bool
		host           string
	}

	testcases := []testcase{
		{
			name:           "签名者工作流(signer workflow)无效的情况",
			signerWorkflow: "foo/bar/.github/workflows/attest.yml",
			expectErr:      true,
		},
		{
			name:           "带主机(host)的有效签名者工作流(signer workflow)",
			signerWorkflow: "github.com/github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectErr:      false,
		},
		{
			name:           "不带主机(host)的有效签名者工作流(signer workflow)(默认为 github.com)",
			signerWorkflow: "github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectErr:      false,
			host:           "github.com",
		},
	}

	for _, tc := range testcases {
		opts := baseOpts
		opts.SignerWorkflow = tc.signerWorkflow
		opts.Hostname = tc.host

		err := runVerify(&opts)
		if tc.expectErr {
			require.Error(t, err, "'%s' 应返回错误", tc.name)
		} else {
			require.NoError(t, err, "'%s' 出现意外错误", tc.name)
		}
	}
}
