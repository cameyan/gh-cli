package verify

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
)

const hostRegex = `^[a-zA-Z0-9-]+\.[a-zA-Z0-9-]+.*$`

// signerWorkflowRefPattern 匹配终止于为 GitHub Actions 工作流签发的
// 证书(certificate) SubjectAlternativeName 的 "@<ref>"。
//
// 该分隔符无法通过按 "@" 拆分来定位，因为两侧都可能包含 "@"。
// git 引用(ref)可能包含 "@"，例如 JavaScript monorepo 所用的
// refs/tags/pkg@1.2.3 约定，工作流文件名也可能包含 "@"。区分它们的是形态:
// 引用(ref)要么是 "refs/" 前缀的路径，要么是裸对象 ID，而工作流文件名
// 两者都不是。它不能包含 "/"，因为 Actions 只加载直接存储在
// .github/workflows 中的工作流，它也不能是裸对象 ID，因为它必须带有
// .yml 或 .yaml 扩展名。
const signerWorkflowRefPattern = `@(refs/.*|[0-9a-fA-F]+)`

// signerWorkflowHasRef 报告 --signer-workflow 值是否已固定(pin)了一个引用(ref)。
var signerWorkflowHasRef = regexp.MustCompile(signerWorkflowRefPattern + `$`)

// signerWorkflowSANPattern 为 --signer-workflow 构建强制执行的 SAN 模式(pattern)。
//
// sigstore-go 使用 regexp.MatchString 匹配 SAN 模式(pattern)，该匹配是非锚定的，
// 因此仅在开头锚定的模式(pattern)是前缀匹配: 名为
// release.yml.attacker.yml 的工作流可以满足对 release.yml 的固定(pin)。
// 在结尾锚定可以使固定(pin)成为精确的身份匹配(identity match)。
//
// 文档记载的标志(flag)格式不携带引用(ref)，这样的值必须继续匹配
// 来自任何引用(ref)的证书(certificate)，因此引用(ref)是可选的。
// 一旦该值固定(pin)了它自己的引用(ref)，它就必须与 SAN 的结尾精确匹配。
// 在那里让引用(ref)保持可选，将允许在已固定(pin)的引用(ref)之后再跟随另一个引用(ref)，
// 并且由于分支(branch)可以命名为
// "main@refs/heads/attacker"，对 refs/heads/main 的固定(pin)仍会接受
// 由不同分支(branch)构建的证书(certificate)。
func signerWorkflowSANPattern(workflowURL string) string {
	pattern := "^" + regexp.QuoteMeta(workflowURL)
	if signerWorkflowHasRef.MatchString(workflowURL) {
		return pattern + "$"
	}
	return pattern + "(" + signerWorkflowRefPattern + ")?$"
}

func expandToGitHubURL(tenant, ownerOrRepo string) string {
	if tenant == "" {
		return fmt.Sprintf("https://github.com/%s", ownerOrRepo)
	}
	return fmt.Sprintf("https://%s.ghe.com/%s", tenant, ownerOrRepo)
}

func expandToGitHubURLRegex(tenant, ownerOrRepo string) string {
	url := expandToGitHubURL(tenant, ownerOrRepo)
	return fmt.Sprintf("(?i)^%s", regexp.QuoteMeta(url+"/"))
}

