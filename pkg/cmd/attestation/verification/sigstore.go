package verification

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	o "github.com/cli/cli/v2/pkg/option"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	PublicGoodIssuerOrg = "sigstore.dev"
	GitHubIssuerOrg     = "GitHub, Inc."
)

// AttestationProcessingResult 捕获对给定证明(attestation)的签名验证与策略评估的处理过程
type AttestationProcessingResult struct {
	Attestation        *api.Attestation           `json:"attestation"`
	VerificationResult *verify.VerificationResult `json:"verificationResult"`
}

type SigstoreConfig struct {
	TrustedRoot        string
	Logger             *io.Handler
	NoPublicGood       bool
	ExternalHttpClient *http.Client
	// 如果未使用租户(tenancy)模式，则信任域(trust domain)为空
	TrustDomain string
	// TUFMetadataDir
	TUFMetadataDir o.Option[string]
}

type SigstoreVerifier interface {
	Verify(attestations []*api.Attestation, policy verify.PolicyBuilder) ([]*AttestationProcessingResult, error)
}

type LiveSigstoreVerifier struct {
	Logger       *io.Handler
	NoPublicGood bool
	PublicGood   *verify.Verifier
	GitHub       *verify.Verifier
	Custom       map[string]*verify.Verifier
}

var ErrNoAttestationsVerified = errors.New("没有证明(attestation)通过验证")

// NewLiveSigstoreVerifier 创建一个新的 LiveSigstoreVerifier 结构体，
// 用于针对公共善意实例(Public Good)、GitHub 或自定义受信任根(trusted root)
// 验证制品(artifact)和证明(attestation)。
func NewLiveSigstoreVerifier(config SigstoreConfig) (*LiveSigstoreVerifier, error) {
	liveVerifier := &LiveSigstoreVerifier{
		Logger:       config.Logger,
		NoPublicGood: config.NoPublicGood,
	}
	// 如果设置了自定义受信任根(trusted root)，则配置自定义验证器(verifier)，
	// 并假定不需要公共善意实例(Public Good)或 GitHub 验证器(verifier)
	if config.TrustedRoot != "" {
		customVerifiers, err := createCustomVerifiers(config.TrustedRoot, config.NoPublicGood)
		if err != nil {
			return nil, fmt.Errorf("创建自定义验证器(verifier)出错: %s", err)
		}
		liveVerifier.Custom = customVerifiers
		return liveVerifier, nil
	}

	// 未设置自定义受信任根(trusted root)，因此配置公共善意实例(Public Good)和 GitHub 验证器(verifier)
	if !config.NoPublicGood {
		publicGoodVerifier, err := newPublicGoodVerifier(config.TUFMetadataDir, config.ExternalHttpClient)
		if err != nil {
			// 记录警告但继续执行 - PGI 不可用不应阻碍 GitHub 证明(attestation)的验证
			config.Logger.VerbosePrintf("警告: 初始化 Sigstore 公共善意实例(Public Good)验证器(verifier)失败: %v\n", err)
			config.Logger.VerbosePrintf("将在没有公共善意实例(Public Good Instance)验证的情况下继续\n")
		} else {
			liveVerifier.PublicGood = publicGoodVerifier
		}
	}

	github, err := newGitHubVerifier(config.TrustDomain, config.TUFMetadataDir, config.ExternalHttpClient)
	if err != nil {
		config.Logger.VerbosePrintf("警告: 初始化 GitHub 验证器(verifier)失败: %v\n", err)
	} else {
		liveVerifier.GitHub = github
	}

	if liveVerifier.noVerifierSet() {
		return nil, fmt.Errorf("无法初始化任何有效的 Sigstore 验证器(verifier)")
	}

	return liveVerifier, nil
}

