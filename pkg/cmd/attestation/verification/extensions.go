package verification

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

var (
	GitHubOIDCIssuer       = "https://token.actions.githubusercontent.com"
	GitHubTenantOIDCIssuer = "https://token.actions.%s.ghe.com"
)

// VerifyCertExtensions 使我们能够对证书(certificate)扩展进行大小写不敏感的比较
func VerifyCertExtensions(results []*AttestationProcessingResult, ec EnforcementCriteria) ([]*AttestationProcessingResult, error) {
	if len(results) == 0 {
		return nil, errors.New("没有证明(attestation)处理结果")
	}

	verified := make([]*AttestationProcessingResult, 0, len(results))
	var lastErr error
	for _, attestation := range results {
		if err := verifyCertExtensions(*attestation.VerificationResult.Signature.Certificate, ec.Certificate); err != nil {
			lastErr = err
			// 如果验证失败，则继续处理 for 循环中的下一条证明(attestation)
			continue
		}
		// 否则，将该结果添加到 results 切片中并递增 verifyCount
		verified = append(verified, attestation)
	}

	// 如果退出 for 循环时没有验证任何证明(attestation)，
	// 则返回找到的最后一个错误
	if len(verified) == 0 {
		return nil, lastErr
	}

	return verified, nil
}

func verifyCertExtensions(given, expected certificate.Summary) error {
	if !strings.EqualFold(expected.SourceRepositoryOwnerURI, given.SourceRepositoryOwnerURI) {
		return fmt.Errorf("期望 SourceRepositoryOwnerURI 为 %s，实际为 %s", expected.SourceRepositoryOwnerURI, given.SourceRepositoryOwnerURI)
	}

	// 如果 repo 已设置，则比较 SourceRepositoryURI 字段
	if expected.SourceRepositoryURI != "" && !strings.EqualFold(expected.SourceRepositoryURI, given.SourceRepositoryURI) {
		return fmt.Errorf("期望 SourceRepositoryURI 为 %s，实际为 %s", expected.SourceRepositoryURI, given.SourceRepositoryURI)
	}

	// 比较 OIDC 签发者(issuer)。如果不相等，则根据是否存在部分匹配
	// 返回相应的错误
	if !strings.EqualFold(expected.Issuer, given.Issuer) {
		if strings.Index(given.Issuer, expected.Issuer+"/") == 0 {
			return fmt.Errorf("期望 Issuer 为 %s，实际为 %s -- 如果您的企业有自定义 OIDC 签发者(issuer)策略，请使用 --cert-oidc-issuer 标志(flag)并指定您期望的签发者(issuer)", expected.Issuer, given.Issuer)
		}
		return fmt.Errorf("期望 Issuer 为 %s，实际为 %s", expected.Issuer, given.Issuer)
	}

	if expected.BuildSignerDigest != "" && !strings.EqualFold(expected.BuildSignerDigest, given.BuildSignerDigest) {
		return fmt.Errorf("期望 BuildSignerDigest 为 %s，实际为 %s", expected.BuildSignerDigest, given.BuildSignerDigest)
	}
	if expected.SourceRepositoryDigest != "" && !strings.EqualFold(expected.SourceRepositoryDigest, given.SourceRepositoryDigest) {
		return fmt.Errorf("期望 SourceRepositoryDigest 为 %s，实际为 %s", expected.SourceRepositoryDigest, given.SourceRepositoryDigest)
	}
	// SourceRepositoryRef 必须进行大小写敏感的比较
	if expected.SourceRepositoryRef != "" && expected.SourceRepositoryRef != given.SourceRepositoryRef {
		return fmt.Errorf("期望 SourceRepositoryRef 为 %s，实际为 %s", expected.SourceRepositoryRef, given.SourceRepositoryRef)
	}

	return nil
}
