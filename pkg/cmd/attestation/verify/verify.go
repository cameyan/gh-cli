package verify

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/auth"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"

	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"
)

func NewVerifyCmd(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{}
	verifyCmd := &cobra.Command{
		Use:   "verify [<file-path> | oci://<image-uri>] [--owner | --repo]",
		Args:  cmdutil.ExactArgs(1, "必须指定文件路径或容器镜像 URI，以及 --owner 或 --repo 之一"),
		Short: "使用证明验证制品的完整性",
		Long: heredoc.Docf(`
			使用与制品关联的密码学签名证明(attestation)验证其完整性与来源(provenance)。

			## 理解验证

			证明是由行为者(即 GitHub Actions 工作流)就主体(即制品)做出的声明(即来源(provenance)语句)。

			要验证一条证明，您必须提供一个制品并验证:
			* 生成该证明的行为者的身份
			* 期望的证明断言类型(即声明的性质)

			默认情况下，该命令执行 %[1]s%[2]s%[1]s 断言类型。要验证其他证明断言类型，请使用 %[1]s--predicate-type%[1]s 标志(flag)。

			“行为者身份”由以下部分组成:
			* 与制品关联的仓库或仓库所有者
			* 生成该证明的 Actions 工作流(即签名者工作流)

			随后，该身份将针对证明证书的 SourceRepository、SourceRepositoryOwner 和 SubjectAlternativeName
			(SAN)等字段进行验证。

			由您自行决定要以多高的精度执行该身份验证。

			至少，该命令需要以下二者之一:
			* %[1]s--owner%[1]s 标志(flag)(例如 --owner github)，或
			* %[1]s--repo%[1]s 标志(flag)(例如 --repo github/example)

			您指定的身份越精确，您对验证过程所提供的安全保证的控制力就越强。

			理想情况下，还应使用 %[1]s--signer-workflow%[1]s 或 %[1]s--cert-identity%[1]s
			标志验证签名者工作流的路径。

			请注意: 如果您的证明是通过可复用工作流生成的，那么该可复用工作流就是需要验证其身份的签名者。
			在这种情况下，您必须使用 %[1]s--signer-workflow%[1]s 或 %[1]s--signer-repo%[1]s 标志。

			更多选项，请参见其他可用标志。

			## 加载制品与证明

			要指定制品，该命令需要:
			* 指向制品的文件路径，或
			* 容器镜像 URI(例如 %[1]soci://<image-uri>%[1]s)
			  * (注意，如果提供 OCI URL，必须已通过其容器镜像仓库的身份认证)

			默认情况下，该命令将尝试通过 GitHub API 获取相关证明，使用提供给 %[1]s--owner%[1]s 或 %[1]s--repo%[1]s 的值。

			若要改为从制品的 OCI 镜像仓库获取证明，请使用 %[1]s--bundle-from-oci%[1]s 标志。

			若要使用存储在磁盘上的证明进行离线验证(参见 download 命令)，请为 %[1]s--bundle%[1]s 标志提供一个路径。

			When you load attestations with %[1]s--bundle%[1]s or %[1]s--bundle-from-oci%[1]s, this command
			compares the %[1]s--repo%[1]s or %[1]s--owner%[1]s value only with the repository and owner
			names recorded in the attestation's certificate when the attestation was
			created. It cannot detect whether the repository or owner has since been
			renamed, or whether a different repository or owner now uses that name. To
			confirm that an attestation belongs to the repository or owner that currently
			has the name, omit %[1]s--bundle%[1]s and %[1]s--bundle-from-oci%[1]s so that this command
			fetches attestations from the GitHub API.

			## Additional Policy Enforcement

			给定 %[1]s--format=json%[1]s 标志，验证成功后该命令将输出一个 JSON 数组，其中每条已验证的证明对应一个条目。

			该输出随后可用于其他策略执行，例如将其通过管道传输到策略引擎。

			数组中的每个对象包含两个属性:
			* 一个 %[1]sattestation%[1]s 对象，包含已验证的签名包
			* 一个 %[1]sverificationResult%[1]s 对象，是已验证签名包内容的解析表示

			在 %[1]sverificationResult%[1]s 对象中，您将看到:
			* %[1]ssignature.certificate%[1]s，是证明中内嵌的 X.509 证书的解析表示
			* %[1]sverifiedTimestamps%[1]s，是一个对象数组，表示证明被透明日志或时间戳权威机构见证的时间
			* %[1]sstatement%[1]s，包含引用制品的 %[1]ssubject%[1]s 数组、%[1]spredicateType%[1]s 字段，
			  以及包含附加的、通常由用户控制的元数据的 %[1]spredicate%[1]s 对象

			重要提示: 请注意，只有 %[1]ssignature.certificate%[1]s 和 %[1]sverifiedTimestamps%[1]s
			属性包含无法被生成该证明的工作流操纵的值。

			处理在 GitHub Actions 中创建的证明时，%[1]ssignature.certificate%[1]s 的内容直接来自 GitHub
			生成的 OpenID Connect 令牌。%[1]sverifiedTimestamps%[1]s 数组的内容来自透明日志或时间戳权威机
			构的签名时间戳 – 同样无法被用户伪造。

			使用该输出设计策略执行时，在检查 %[1]sstatement.predicate%[1]s 属性的内容时必须特别小心:
			如果攻击者获得了您工作流执行上下文的访问权限，他们就可能伪造 %[1]sstatement.predicate%[1]s 的内容。

			为缓解该攻击向量，请考虑使用“受信任的构建器”: 在生成制品时，让构建和证明签名在一个可复用工作流内进行，
			其执行不受调用方工作流所提供输入的影响。

			关于 %[1]s--signer-workflow%[1]s，请参见上文。
			`, "`", verification.SLSAPredicateV1),
		Example: heredoc.Doc(`
			# 验证与仓库关联的制品
			$ gh attestation verify example.bin --repo github/example

			# 验证与组织关联的制品
			$ gh attestation verify example.bin --owner github

			# 验证制品并输出完整验证结果
			$ gh attestation verify example.bin --owner github --format json

			# 使用存储在磁盘上的证明验证 OCI 镜像
			$ gh attestation verify oci://<image-uri> --owner github --bundle sha256:foo.jsonl

			# 验证由可复用工作流签名的制品
			$ gh attestation verify example.bin --owner github --signer-repo actions/example
		`),
		// PreRunE 用于在命令运行前校验标志(flag)
		// 如果返回错误，其消息将连同命令使用说明一起打印到终端
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// 创建整个 verify 命令通用的日志记录器(logger)
			opts.Logger = io.NewHandler(f.IOStreams)

			// 设置制品(artifact)路径
			opts.ArtifactPath = args[0]

			// 检查给定的标志(flag)组合是否有效
			if err := opts.AreFlagsValid(); err != nil {
				return err
			}

			// 清理文件路径选项值
			opts.Clean()

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			hc, err := f.HttpClient()
			if err != nil {
				return err
			}

			externalClient, err := f.ExternalHttpClient()
			if err != nil {
				return err
			}

			opts.OCIClient = oci.NewLiveClient()

			if opts.Hostname == "" {
				opts.Hostname, _ = ghauth.DefaultHost()
			}
			err = auth.IsHostSupported(opts.Hostname)
			if err != nil {
				return err
			}

			opts.APIClient = api.NewLiveClient(hc, externalClient, opts.Hostname, opts.Logger)

			config := verification.SigstoreConfig{
				ExternalHttpClient: externalClient,
				Logger:             opts.Logger,
				NoPublicGood:       opts.NoPublicGood,
				TrustedRoot:        opts.TrustedRoot,
			}

			// 检测到租户(tenancy)时进行相应准备
			if ghauth.IsTenancy(opts.Hostname) {
				td, err := opts.APIClient.GetTrustDomain()
				if err != nil {
					return fmt.Errorf("获取信任域出错，请确保已对该主机完成身份认证: %w", err)
				}

				tenant, found := ghinstance.TenantName(opts.Hostname)
				if !found {
					return fmt.Errorf("提供的主机名无效: '%s'",
						opts.Hostname)
				}
				config.TrustDomain = td
				opts.Tenant = tenant
			}

			if runF != nil {
				return runF(opts)
			}

			sigstoreVerifier, err := verification.NewLiveSigstoreVerifier(config)
			if err != nil {
				return fmt.Errorf("创建 Sigstore 验证器出错: %w", err)
			}
			opts.SigstoreVerifier = sigstoreVerifier
			opts.Config = f.Config

			if err := runVerify(opts); err != nil {
				return fmt.Errorf("\n错误: %v", err)
			}
			return nil
		},
	}

	// 通用标志(flag)
	verifyCmd.Flags().StringVarP(&opts.BundlePath, "bundle", "b", "", "磁盘上签名包的路径，可以是包含单个签名包的 JSON 文件，也可以是包含多个签名包的 JSON lines 文件")
	cmdutil.DisableAuthCheckFlag(verifyCmd.Flags().Lookup("bundle"))
	verifyCmd.Flags().BoolVarP(&opts.UseBundleFromRegistry, "bundle-from-oci", "", false, "验证 OCI 镜像时，从 OCI 镜像仓库而非 GitHub 获取证明的签名包")
	cmdutil.StringEnumFlag(verifyCmd, &opts.DigestAlgorithm, "digest-alg", "d", "sha256", []string{"sha256", "sha512"}, "用于计算制品摘要的算法")
	verifyCmd.Flags().StringVarP(&opts.Owner, "owner", "o", "", "用于限定证明查询范围的 GitHub 组织")
	verifyCmd.Flags().StringVarP(&opts.Repo, "repo", "R", "", "仓库名称，格式为 <owner>/<repo>")
	verifyCmd.MarkFlagsMutuallyExclusive("owner", "repo")
	verifyCmd.MarkFlagsOneRequired("owner", "repo")
	verifyCmd.Flags().BoolVarP(&opts.NoPublicGood, "no-public-good", "", false, "不验证由 Sigstore 公共服务实例(Public Good Instance)签名的证明")
	verifyCmd.Flags().StringVarP(&opts.TrustedRoot, "custom-trusted-root", "", "", "trusted_root.jsonl 文件的路径；通常用于离线验证")
	verifyCmd.Flags().IntVarP(&opts.Limit, "limit", "L", api.DefaultLimit, "最多获取的证明数量")
	cmdutil.AddFormatFlags(verifyCmd, &opts.exporter)
	verifyCmd.Flags().StringVarP(&opts.Hostname, "hostname", "", "", "配置要使用的主机")
	// 策略执行(policy enforcement)标志(flag)
	verifyCmd.Flags().StringVarP(&opts.PredicateType, "predicate-type", "", verification.SLSAPredicateV1, "强制要求已验证证明的断言类型与提供的值匹配")
	verifyCmd.Flags().BoolVarP(&opts.DenySelfHostedRunner, "deny-self-hosted-runners", "", false, "对由自托管运行器生成的证明判定验证失败")
	verifyCmd.Flags().StringVarP(&opts.SAN, "cert-identity", "", "", "强制要求证书的 SubjectAlternativeName 与提供的值完全匹配")
	verifyCmd.Flags().StringVarP(&opts.SANRegex, "cert-identity-regex", "i", "", "强制要求证书的 SubjectAlternativeName 与提供的正则匹配")
	verifyCmd.Flags().StringVarP(&opts.SignerRepo, "signer-repo", "", "", "强制要求签署证明的工作流所在仓库与提供的值(<owner>/<repo>)匹配")
	verifyCmd.Flags().StringVarP(&opts.SignerWorkflow, "signer-workflow", "", "", "强制要求签署证明的工作流与提供的值([host/]<owner>/<repo>/<path>/<to>/<workflow>)匹配")
	verifyCmd.MarkFlagsMutuallyExclusive("cert-identity", "cert-identity-regex", "signer-repo", "signer-workflow")
	verifyCmd.Flags().StringVarP(&opts.OIDCIssuer, "cert-oidc-issuer", "", verification.GitHubOIDCIssuer, "强制要求 OIDC 令牌的签发者与提供的值匹配")
	verifyCmd.Flags().StringVarP(&opts.SignerDigest, "signer-digest", "", "", "强制要求与签名者工作流关联的摘要与提供的值匹配")
	verifyCmd.Flags().StringVarP(&opts.SourceRef, "source-ref", "", "", "强制要求与源仓库关联的 git 引用与提供的值匹配")
	verifyCmd.Flags().StringVarP(&opts.SourceDigest, "source-digest", "", "", "强制要求与源仓库关联的摘要与提供的值匹配")

	return verifyCmd
}