func createCustomVerifiers(trustedRoot string, noPublicGood bool) (map[string]*verify.Verifier, error) {
	customTrustRoots, err := os.ReadFile(trustedRoot)
	if err != nil {
		return nil, fmt.Errorf("无法读取文件 %s: %v", trustedRoot, err)
	}

	verifiers := make(map[string]*verify.Verifier)
	reader := bufio.NewReader(bytes.NewReader(customTrustRoots))
	var line []byte
	var readError error
	line, readError = reader.ReadBytes('\n')
	for readError == nil {
		// 加载每个受信任根(trusted root)
		trustedRoot, err := root.NewTrustedRootFromJSON(line)
		if err != nil {
			return nil, fmt.Errorf("创建自定义验证器(verifier)失败: %v", err)
		}

		// 将 bundle 叶子证书(leafCert)的签发者(issuer)与受信任根(trusted root)的证书颁发机构(cert authority)进行比较
		certAuthorities := trustedRoot.FulcioCertificateAuthorities()
		for _, certAuthority := range certAuthorities {
			fulcioCertAuthority, ok := certAuthority.(*root.FulcioCertificateAuthority)
			if !ok {
				return nil, fmt.Errorf("受信任根(trusted root)的证书颁发机构(cert authority)不是 FulcioCertificateAuthority")
			}
			lowestCert, err := getLowestCertInChain(fulcioCertAuthority)
			if err != nil {
				return nil, err
			}

			// 如果自定义受信任根(trusted root)的签发者(issuer)未设置，则跳过它
			if len(lowestCert.Issuer.Organization) == 0 {
				continue
			}
			issuer := lowestCert.Issuer.Organization[0]

			// 确定与此受信任根(trusted root)配合使用的策略。
			//
			// 注意，我们*仅*根据签发者(issuer)来推断策略。
			// 我们*必须*使用所提供的受信任根(trusted root)。
			switch issuer {
			case PublicGoodIssuerOrg:
				if noPublicGood {
					return nil, fmt.Errorf("检测到公共善意实例(public good instance)，但请求的验证不包含公共善意实例")
				}
				if _, ok := verifiers[PublicGoodIssuerOrg]; ok {
					// 我们已经使用此自定义受信任根(trusted root)创建了公共善意实例(public good)验证器(verifier)，
					// 因此跳过它
					continue
				}
				publicGood, err := newPublicGoodVerifierWithTrustedRoot(trustedRoot)
				if err != nil {
					return nil, err
				}
				verifiers[PublicGoodIssuerOrg] = publicGood
			case GitHubIssuerOrg:
				if _, ok := verifiers[GitHubIssuerOrg]; ok {
					// 我们已经使用此自定义受信任根(trusted root)创建了 GitHub 验证器(verifier)，
					// 因此跳过它
					continue
				}
				github, err := newGitHubVerifierWithTrustedRoot(trustedRoot)
				if err != nil {
					return nil, err
				}
				verifiers[GitHubIssuerOrg] = github
			default:
				if _, ok := verifiers[issuer]; ok {
					// 我们已经使用此自定义受信任根(trusted root)创建了自定义验证器(verifier)，
					// 因此跳过它
					continue
				}
				// 对合理的策略做出最佳推测
				custom, err := newCustomVerifier(trustedRoot)
				if err != nil {
					return nil, err
				}
				verifiers[issuer] = custom
			}
		}
		line, readError = reader.ReadBytes('\n')
	}
	return verifiers, nil
}

func getBundleIssuer(b *bundle.Bundle) (string, error) {
	if !b.MinVersion("0.2") {
		return "", fmt.Errorf("不支持的 bundle 版本: %s", b.MediaType)
	}
	verifyContent, err := b.VerificationContent()
	if err != nil {
		return "", fmt.Errorf("获取 bundle 验证内容(verification content)失败: %v", err)
	}
	leafCert := verifyContent.Certificate()
	if leafCert == nil {
		return "", fmt.Errorf("未找到叶子证书(leaf cert)")
	}
	if len(leafCert.Issuer.Organization) != 1 {
		return "", fmt.Errorf("期望叶子证书(leaf certificate)的签发者(issuer)只包含一个组织(organization)")
	}
	return leafCert.Issuer.Organization[0], nil
}

