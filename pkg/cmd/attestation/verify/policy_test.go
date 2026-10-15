package verify

import (
	"regexp"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
)

func TestNewEnforcementCriteria(t *testing.T) {
	artifactPath := "../test/data/sigstore-js-2.1.0.tgz"

	t.Run("使用 SANRegex 和 SAN 设置 SANRegex 和 SAN", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:   artifactPath,
			Owner:          "foo",
			Repo:           "foo/bar",
			SAN:            "https://github/foo/bar/.github/workflows/attest.yml",
			SANRegex:       "(?i)^https://github/foo",
			SignerRepo:     "wrong/value",
			SignerWorkflow: "wrong/value/.github/workflows/attest.yml",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://github/foo/bar/.github/workflows/attest.yml", c.SAN)
		require.Equal(t, "(?i)^https://github/foo", c.SANRegex)
	})

	t.Run("使用 SignerRepo 设置 SANRegex", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:   artifactPath,
			Owner:          "wrong",
			Repo:           "wrong/value",
			SignerRepo:     "foo/bar",
			SignerWorkflow: "wrong/value/.github/workflows/attest.yml",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `(?i)^https://github\.com/foo/bar/`, c.SANRegex)
		require.Zero(t, c.SAN)
	})

	t.Run("使用 SignerRepo 和 Tenant 设置 SANRegex", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:   artifactPath,
			Owner:          "wrong",
			Repo:           "wrong/value",
			SignerRepo:     "foo/bar",
			SignerWorkflow: "wrong/value/.github/workflows/attest.yml",
			Tenant:         "baz",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `(?i)^https://baz\.ghe\.com/foo/bar/`, c.SANRegex)
		require.Zero(t, c.SAN)
	})

	t.Run("使用匹配主机正则的 SignerWorkflow 设置 SANRegex", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:   artifactPath,
			Owner:          "wrong",
			Repo:           "wrong/value",
			SignerWorkflow: "foo/bar/.github/workflows/attest.yml",
			Hostname:       "github.com",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `^https://github\.com/foo/bar/\.github/workflows/attest\.yml(@(refs/.*|[0-9a-fA-F]+))?$`, c.SANRegex)
		require.Zero(t, c.SAN)
	})

	t.Run("使用 opts.Repo 设置 SANRegex", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "wrong",
			Repo:         "foo/bar",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `(?i)^https://github\.com/foo/bar/`, c.SANRegex)
	})

	t.Run("使用 opts.Owner 设置 SANRegex", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `(?i)^https://github\.com/foo/`, c.SANRegex)
	})

	t.Run("SANRegex 转义 repo 名称中的正则元字符", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			SignerRepo:   "my.org/my.repo",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, `(?i)^https://github\.com/my\.org/my\.repo/`, c.SANRegex)

		// 验证生成的正则(regex)不会匹配一个外观相似的仓库(repo)
		re := regexp.MustCompile(c.SANRegex)
		require.True(t, re.MatchString("https://github.com/my.org/my.repo/.github/workflows/build.yml"))
		require.False(t, re.MatchString("https://github.com/myXorg/myXrepo/.github/workflows/build.yml"))
	})

	t.Run("当 opts.DenySelfHostedRunner 为 true 时将 Extensions.RunnerEnvironment 设置为 GitHubRunner 值", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:         artifactPath,
			Owner:                "foo",
			Repo:                 "foo/bar",
			DenySelfHostedRunner: true,
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, verification.GitHubRunner, c.Certificate.RunnerEnvironment)
	})

	t.Run("当 opts.DenySelfHostedRunner 为 false 时将 Extensions.RunnerEnvironment 设置为 * 值", func(t *testing.T) {
		opts := &Options{
			ArtifactPath:         artifactPath,
			Owner:                "foo",
			Repo:                 "foo/bar",
			DenySelfHostedRunner: false,
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Zero(t, c.Certificate.RunnerEnvironment)
	})

	t.Run("使用 opts.Repo 和 opts.Tenant 设置 Extensions.SourceRepositoryURI", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
			Tenant:       "baz",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://baz.ghe.com/foo/bar", c.Certificate.SourceRepositoryURI)
	})

	t.Run("使用 opts.Repo 设置 Extensions.SourceRepositoryURI", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://github.com/foo/bar", c.Certificate.SourceRepositoryURI)
	})

	t.Run("使用 SANRegex 和 SAN 设置 SANRegex 和 SAN，并使用 opts.Repo 设置 Extensions.SourceRepositoryURI", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "baz",
			Repo:         "baz/xyz",
			SAN:          "https://github/foo/bar/.github/workflows/attest.yml",
			SANRegex:     "(?i)^https://github/foo",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://github/foo/bar/.github/workflows/attest.yml", c.SAN)
		require.Equal(t, "(?i)^https://github/foo", c.SANRegex)
		require.Equal(t, "https://github.com/baz/xyz", c.Certificate.SourceRepositoryURI)
	})

	t.Run("使用 opts.Owner 和 opts.Tenant 设置 Extensions.SourceRepositoryOwnerURI", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
			Tenant:       "baz",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://baz.ghe.com/foo", c.Certificate.SourceRepositoryOwnerURI)
	})

	t.Run("使用 opts.Owner 设置 Extensions.SourceRepositoryOwnerURI", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://github.com/foo", c.Certificate.SourceRepositoryOwnerURI)
	})

	t.Run("使用 opts.Tenant 设置 OIDCIssuer", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
			Tenant:       "baz",
			OIDCIssuer:   verification.GitHubOIDCIssuer,
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://token.actions.baz.ghe.com", c.Certificate.Issuer)
	})

	t.Run("使用 opts.OIDCIssuer 设置 OIDCIssuer", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "foo",
			Repo:         "foo/bar",
			OIDCIssuer:   "https://foo.com",
			Tenant:       "baz",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "https://foo.com", c.Certificate.Issuer)
	})

	t.Run("使用 opts.SignerDigest 设置 Certificate.BuildSignerDigest", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "wrong",
			Repo:         "wrong/value",
			SignerDigest: "foo",
			Hostname:     "github.com",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "foo", c.Certificate.BuildSignerDigest)
	})

	t.Run("使用 opts.SourceDigest 设置 Certificate.SourceRepositoryDigest", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "wrong",
			Repo:         "wrong/value",
			SourceDigest: "foo",
			Hostname:     "github.com",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "foo", c.Certificate.SourceRepositoryDigest)
	})

	t.Run("使用 opts.SourceRef 设置 Certificate.SourceRepositoryRef", func(t *testing.T) {
		opts := &Options{
			ArtifactPath: artifactPath,
			Owner:        "wrong",
			Repo:         "wrong/value",
			SourceRef:    "refs/heads/main",
			Hostname:     "github.com",
		}

		c, err := newEnforcementCriteria(opts)
		require.NoError(t, err)
		require.Equal(t, "refs/heads/main", c.Certificate.SourceRepositoryRef)
	})
}

