package verification

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
)

func createSampleResult() *AttestationProcessingResult {
	return &AttestationProcessingResult{
		VerificationResult: &verify.VerificationResult{
			Signature: &verify.SignatureVerificationResult{
				Certificate: &certificate.Summary{
					SourceRepositoryOwnerURI: "https://github.com/owner",
					SourceRepositoryURI:      "https://github.com/owner/repo",
					Issuer:                   "https://token.actions.githubusercontent.com",
					SourceRepositoryRef:      "refs/heads/main",
				},
			},
		},
	}
}

func TestVerifyCertExtensions(t *testing.T) {
	results := []*AttestationProcessingResult{createSampleResult()}

	certSummary := certificate.Summary{
		SourceRepositoryOwnerURI: "https://github.com/owner",
		SourceRepositoryURI:      "https://github.com/owner/repo",
		Issuer:                   GitHubOIDCIssuer,
		SourceRepositoryRef:      "refs/heads/main",
	}

	c := EnforcementCriteria{
		Certificate: certSummary,
	}

	t.Run("单条结果通过的情况", func(t *testing.T) {
		verified, err := VerifyCertExtensions(results, c)
		require.NoError(t, err)
		require.Len(t, verified, 1)
	})

	t.Run("1/2 条有效结果通过的情况", func(t *testing.T) {
		twoResults := []*AttestationProcessingResult{createSampleResult(), createSampleResult()}
		require.Len(t, twoResults, 2)
		twoResults[1].VerificationResult.Signature.Certificate.Extensions.SourceRepositoryOwnerURI = "https://github.com/wrong"

		verified, err := VerifyCertExtensions(twoResults, c)
		require.NoError(t, err)
		require.Len(t, verified, 1)
	})

	t.Run("所有结果验证失败的情况", func(t *testing.T) {
		twoResults := []*AttestationProcessingResult{createSampleResult(), createSampleResult()}
		require.Len(t, twoResults, 2)
		twoResults[0].VerificationResult.Signature.Certificate.Extensions.SourceRepositoryOwnerURI = "https://github.com/wrong"
		twoResults[1].VerificationResult.Signature.Certificate.Extensions.SourceRepositoryOwnerURI = "https://github.com/wrong"

		verified, err := VerifyCertExtensions(twoResults, c)
		require.Error(t, err)
		require.Nil(t, verified)
	})

	t.Run("SourceRepositoryOwnerURI 错误的情况", func(t *testing.T) {
		expectedCriteria := c
		expectedCriteria.Certificate.SourceRepositoryOwnerURI = "https://github.com/wrong"
		verified, err := VerifyCertExtensions(results, expectedCriteria)
		require.ErrorContains(t, err, "期望 SourceRepositoryOwnerURI 为 https://github.com/wrong，实际为 https://github.com/owner")
		require.Nil(t, verified)
	})

	t.Run("SourceRepositoryURI 错误的情况", func(t *testing.T) {
		expectedCriteria := c
		expectedCriteria.Certificate.SourceRepositoryURI = "https://github.com/foo/wrong"
		verified, err := VerifyCertExtensions(results, expectedCriteria)
		require.ErrorContains(t, err, "期望 SourceRepositoryURI 为 https://github.com/foo/wrong，实际为 https://github.com/owner/repo")
		require.Nil(t, verified)
	})

	t.Run("OIDCIssuer 错误的情况", func(t *testing.T) {
		expectedCriteria := c
		expectedCriteria.Certificate.Issuer = "wrong"
		verified, err := VerifyCertExtensions(results, expectedCriteria)
		require.ErrorContains(t, err, "期望 Issuer 为 wrong，实际为 https://token.actions.githubusercontent.com")
		require.Nil(t, verified)
	})

	t.Run("OIDCIssuer 部分匹配的情况", func(t *testing.T) {
		expectedResults := []*AttestationProcessingResult{createSampleResult()}
		expectedResults[0].VerificationResult.Signature.Certificate.Extensions.Issuer = "https://token.actions.githubusercontent.com/foo-bar"
		verified, err := VerifyCertExtensions(expectedResults, c)
		require.ErrorContains(t, err, "期望 Issuer 为 https://token.actions.githubusercontent.com，实际为 https://token.actions.githubusercontent.com/foo-bar -- 如果您的企业有自定义 OIDC 签发者")
		require.Nil(t, verified)
	})

	t.Run("SourceRepositoryRef 大小写敏感错误的情况", func(t *testing.T) {
		expectedResults := []*AttestationProcessingResult{createSampleResult()}
		expectedResults[0].VerificationResult.Signature.Certificate.Extensions.SourceRepositoryRef = "refs/heads/MAIN"
		verified, err := VerifyCertExtensions(expectedResults, c)
		require.ErrorContains(t, err, "期望 SourceRepositoryRef 为 refs/heads/main，实际为 refs/heads/MAIN")
		require.Nil(t, verified)
	})
}