func (v *LiveSigstoreVerifier) chooseVerifier(issuer string) (*verify.Verifier, error) {
	// 如果未设置自定义受信任根(trusted root)，则返回公共善意实例(Public Good)或 GitHub 验证器(verifier)
	// 如果所选验证器(verifier)尚未创建，则将其创建为 LiveSigstoreVerifier 字段，供后续调用使用
	if v.Custom != nil {
		custom, ok := v.Custom[issuer]
		if !ok {
			return nil, fmt.Errorf("未找到签发者(issuer) \"%s\" 对应的自定义验证器(verifier)", issuer)
		}
		return custom, nil
	}
	switch issuer {
	case PublicGoodIssuerOrg:
		if v.NoPublicGood {
			return nil, fmt.Errorf("检测到公共善意实例(public good instance)，但请求的验证不包含公共善意实例")
		}
		if v.PublicGood == nil {
			return nil, fmt.Errorf("公共善意实例(public good)验证器(verifier)不可用(初始化可能已失败)")
		}
		return v.PublicGood, nil
	case GitHubIssuerOrg:
		if v.GitHub == nil {
			return nil, fmt.Errorf("GitHub 验证器(verifier)不可用(初始化可能已失败)")
		}
		return v.GitHub, nil
	default:
		return nil, fmt.Errorf("无法识别叶子证书(leaf certificate)的签发者(issuer)")
	}
}

func getLowestCertInChain(ca *root.FulcioCertificateAuthority) (*x509.Certificate, error) {
	if len(ca.Intermediates) > 0 {
		return ca.Intermediates[0], nil
	} else if ca.Root != nil {
		return ca.Root, nil
	}

	return nil, fmt.Errorf("证书颁发机构(certificate authority)没有任何证书")
}

func (v *LiveSigstoreVerifier) verify(attestation *api.Attestation, policy verify.PolicyBuilder) (*AttestationProcessingResult, error) {
	issuer, err := getBundleIssuer(attestation.Bundle)
	if err != nil {
		return nil, fmt.Errorf("获取 bundle 签发者(issuer)失败: %v", err)
	}

	// 确定应由哪个验证器(verifier)尝试对该 bundle 进行验证
	verifier, err := v.chooseVerifier(issuer)
	if err != nil {
		return nil, fmt.Errorf("基于提供的 bundle 签发者(issuer)选择验证器(verifier)失败: %v", err)
	}

	v.Logger.VerbosePrintf("正在尝试针对签发者(issuer) \"%s\" 进行验证\n", issuer)
	// 尝试验证该证明(attestation)
	result, err := verifier.Verify(attestation.Bundle, policy)
	// 如果验证失败，则创建错误并提前退出验证
	if err != nil {
		v.Logger.VerbosePrint(v.Logger.ColorScheme.Redf(
			"针对签发者(issuer) \"%s\" 验证失败 \n\n", issuer,
		))

		return nil, fmt.Errorf("使用签发者(issuer) \"%s\" 进行验证时出错", issuer)
	}

	// 如果验证成功，则将结果添加到
	// AttestationProcessingResult 条目中
	v.Logger.VerbosePrint(v.Logger.ColorScheme.Greenf(
		"成功 - 已使用 \"%s\" 验证证明(attestation)签名\n", issuer,
	))

	return &AttestationProcessingResult{
		Attestation:        attestation,
		VerificationResult: result,
	}, nil
}

func (v *LiveSigstoreVerifier) Verify(attestations []*api.Attestation, policy verify.PolicyBuilder) ([]*AttestationProcessingResult, error) {
	if len(attestations) == 0 {
		return nil, ErrNoAttestationsVerified
	}

	results := make([]*AttestationProcessingResult, len(attestations))
	var verifyCount int
	var lastError error
	totalAttestations := len(attestations)
	for i, a := range attestations {
		v.Logger.VerbosePrintf("正在针对已配置的 Sigstore 受信任根(trust roots)验证证明(attestation) %d/%d\n", i+1, totalAttestations)

		apr, err := v.verify(a, policy)
		if err != nil {
			lastError = err
			// 如果验证失败，则继续处理 for 循环中的下一条证明(attestation)
			continue
		}
		// 否则，将该结果添加到 results 切片中并递增 verifyCount
		results[verifyCount] = apr
		verifyCount++
	}

	if verifyCount == 0 {
		return nil, lastError
	}

	// 截断 results 切片，使其仅包含已验证的证明(attestation)
	results = results[:verifyCount]

	return results, nil
}