func TestValidateSignerWorkflow(t *testing.T) {
	type testcase struct {
		name                   string
		providedSignerWorkflow string
		expectedWorkflowRegex  string
		host                   string
		expectErr              bool
		errContains            string
	}

	testcases := []testcase{
		{
			name:                   "未指定主机(host)的工作流(workflow)",
			providedSignerWorkflow: "github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectErr:              true,
			errContains:            "未知的签名者工作流主机",
		},
		{
			name:                   "使用默认主机(host)的工作流(workflow)",
			providedSignerWorkflow: "github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectedWorkflowRegex:  `^https://github\.com/github/artifact-attestations-workflows/\.github/workflows/attest\.yml(@(refs/.*|[0-9a-fA-F]+))?$`,
			host:                   "github.com",
		},
		{
			name:                   "包含工作流(workflow) URL 的工作流(workflow)",
			providedSignerWorkflow: "github.com/github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectedWorkflowRegex:  `^https://github\.com/github/artifact-attestations-workflows/\.github/workflows/attest\.yml(@(refs/.*|[0-9a-fA-F]+))?$`,
			host:                   "github.com",
		},
		{
			name:                   "设置了 GH_HOST 的工作流(workflow)",
			providedSignerWorkflow: "github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectedWorkflowRegex:  `^https://myhost\.github\.com/github/artifact-attestations-workflows/\.github/workflows/attest\.yml(@(refs/.*|[0-9a-fA-F]+))?$`,
			host:                   "myhost.github.com",
		},
		{
			name:                   "使用已认证主机(host)的工作流(workflow)",
			providedSignerWorkflow: "github/artifact-attestations-workflows/.github/workflows/attest.yml",
			expectedWorkflowRegex:  `^https://authedhost\.github\.com/github/artifact-attestations-workflows/\.github/workflows/attest\.yml(@(refs/.*|[0-9a-fA-F]+))?$`,
			host:                   "authedhost.github.com",
		},
	}

	for _, tc := range testcases {
		// 所有主机(host)解析都在 verify.go:RunE 中完成
		workflowRegex, err := validateSignerWorkflow(tc.host, tc.providedSignerWorkflow)
		require.Equal(t, tc.expectedWorkflowRegex, workflowRegex)

		if tc.expectErr {
			require.Error(t, err)
			require.ErrorContains(t, err, tc.errContains)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.expectedWorkflowRegex, workflowRegex)
		}
	}
}