func runVerify(opts *Options) error {
	ec, err := newEnforcementCriteria(opts)
	if err != nil {
		opts.Logger.Println(opts.Logger.ColorScheme.Red("✗ 构建验证策略失败"))
		return err
	}

	if err := ec.Valid(); err != nil {
		opts.Logger.Println(opts.Logger.ColorScheme.Red("✗ 验证策略无效"))
		return err
	}

	artifact, err := artifact.NewDigestedArtifact(opts.OCIClient, opts.ArtifactPath, opts.DigestAlgorithm)
	if err != nil {
		opts.Logger.Printf(opts.Logger.ColorScheme.Red("✗ 加载 %s 的摘要失败\n"), opts.ArtifactPath)
		return err
	}

	opts.Logger.Printf("已加载 %s 的摘要 %s\n", artifact.URL, artifact.DigestWithAlg())

	attestations, logMsg, err := getAttestations(opts, *artifact)
	if err != nil {
		if ok := errors.Is(err, api.ErrNoAttestationsFound); ok {
			opts.Logger.Printf(opts.Logger.ColorScheme.Red("✗ 未找到主题 %s 的证明\n"), artifact.DigestWithAlg())
			return err
		}
		// 打印表示获取证明(attestation)失败的消息
		opts.Logger.Println(opts.Logger.ColorScheme.Red(logMsg))
		return err
	}
	// 打印表示成功获取证明(attestation)的消息
	opts.Logger.Println(logMsg)

	// 打印将针对证明(attestation)执行的策略的相关信息
	opts.Logger.Println("\n将执行以下策略条件:")
	opts.Logger.Println(ec.BuildPolicyInformation())

	verified, errMsg, err := verifyAttestations(*artifact, attestations, opts.SigstoreVerifier, ec)
	if err != nil {
		opts.Logger.Println(opts.Logger.ColorScheme.Red(errMsg))
		return err
	}

	opts.Logger.Println(opts.Logger.ColorScheme.Green("✓ 验证成功！\n"))

	// 如果通过 --json 标志(flag)提供了导出器(exporter)，则以 JSON 格式将结果写入终端
	if opts.exporter != nil {
		// 以 JSON 对象数组的形式将结果打印到终端
		if err = opts.exporter.Write(opts.Logger.IO, verified); err != nil {
			opts.Logger.Println(opts.Logger.ColorScheme.Red("✗ 写入 JSON 输出失败"))
			return err
		}
		return nil
	}

	opts.Logger.Printf("以下 %s 符合策略条件\n\n", text.Pluralize(len(verified), "attestation"))

	// 否则将结果打印到终端
	for i, v := range verified {
		buildConfigURI := v.VerificationResult.Signature.Certificate.Extensions.BuildConfigURI
		sourceRepoAndOrg, sourceWorkflow, err := extractAttestationDetail(opts.Tenant, buildConfigURI)
		if err != nil {
			opts.Logger.Println(opts.Logger.ColorScheme.Red("解析构建配置 URI 失败"))
			return err
		}
		builderSignerURI := v.VerificationResult.Signature.Certificate.Extensions.BuildSignerURI
		signerRepoAndOrg, signerWorkflow, err := extractAttestationDetail(opts.Tenant, builderSignerURI)
		if err != nil {
			opts.Logger.Println(opts.Logger.ColorScheme.Red("解析构建签名者 URI 失败"))
			return err
		}

		opts.Logger.Printf("- 证明 #%d\n", i+1)
		rows := [][]string{
			{"  - 构建仓库", sourceRepoAndOrg},
			{"  - 构建工作流", sourceWorkflow},
			{"  - 签名仓库", signerRepoAndOrg},
			{"  - 签名工作流", signerWorkflow},
		}
		//nolint:errcheck
		opts.Logger.PrintBulletPoints(rows)
	}

	// 所有证明(attestation)均通过验证与策略评估
	return nil
}

