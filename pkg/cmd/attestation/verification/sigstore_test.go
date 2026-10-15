package verification

import (
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/stretchr/testify/require"
)

// 注意: 需要网络访问和 TUF 客户端初始化的测试
// 位于带有 //go:build integration 标记的 sigstore_integration_test.go 中。
// 这些单元测试专注于在不依赖网络访问的情况下测试逻辑。

// TestChooseVerifierWithNilPublicGood 测试当遇到 PGI 证明(attestation)但 PGI 验证器(verifier)为 nil(初始化失败)时，
// chooseVerifier 返回错误。
func TestChooseVerifierWithNilPublicGood(t *testing.T) {
	verifier := &LiveSigstoreVerifier{
		Logger:       io.NewTestHandler(),
		NoPublicGood: false,
		PublicGood:   nil, // 模拟 PGI 初始化失败
		GitHub:       nil, // 该测试不需要
	}

	_, err := verifier.chooseVerifier(PublicGoodIssuerOrg)

	require.Error(t, err)
	require.ErrorContains(t, err, "公共服务实例(Public Good Instance)验证器不可用")
}

func TestChooseVerifierWithNilGitHub(t *testing.T) {
	verifier := &LiveSigstoreVerifier{
		Logger:       io.NewTestHandler(),
		NoPublicGood: false,
		PublicGood:   nil,
		GitHub:       nil, // 模拟 GitHub 验证器(verifier)初始化失败
	}

	_, err := verifier.chooseVerifier(GitHubIssuerOrg)

	require.Error(t, err)
	require.ErrorContains(t, err, "GitHub 验证器不可用")
}

// TestChooseVerifierUnrecognizedIssuer 测试对于无法识别的签发者(issuer)
// 返回错误。
func TestChooseVerifierUnrecognizedIssuer(t *testing.T) {
	verifier := &LiveSigstoreVerifier{
		Logger:       io.NewTestHandler(),
		NoPublicGood: false,
	}

	_, err := verifier.chooseVerifier("unknown-issuer")

	require.Error(t, err)
	require.ErrorContains(t, err, "无法识别叶子证书的签发者")
}

func TestLiveSigstoreVerifier_noVerifierSet(t *testing.T) {
	verifier := &LiveSigstoreVerifier{
		Logger:       io.NewTestHandler(),
		NoPublicGood: true,
		PublicGood:   nil,
		GitHub:       nil,
	}

	require.True(t, verifier.noVerifierSet())
}