// TestSignerWorkflowSANMatching 将 validateSignerWorkflow 构建的模式(pattern)
// 通过使用它的 sigstore-go 匹配器(matcher)进行驱动验证，因为该匹配器(matcher)
// 使用 regexp.MatchString 应用该模式(pattern)，不会替调用方进行锚定。
func TestSignerWorkflowSANMatching(t *testing.T) {
	const pinnedWorkflow = "owner/builder/.github/workflows/release.yml"
	const workflowDir = "https://github.com/owner/builder/.github/workflows/"

	testcases := []struct {
		name        string
		san         string
		expectMatch bool
	}{
		{
			name:        "在分支(branch)引用(ref)上固定(pin)的工作流(workflow)",
			san:         workflowDir + "release.yml@refs/heads/main",
			expectMatch: true,
		},
		{
			name:        "在提交(commit) SHA 上固定(pin)的工作流(workflow)",
			san:         workflowDir + "release.yml@09b495c3f12c7881b3cc17209a327792065c1a1d",
			expectMatch: true,
		},
		{
			name:        "在包含 @ 的标签(tag)上固定(pin)的工作流(workflow)",
			san:         workflowDir + "release.yml@refs/tags/pkg@1.2.3",
			expectMatch: true,
		},
		{
			name:        "在包含 @ 的分支(branch)上固定(pin)的工作流(workflow)",
			san:         workflowDir + "release.yml@refs/heads/feature@v2",
			expectMatch: true,
		},
		{
			name:        "未带引用(ref)的固定(pin)工作流(workflow)",
			san:         workflowDir + "release.yml",
			expectMatch: true,
		},
		{
			name:        "文件名扩展了被固定(pin)名称的工作流(workflow)",
			san:         workflowDir + "release.yml.attacker.yml@refs/heads/attacker",
			expectMatch: false,
		},
		{
			name:        "文件名包含引用(ref)分隔符的工作流(workflow)",
			san:         workflowDir + "release.yml@attacker.yml@refs/heads/attacker",
			expectMatch: false,
		},
		{
			name:        "被固定(pin)仓库(repository)中的另一个工作流(workflow)",
			san:         workflowDir + "other.yml@refs/heads/main",
			expectMatch: false,
		},
		{
			name:        "外观相似仓库(repository)中的被固定(pin)工作流(workflow)",
			san:         "https://github.com/owner/builder-attacker/.github/workflows/release.yml@refs/heads/main",
			expectMatch: false,
		},
	}

	sanRegex, err := validateSignerWorkflow("github.com", pinnedWorkflow)
	require.NoError(t, err)

	matcher, err := verify.NewSANMatcher("", sanRegex)
	require.NoError(t, err)

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			err := matcher.Verify(certificate.Summary{SubjectAlternativeName: tc.san})
			if tc.expectMatch {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestSignerWorkflowSANMatchingObservedCertificate 使用 Fulcio 实际为存储在
// .github/workflows/provenance-image.yml@evil.yml 的工作流签发的证书(certificate)中的
// SubjectAlternativeName。Actions 会加载名称包含 "@" 的工作流文件，且 OIDC 声明(claim)
// 和 Fulcio 都不会对其进行转义，因此该分隔符确实可能在 SAN 中出现多次。
func TestSignerWorkflowSANMatchingObservedCertificate(t *testing.T) {
	const observedSAN = "https://github.com/bdehamer/attest-demo/.github/workflows/provenance-image.yml@evil.yml@refs/heads/main"

	sanRegex, err := validateSignerWorkflow("github.com", "bdehamer/attest-demo/.github/workflows/provenance-image.yml")
	require.NoError(t, err)

	matcher, err := verify.NewSANMatcher("", sanRegex)
	require.NoError(t, err)

	require.Error(t, matcher.Verify(certificate.Summary{SubjectAlternativeName: observedSAN}))
}

// TestSignerWorkflowSANMatchingPinnedRef 覆盖同时固定(pin)引用(ref)和工作流(workflow)的
// --signer-workflow 值。这样的值必须与 SAN 的结尾精确匹配，因为 git 引用(ref)本身
// 可能被命名为以被固定(pin)的引用(ref)开头、后跟某种引用(ref)形态的内容。
func TestSignerWorkflowSANMatchingPinnedRef(t *testing.T) {
	const pinnedWorkflow = "owner/builder/.github/workflows/release.yml@refs/heads/main"
	const workflowURL = "https://github.com/owner/builder/.github/workflows/release.yml"

	testcases := []struct {
		name        string
		san         string
		expectMatch bool
	}{
		{
			name:        "被固定(pin)的引用(ref)",
			san:         workflowURL + "@refs/heads/main",
			expectMatch: true,
		},
		{
			name:        "名称扩展了被固定(pin)引用(ref)的分支(branch)",
			san:         workflowURL + "@refs/heads/mainattacker",
			expectMatch: false,
		},
		{
			name:        "命名使得第二个引用(ref)跟随被固定(pin)引用(ref)的分支(branch)",
			san:         workflowURL + "@refs/heads/main@refs/heads/attacker",
			expectMatch: false,
		},
		{
			name:        "命名使得对象 ID 跟随被固定(pin)引用(ref)的分支(branch)",
			san:         workflowURL + "@refs/heads/main@09b495c3f12c7881b3cc17209a327792065c1a1d",
			expectMatch: false,
		},
		{
			name:        "另一个引用(ref)",
			san:         workflowURL + "@refs/heads/attacker",
			expectMatch: false,
		},
	}

	sanRegex, err := validateSignerWorkflow("github.com", pinnedWorkflow)
	require.NoError(t, err)

	matcher, err := verify.NewSANMatcher("", sanRegex)
	require.NoError(t, err)

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			err := matcher.Verify(certificate.Summary{SubjectAlternativeName: tc.san})
			if tc.expectMatch {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestSignerWorkflowSANMatchingObservedNestedRef 使用从在名为 "main@refs/heads/evil" 的
// 分支(branch)上运行的工作流中观察到的 job_workflow_ref 声明(claim)。Actions 会从这样的
// 分支(branch)运行工作流(workflow)，因此对 refs/heads/main 的固定(pin)不得接受它。
func TestSignerWorkflowSANMatchingObservedNestedRef(t *testing.T) {
	const observedSAN = "https://github.com/bdehamer/attest-demo/.github/workflows/control-probe.yml@refs/heads/main@refs/heads/evil"

	sanRegex, err := validateSignerWorkflow("github.com", "bdehamer/attest-demo/.github/workflows/control-probe.yml@refs/heads/main")
	require.NoError(t, err)

	matcher, err := verify.NewSANMatcher("", sanRegex)
	require.NoError(t, err)

	require.Error(t, matcher.Verify(certificate.Summary{SubjectAlternativeName: observedSAN}))
}

// TestSignerWorkflowSANMatchingRepositoryValue 覆盖指定仓库(repository)而非工作流(workflow)
// 的 --signer-workflow 值。该值仍会作为完整身份(identity)进行匹配，因此它不能匹配
// 名称仅以其开头的仓库(repository)。
func TestSignerWorkflowSANMatchingRepositoryValue(t *testing.T) {
	sanRegex, err := validateSignerWorkflow("github.com", "owner/builder")
	require.NoError(t, err)

	matcher, err := verify.NewSANMatcher("", sanRegex)
	require.NoError(t, err)

	san := "https://github.com/owner/builder-attacker/.github/workflows/release.yml@refs/heads/main"
	require.Error(t, matcher.Verify(certificate.Summary{SubjectAlternativeName: san}))
}