func extractAttestationDetail(tenant, builderSignerURI string) (string, string, error) {
	// 如果给定一个类似如下格式的构建签名者 URI:
	// https://github.com/foo/bar/.github/workflows/release.yml@refs/heads/main
	// 我们要提取:
	// * foo/bar
	// * .github/workflows/release.yml@refs/heads/main
	var orgAndRepoRegexp *regexp.Regexp
	var workflowRegexp *regexp.Regexp

	if tenant == "" {
		orgAndRepoRegexp = regexp.MustCompile(`https://github\.com/([^/]+/[^/]+)/`)
		workflowRegexp = regexp.MustCompile(`https://github\.com/[^/]+/[^/]+/(.+)`)
	} else {
		var tr = regexp.QuoteMeta(tenant)
		orgAndRepoRegexp = regexp.MustCompile(fmt.Sprintf(
			`https://%s\.ghe\.com/([^/]+/[^/]+)/`,
			tr))
		workflowRegexp = regexp.MustCompile(fmt.Sprintf(
			`https://%s\.ghe\.com/[^/]+/[^/]+/(.+)`,
			tr))
	}

	match := orgAndRepoRegexp.FindStringSubmatch(builderSignerURI)
	if len(match) < 2 {
		return "", "", fmt.Errorf("未找到组织和仓库的匹配项: %s", builderSignerURI)
	}
	orgAndRepo := match[1]

	match = workflowRegexp.FindStringSubmatch(builderSignerURI)
	if len(match) < 2 {
		return "", "", fmt.Errorf("未找到工作流的匹配项: %s", builderSignerURI)
	}
	workflow := match[1]

	return orgAndRepo, workflow, nil
}
