package verification

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// 表示证书(certificate) RunnerEnvironment 扩展中的 GitHub 托管运行器(runner)
const GitHubRunner = "github-hosted"

// BuildDigestPolicyOption 根据给定的制品(artifact)摘要(digest)与摘要算法
// 构建一个 verify.ArtifactPolicyOption
func BuildDigestPolicyOption(a artifact.DigestedArtifact) (verify.ArtifactPolicyOption, error) {
	// sigstore-go 期望制品(artifact)摘要(digest)是从十六进制解码得到的
	decoded, err := hex.DecodeString(a.Digest())
	if err != nil {
		return nil, err
	}
	return verify.WithArtifactDigest(a.Algorithm(), decoded), nil
}

type EnforcementCriteria struct {
	Certificate   certificate.Summary
	PredicateType string
	SANRegex      string
	SAN           string
}

func (c EnforcementCriteria) Valid() error {
	if c.Certificate.Issuer == "" {
		return fmt.Errorf("必须设置 Issuer")
	}
	if c.Certificate.RunnerEnvironment != "" && c.Certificate.RunnerEnvironment != GitHubRunner {
		return fmt.Errorf("RunnerEnvironment 必须设置为 \"\" 或 %s", GitHubRunner)
	}
	if c.Certificate.SourceRepositoryOwnerURI == "" {
		return fmt.Errorf("必须设置 SourceRepositoryOwnerURI")
	}
	if c.PredicateType == "" {
		return fmt.Errorf("必须设置 PredicateType")
	}
	if c.SANRegex == "" && c.SAN == "" {
		return fmt.Errorf("必须设置 SANRegex 或 SAN")
	}
	return nil
}

func (c EnforcementCriteria) BuildPolicyInformation() string {
	policyAttr := [][]string{}

	policyAttr = appendStr(policyAttr, "- 断言类型必须匹配", c.PredicateType)

	policyAttr = appendStr(policyAttr, "- 源仓库所有者 URI 必须匹配", c.Certificate.SourceRepositoryOwnerURI)

	if c.Certificate.SourceRepositoryURI != "" {
		policyAttr = appendStr(policyAttr, "- 源仓库 URI 必须匹配", c.Certificate.SourceRepositoryURI)
	}

	if c.Certificate.BuildSignerDigest != "" {
		policyAttr = appendStr(policyAttr, "- 构建签名者摘要必须匹配", c.Certificate.BuildSignerDigest)
	}
	if c.Certificate.SourceRepositoryDigest != "" {
		policyAttr = appendStr(policyAttr, "- 源仓库摘要必须匹配", c.Certificate.SourceRepositoryDigest)
	}
	if c.Certificate.SourceRepositoryRef != "" {
		policyAttr = appendStr(policyAttr, "- 源仓库引用必须匹配", c.Certificate.SourceRepositoryRef)
	}

	if c.SAN != "" {
		policyAttr = appendStr(policyAttr, "- 使用者备用名称必须匹配", c.SAN)
	} else if c.SANRegex != "" {
		policyAttr = appendStr(policyAttr, "- 使用者备用名称必须匹配正则", c.SANRegex)
	}

	policyAttr = appendStr(policyAttr, "- OIDC 签发者必须匹配", c.Certificate.Issuer)
	if c.Certificate.RunnerEnvironment == GitHubRunner {
		policyAttr = appendStr(policyAttr, "- Action 工作流运行环境必须匹配", GitHubRunner)
	}

	maxColLen := 0
	for _, attr := range policyAttr {
		if len(attr[0]) > maxColLen {
			maxColLen = len(attr[0])
		}
	}

	var policyInfo strings.Builder
	for _, attr := range policyAttr {
		dots := strings.Repeat(".", maxColLen-len(attr[0]))
		policyInfo.WriteString(fmt.Sprintf("%s:%s %s\n", attr[0], dots, attr[1]))
	}

	return policyInfo.String()
}

func appendStr(arr [][]string, a, b string) [][]string {
	return append(arr, []string{a, b})
}
