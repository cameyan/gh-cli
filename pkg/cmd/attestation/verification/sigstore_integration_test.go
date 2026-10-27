//go:build integration

package verification

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test"
	o "github.com/cli/cli/v2/pkg/option"

	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
)

func TestLiveSigstoreVerifier(t *testing.T) {
	type testcase struct {
		name         string
		attestations []*api.Attestation
		expectErr    bool
		errContains  string
	}

	testcases := []testcase{
		{
			name:         "签名无效的情况",
			attestations: getAttestationsFor(t, "../test/data/sigstoreBundle-invalid-signature.json"),
			expectErr:    true,
			errContains:  "使用签发者(issuer) \"sigstore.dev\" 进行验证时出错",
		},
		{
			name:         "制品(artifact)有效且 JSON lines 文件包含多个 Sigstore bundle 的情况",
			attestations: getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl"),
		},
		{
			name:         "bundle 版本无效的情况",
			attestations: getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json"),
			expectErr:    true,
			errContains:  "不支持的 bundle 版本",
		},
		{
			name:         "没有证明(attestation)的情况",
			attestations: []*api.Attestation{},
			expectErr:    true,
			errContains:  "没有证明(attestation)通过验证",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
				ExternalHttpClient: http.DefaultClient,
				Logger:             io.NewTestHandler(),
				TUFMetadataDir:     o.Some(t.TempDir()),
			})
			require.NoError(t, err)

			results, err := verifier.Verify(tc.attestations, publicGoodPolicy(t))

			if tc.expectErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
				require.Nil(t, results)
			} else {
				require.NoError(t, err)
				require.Equal(t, len(tc.attestations), len(results))
			}
		})
	}

	t.Run("2/3 个证明(attestation)通过验证的情况", func(t *testing.T) {
		verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
			ExternalHttpClient: http.DefaultClient,
			Logger:             io.NewTestHandler(),
			TUFMetadataDir:     o.Some(t.TempDir()),
		})
		require.NoError(t, err)

		invalidBundle := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json")
		attestations := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")
		attestations = append(attestations, invalidBundle[0])
		require.Len(t, attestations, 3)

		results, err := verifier.Verify(attestations, publicGoodPolicy(t))

		require.Len(t, results, 2)
		require.NoError(t, err)
	})

	t.Run("0/2 个证明(attestation)通过验证而失败的情况", func(t *testing.T) {
		verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
			ExternalHttpClient: http.DefaultClient,
			Logger:             io.NewTestHandler(),
			TUFMetadataDir:     o.Some(t.TempDir()),
		})
		require.NoError(t, err)

		invalidBundle := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json")
		attestations := getAttestationsFor(t, "../test/data/sigstoreBundle-invalid-signature.json")
		attestations = append(attestations, invalidBundle[0])
		require.Len(t, attestations, 2)

		results, err := verifier.Verify(attestations, publicGoodPolicy(t))
		require.Nil(t, results)
		require.Error(t, err)
	})

	t.Run("GitHub Sigstore 制品(artifact)的情况", func(t *testing.T) {
		githubArtifactPath := test.NormalizeRelativePath("../test/data/github_provenance_demo-0.0.12-py3-none-any.whl")
		githubArtifact, err := artifact.NewDigestedArtifact(nil, githubArtifactPath, "sha256")
		require.NoError(t, err)

		githubPolicy := buildPolicy(t, *githubArtifact)

		attestations := getAttestationsFor(t, "../test/data/github_provenance_demo-0.0.12-py3-none-any-bundle.jsonl")

		verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
			ExternalHttpClient: http.DefaultClient,
			Logger:             io.NewTestHandler(),
			TUFMetadataDir:     o.Some(t.TempDir()),
		})
		require.NoError(t, err)

		results, err := verifier.Verify(attestations, githubPolicy)
		require.Len(t, results, 1)
		require.NoError(t, err)
	})

	t.Run("自定义受信任根(trusted root)的情况", func(t *testing.T) {
		attestations := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")

		verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
			ExternalHttpClient: http.DefaultClient,
			Logger:             io.NewTestHandler(),
			TrustedRoot:        test.NormalizeRelativePath("../test/data/trusted_root.json"),
			TUFMetadataDir:     o.Some(t.TempDir()),
		})
		require.NoError(t, err)

		results, err := verifier.Verify(attestations, publicGoodPolicy(t))
		require.Len(t, results, 2)
		require.NoError(t, err)
	})

	t.Run("当 GitHub 验证器(verifier)初始化失败时返回错误而非 panic 的情况", func(t *testing.T) {
		githubArtifactPath := test.NormalizeRelativePath("../test/data/github_provenance_demo-0.0.12-py3-none-any.whl")
		githubArtifact, err := artifact.NewDigestedArtifact(nil, githubArtifactPath, "sha256")
		require.NoError(t, err)

		githubPolicy := buildPolicy(t, *githubArtifact)
		attestations := getAttestationsFor(t, "../test/data/github_provenance_demo-0.0.12-py3-none-any-bundle.jsonl")

		verifier, err := NewLiveSigstoreVerifier(SigstoreConfig{
			ExternalHttpClient: http.DefaultClient,
			Logger:             io.NewTestHandler(),
			TrustDomain:        "missing-trust-domain",
			TUFMetadataDir:     o.Some(t.TempDir()),
		})
		require.NoError(t, err)
		results, verifyErr := verifier.Verify(attestations, githubPolicy)
		require.Nil(t, results)
		require.Error(t, verifyErr)
		require.ErrorContains(t, verifyErr, "基于提供的 bundle 签发者(issuer)选择验证器(verifier)失败")
		require.ErrorContains(t, verifyErr, "GitHub 验证器(verifier)不可用")
	})
}

func publicGoodPolicy(t *testing.T) verify.PolicyBuilder {
	t.Helper()

	artifactPath := test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz")
	publicGoodArtifact, err := artifact.NewDigestedArtifact(nil, artifactPath, "sha512")
	require.NoError(t, err)

	return buildPolicy(t, *publicGoodArtifact)
}

func buildPolicy(t *testing.T, artifact artifact.DigestedArtifact) verify.PolicyBuilder {
	t.Helper()

	artifactDigestPolicyOption, err := BuildDigestPolicyOption(artifact)
	require.NoError(t, err)

	return verify.NewPolicy(artifactDigestPolicyOption, verify.WithoutIdentitiesUnsafe())
}

func getAttestationsFor(t *testing.T, bundlePath string) []*api.Attestation {
	t.Helper()

	attestations, err := GetLocalAttestations(bundlePath)
	require.NoError(t, err)

	return attestations
}
