package verify

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
)

// Options 捕获 verify 命令的选项
type Options struct {
	ArtifactPath          string
	BundlePath            string
	UseBundleFromRegistry bool
	Config                func() (gh.Config, error)
	TrustedRoot           string
	DenySelfHostedRunner  bool
	DigestAlgorithm       string
	Limit                 int
	NoPublicGood          bool
	OIDCIssuer            string
	Owner                 string
	PredicateType         string
	Repo                  string
	SAN                   string
	SANRegex              string
	SignerDigest          string
	SignerRepo            string
	SignerWorkflow        string
	SourceDigest          string
	SourceRef             string
	APIClient             api.Client
	Logger                *io.Handler
	OCIClient             oci.Client
	SigstoreVerifier      verification.SigstoreVerifier
	exporter              cmdutil.Exporter
	Hostname              string
	// 仅在使用租户(tenancy)模式时才会设置 Tenant
	Tenant string
}

// Clean 清理文件路径选项值
func (opts *Options) Clean() {
	if opts.BundlePath != "" {
		opts.BundlePath = filepath.Clean(opts.BundlePath)
	}
}

// FetchAttestationsFromGitHubAPI 返回该命令是否应从 GitHub API 获取证明(attestation)
// 它会检查未提供 bundle 路径，且未设置 "use bundle from registry" 标志(flag)
func (opts *Options) FetchAttestationsFromGitHubAPI() bool {
	return opts.BundlePath == "" && !opts.UseBundleFromRegistry
}

// AreFlagsValid 检查提供的标志(flag)组合是否有效，
// 否则返回错误
func (opts *Options) AreFlagsValid() error {
	// 如果提供了 Repo，检查其格式是否符合预期的 <OWNER>/<REPO>
	if opts.Repo != "" && !isProvidedRepoValid(opts.Repo) {
		return fmt.Errorf("为 repo 提供的值无效: %s", opts.Repo)
	}

	// 如果提供了 SignerRepo，检查其格式是否符合预期的 <OWNER>/<REPO>
	if opts.SignerRepo != "" && !isProvidedRepoValid(opts.SignerRepo) {
		return fmt.Errorf("为 signer-repo 提供的值无效: %s", opts.SignerRepo)
	}

	// 检查 limit 是否介于 1 和 1000 之间
	if opts.Limit < 1 || opts.Limit > 1000 {
		return fmt.Errorf("不允许使用 limit %d，必须介于 1 和 1000 之间", opts.Limit)
	}

	// 检查 bundle-from-oci 标志(flag)是否仅与 OCI 制品(artifact)路径一起使用
	if opts.UseBundleFromRegistry && !strings.HasPrefix(opts.ArtifactPath, "oci://") {
		return fmt.Errorf("bundle-from-oci 标志只能用于 OCI 制品路径")
	}

	// 检查 bundle-from-oci 和 bundle-path 标志(flag)是否未被同时使用
	if opts.UseBundleFromRegistry && opts.BundlePath != "" {
		return fmt.Errorf("bundle-from-oci 标志不能与 bundle-path 标志一起使用")
	}

	// 验证提供的主机名(hostname)
	if opts.Hostname != "" {
		if err := ghinstance.HostnameValidator(opts.Hostname); err != nil {
			return fmt.Errorf("解析主机名出错: %w", err)
		}
	}

	return nil
}

func isProvidedRepoValid(repo string) bool {
	// 我们期望提供的仓库(repository)参数格式为 <OWNER>/<REPO>
	splitRepo := strings.Split(repo, "/")
	return len(splitRepo) == 2
}