func newEnforcementCriteria(opts *Options) (verification.EnforcementCriteria, error) {
	// 使用提供的 PredicateType 初始化执行标准(enforcement criteria)
	c := verification.EnforcementCriteria{
		PredicateType: opts.PredicateType,
	}

	// 通过检查 repo 和 owner 选项来设置 owner 值
	var owner string
	if opts.Repo != "" {
		// 我们期望 repo 参数的格式为 <OWNER>/<REPO>
		splitRepo := strings.Split(opts.Repo, "/")
		// 如果提供了 Repo 但未提供 owner，则将 Repo 值的 OWNER 部分设置为 Owner
		owner = splitRepo[0]
	} else {
		// 否则使用用户提供的 owner 值
		owner = opts.Owner
	}

	// 使用提供的选项设置 SANRegex 和 SAN 值
	// 首先检查是否提供了 opts.SANRegex 或 opts.SAN 值
	if opts.SANRegex != "" || opts.SAN != "" {
		c.SANRegex = opts.SANRegex
		c.SAN = opts.SAN
	} else if opts.SignerRepo != "" {
		// 接着检查是否提供了 opts.SignerRepo
		signedRepoRegex := expandToGitHubURLRegex(opts.Tenant, opts.SignerRepo)
		c.SANRegex = signedRepoRegex
	} else if opts.SignerWorkflow != "" {
		validatedWorkflowRegex, err := validateSignerWorkflow(opts.Hostname, opts.SignerWorkflow)
		if err != nil {
			return verification.EnforcementCriteria{}, err
		}
		c.SANRegex = validatedWorkflowRegex
	} else if opts.Repo != "" {
		// 如果用户未提供 SAN、SANRegex、SignerRepo 或 SignerWorkflow 选项，
		// 则我们默认使用 repo 选项
		c.SANRegex = expandToGitHubURLRegex(opts.Tenant, opts.Repo)
	} else {
		// 如果未提供 opts.Repo，我们回退到 opts.Owner 值
		c.SANRegex = expandToGitHubURLRegex(opts.Tenant, owner)
	}

	// 如果 DenySelfHostedRunner 选项设置为 true，则将
	// RunnerEnvironment 扩展设置为 GitHub 托管运行器(hosted runner)值
	if opts.DenySelfHostedRunner {
		c.Certificate.RunnerEnvironment = verification.GitHubRunner
	} else {
		// 如果通过第二个函数参数将 Certificate.RunnerEnvironment 值设置为空字符串，
		// 则不会在 RunnerEnvironment 字段上进行任何证书(certificate)匹配
		c.Certificate.RunnerEnvironment = ""
	}

	// 如果提供了 Repo 选项，则设置 SourceRepositoryURI 扩展
	if opts.Repo != "" {
		c.Certificate.SourceRepositoryURI = expandToGitHubURL(opts.Tenant, opts.Repo)
	}

	// 使用 owner 和 tenant(如果提供)设置 SourceRepositoryOwnerURI 扩展
	c.Certificate.SourceRepositoryOwnerURI = expandToGitHubURL(opts.Tenant, owner)

	// 如果提供了 tenant 且提供的 OIDC 签发者(issuer)与默认值匹配，
	// 则使用租户(tenant)专属的签发者(issuer)
	if opts.Tenant != "" && opts.OIDCIssuer == verification.GitHubOIDCIssuer {
		c.Certificate.Issuer = fmt.Sprintf(verification.GitHubTenantOIDCIssuer, opts.Tenant)
	} else {
		// 否则使用作为选项提供的自定义 OIDC 签发者(issuer)
		c.Certificate.Issuer = opts.OIDCIssuer
	}

	// 如果提供了相应选项，则设置 SourceRepositoryDigest、SourceRepositoryRef
	// 和 BuildSignerDigest 扩展
	c.Certificate.BuildSignerDigest = opts.SignerDigest
	c.Certificate.SourceRepositoryDigest = opts.SourceDigest
	c.Certificate.SourceRepositoryRef = opts.SourceRef

	return c, nil
}

func buildCertificateIdentityOption(c verification.EnforcementCriteria) (verify.PolicyOption, error) {
	sanMatcher, err := verify.NewSANMatcher(c.SAN, c.SANRegex)
	if err != nil {
		return nil, err
	}

	// 接受任意签发者(issuer)，签发者(issuer)将作为扩展验证(verification)的一部分进行验证
	issuerMatcher, err := verify.NewIssuerMatcher("", ".*")
	if err != nil {
		return nil, err
	}

	extensions := certificate.Extensions{
		RunnerEnvironment: c.Certificate.RunnerEnvironment,
	}

	certId, err := verify.NewCertificateIdentity(sanMatcher, issuerMatcher, extensions)
	if err != nil {
		return nil, err
	}

	return verify.WithCertificateIdentity(certId), nil
}

func buildSigstoreVerifyPolicy(c verification.EnforcementCriteria, a artifact.DigestedArtifact) (verify.PolicyBuilder, error) {
	artifactDigestPolicyOption, err := verification.BuildDigestPolicyOption(a)
	if err != nil {
		return verify.PolicyBuilder{}, err
	}

	certIdOption, err := buildCertificateIdentityOption(c)
	if err != nil {
		return verify.PolicyBuilder{}, err
	}

	policy := verify.NewPolicy(artifactDigestPolicyOption, certIdOption)
	return policy, nil
}

func validateSignerWorkflow(hostname, signerWorkflow string) (string, error) {
	// 我们期望提供的工作流(workflow)参数格式为 [HOST/]<OWNER>/<REPO>/path/to/workflow.yml
	// 如果提供的工作流(workflow)不包含主机(host)，则设置主机(host)
	match, err := regexp.MatchString(hostRegex, signerWorkflow)
	if err != nil {
		return "", err
	}

	if match {
		return signerWorkflowSANPattern(fmt.Sprintf("https://%s", signerWorkflow)), nil
	}

	// 如果提供的工作流(workflow)与期望的格式不匹配，
	// 我们将继续使用提供的主机名(hostname)创建签名者工作流(signer workflow)
	if hostname == "" {
		return "", errors.New("未知的签名者工作流(signer workflow)主机(host)")
	}

	return signerWorkflowSANPattern(fmt.Sprintf("https://%s/%s", hostname, signerWorkflow)), nil
}