func newCustomVerifier(trustedRoot *root.TrustedRoot) (*verify.Verifier, error) {
	// 关于该受信任根(trusted root)，我们所知的只有其配置，因此对策略应该如何设置
	// 做出一些合理推测。
	verifierConfig := []verify.VerifierOption{}
	// 这需要对签名证书(certificate)(例如来自 Sigstore Fulcio)的时间进行一些独立佐证，
	// 即以下之一:
	// - 来自受信任根(trusted root)中时间戳权威机构(timestamp authority)的签名时间戳
	// - 一条透明日志(transparency log)条目(例如来自 Sigstore Rekor)
	verifierConfig = append(verifierConfig, verify.WithObserverTimestamps(1))

	// 从受信任根(trusted root)的内容推断验证选项
	if len(trustedRoot.RekorLogs()) > 0 {
		verifierConfig = append(verifierConfig, verify.WithTransparencyLog(1))
	}

	gv, err := verify.NewVerifier(trustedRoot, verifierConfig...)
	if err != nil {
		return nil, fmt.Errorf("创建自定义验证器(verifier)失败: %v", err)
	}

	return gv, nil
}

func newGitHubVerifier(trustDomain string, tufMetadataDir o.Option[string], hc *http.Client) (*verify.Verifier, error) {
	var tr string

	opts := GitHubTUFOptions(tufMetadataDir, hc)
	client, err := tuf.New(opts)
	if err != nil {
		return nil, fmt.Errorf("创建 TUF 客户端(client)失败: %v", err)
	}

	if trustDomain == "" {
		tr = "trusted_root.json"
	} else {
		tr = fmt.Sprintf("%s.trusted_root.json", trustDomain)
	}
	jsonBytes, err := client.GetTarget(tr)
	if err != nil {
		return nil, err
	}
	trustedRoot, err := root.NewTrustedRootFromJSON(jsonBytes)
	if err != nil {
		return nil, err
	}
	return newGitHubVerifierWithTrustedRoot(trustedRoot)
}

func newGitHubVerifierWithTrustedRoot(trustedRoot *root.TrustedRoot) (*verify.Verifier, error) {
	gv, err := verify.NewVerifier(trustedRoot, verify.WithSignedTimestamps(1))
	if err != nil {
		return nil, fmt.Errorf("创建 GitHub 验证器(verifier)失败: %v", err)
	}

	return gv, nil
}

func newPublicGoodVerifier(tufMetadataDir o.Option[string], hc *http.Client) (*verify.Verifier, error) {
	opts := DefaultOptionsWithCacheSetting(tufMetadataDir, hc)
	client, err := tuf.New(opts)
	if err != nil {
		return nil, fmt.Errorf("创建 TUF 客户端(client)失败: %v", err)
	}
	trustedRoot, err := root.GetTrustedRoot(client)
	if err != nil {
		return nil, fmt.Errorf("获取受信任根(trusted root)失败: %v", err)
	}

	return newPublicGoodVerifierWithTrustedRoot(trustedRoot)
}

func newPublicGoodVerifierWithTrustedRoot(trustedRoot *root.TrustedRoot) (*verify.Verifier, error) {
	sv, err := verify.NewVerifier(trustedRoot, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return nil, fmt.Errorf("创建公共善意实例(Public Good)验证器(verifier)失败: %v", err)
	}

	return sv, nil
}

func (v *LiveSigstoreVerifier) noVerifierSet() bool {
	return v.PublicGood == nil && v.GitHub == nil && len(v.Custom) == 0
}
