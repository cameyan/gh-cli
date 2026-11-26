//go:build integration

package verify

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/stretchr/testify/require"
)

func getAttestationsFor(t *testing.T, bundlePath string) []*api.Attestation {
	t.Helper()

	attestations, err := verification.GetLocalAttestations(bundlePath)
	require.NoError(t, err)

	return attestations
}

func TestVerifyAttestations(t *testing.T) {
	sgVerifier, err := verification.NewLiveSigstoreVerifier(verification.SigstoreConfig{
		ExternalHttpClient: http.DefaultClient,
		Logger:             io.NewTestHandler(),
		TUFMetadataDir:     o.Some(t.TempDir()),
	})
	require.NoError(t, err)

	certSummary := certificate.Summary{}
	certSummary.SourceRepositoryOwnerURI = "https://github.com/sigstore"
	certSummary.SourceRepositoryURI = "https://github.com/sigstore/sigstore-js"
	certSummary.Issuer = verification.GitHubOIDCIssuer

	ec := verification.EnforcementCriteria{
		Certificate:   certSummary,
		PredicateType: verification.SLSAPredicateV1,
		SANRegex:      "^https://github.com/sigstore/",
	}
	require.NoError(t, ec.Valid())

	artifactPath := test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz")
	a, err := artifact.NewDigestedArtifact(nil, artifactPath, "sha512")
	require.NoError(t, err)

	t.Run("所有证明(attestation)均通过验证", func(t *testing.T) {
		attestations := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")
		require.Len(t, attestations, 2)
		results, errMsg, err := verifyAttestations(*a, attestations, sgVerifier, ec)
		require.NoError(t, err)
		require.Zero(t, errMsg)
		require.Len(t, results, 2)
	})

	t.Run("2/3 个证明(attestation)通过 Sigstore 验证的情况", func(t *testing.T) {
		invalidBundle := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json")
		attestations := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")
		attestations = append(attestations, invalidBundle[0])
		require.Len(t, attestations, 3)

		results, errMsg, err := verifyAttestations(*a, attestations, sgVerifier, ec)
		require.NoError(t, err)
		require.Zero(t, errMsg)
		require.Len(t, results, 2)
	})

	t.Run("当 Sigstore 验证失败时判定验证失败", func(t *testing.T) {
		invalidBundle := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json")
		invalidBundle2 := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0-bundle-v0.1.json")
		attestations := append(invalidBundle, invalidBundle2...)
		require.Len(t, attestations, 2)

		results, errMsg, err := verifyAttestations(*a, attestations, sgVerifier, ec)
		require.Error(t, err)
		require.Contains(t, errMsg, "✗ Sigstore 验证失败")
		require.Nil(t, results)
	})

	t.Run("当证书(certificate)扩展与执行标准(enforcement criteria)不匹配时证明(attestation)验证失败", func(t *testing.T) {
		sgjAttestation := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")
		reusableWorkflowAttestations := getAttestationsFor(t, "../test/data/reusable-workflow-attestation.sigstore.json")
		attestations := []*api.Attestation{sgjAttestation[0], reusableWorkflowAttestations[0], sgjAttestation[1]}
		require.Len(t, attestations, 3)

		rwfResult := verification.BuildMockResult(reusableWorkflowAttestations[0].Bundle, "", "", "https://github.com/malancas", "", verification.GitHubOIDCIssuer)
		sgjResult := verification.BuildSigstoreJsMockResult(t)
		mockResults := []*verification.AttestationProcessingResult{&sgjResult, &rwfResult, &sgjResult}
		mockSgVerifier := verification.NewMockSigstoreVerifierWithMockResults(t, mockResults)

		// 我们想测试通过 Sigstore 验证但未通过证书(certificate)扩展验证的证明(attestation)
		// 会在 verifyAttestations 的第二步中被正确过滤。
		// 通过使用 mock Sigstore 验证器(verifier)，我们可以确保
		// verifyAttestations 中对 verification.VerifyCertExtensions 的调用
		// 按预期过滤证明(attestation)
		results, errMsg, err := verifyAttestations(*a, attestations, mockSgVerifier, ec)
		require.NoError(t, err)
		require.Zero(t, errMsg)
		require.Len(t, results, 2)
		for _, result := range results {
			require.NotEqual(t, result.Attestation.Bundle, reusableWorkflowAttestations[0].Bundle)
		}
	})

	t.Run("当证书(certificate)扩展验证失败时判定验证失败", func(t *testing.T) {
		attestations := getAttestationsFor(t, "../test/data/sigstore-js-2.1.0_with_2_bundles.jsonl")
		require.Len(t, attestations, 2)

		expectedCriteria := ec
		expectedCriteria.Certificate.SourceRepositoryOwnerURI = "https://github.com/wrong"

		results, errMsg, err := verifyAttestations(*a, attestations, sgVerifier, expectedCriteria)
		require.Error(t, err)
		require.Contains(t, errMsg, "✗ 策略(policy)验证失败")
		require.Nil(t, results)
	})
}
