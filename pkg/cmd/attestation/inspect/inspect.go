package inspect

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/auth"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/digitorus/timestamp"
	in_toto "github.com/in-toto/attestation/go/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"
)

func NewInspectCmd(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{}
	inspectCmd := &cobra.Command{
		Use:    "inspect <path-to-sigstore-bundle>",
		Args:   cmdutil.ExactArgs(1, "必须指定 bundle 文件路径"),
		Hidden: true,
		Short:  "检查(inspect)一个 Sigstore bundle",
		Long: heredoc.Docf(`
			检查(inspect)已下载到磁盘的 Sigstore bundle。要下载与制品(artifact)关联的 bundle，
			请参见 %[1]sgh at download%[1]s 命令。

			给定一个 .json 或 .jsonl 文件，该命令将:
			- 提取 bundle 的语句(statement)和谓词(predicate)
			- 提供证书(certificate)摘要(summary)(如果存在)，并指明该证书是由 GitHub 还是
			  由 Sigstore 的公共善意实例(Public Good Instance, PGI)签发
			- 检查 bundle 的“真实性(authenticity)”

			就我们的目的而言，如果我们拥有验证所含证书(certificate)、透明日志(transparency log)条目
			和签名时间戳所需的受信任材料，且所含签名与提供的公钥匹配，则该 bundle 是真实的(authentic)。

			该命令不能用于验证(verify) bundle。要验证 bundle，请参见 %[1]sgh at verify%[1]s 命令。

			默认情况下，该命令打印一个精简表格。要查看完整结果，请提供 %[1]s--format=json%[1]s 标志(flag)。
		`, "`"),
		Example: heredoc.Doc(`
			# 检查(inspect)一个 Sigstore bundle 并以表格格式打印结果
			$ gh attestation inspect <path-to-bundle>

			# 检查(inspect)一个 Sigstore bundle 并以 JSON 格式打印结果
			$ gh attestation inspect <path-to-bundle> --format=json
		`),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// 创建整个 inspect 命令通用的日志记录器(logger)
			opts.Logger = io.NewHandler(f.IOStreams)

			// 设置 bundle 路径
			opts.BundlePath = args[0]

			// Clean file path options
			opts.Clean()

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// handle tenancy
			if opts.Hostname == "" {
				opts.Hostname, _ = ghauth.DefaultHost()
			}

			if err := auth.IsHostSupported(opts.Hostname); err != nil {
				return err
			}

			hc, err := f.HttpClient()
			if err != nil {
				return err
			}

			externalClient, err := f.ExternalHttpClient()
			if err != nil {
				return err
			}

			config := verification.SigstoreConfig{
				ExternalHttpClient: externalClient,
				Logger:             opts.Logger,
			}

			if ghauth.IsTenancy(opts.Hostname) {
				apiClient := api.NewLiveClient(hc, externalClient, opts.Hostname, opts.Logger)
				td, err := apiClient.GetTrustDomain()
				if err != nil {
					return fmt.Errorf("获取信任域(trust domain)出错，请确保已对该主机(host)完成身份认证: %w", err)
				}
				_, found := ghinstance.TenantName(opts.Hostname)
				if !found {
					return fmt.Errorf("提供的主机名(hostname)无效: '%s'",
						opts.Hostname)
				}

				config.TrustDomain = td
			}

			sgVerifier, err := verification.NewLiveSigstoreVerifier(config)
			if err != nil {
				return fmt.Errorf("创建 Sigstore 验证器(verifier)失败: %w", err)
			}
			opts.SigstoreVerifier = sgVerifier

			if runF != nil {
				return runF(opts)
			}

			if err := runInspect(opts); err != nil {
				return fmt.Errorf("检查(inspect)制品(artifact)和 bundle 失败: %w", err)
			}
			return nil
		},
	}

	inspectCmd.Flags().StringVarP(&opts.Hostname, "hostname", "", "", "配置要使用的主机(host)")
	cmdutil.AddFormatFlags(inspectCmd, &opts.exporter)

	return inspectCmd
}

type BundleInspectResult struct {
	InspectedBundles []BundleInspection `json:"inspectedBundles"`
}

type BundleInspection struct {
	Authentic              bool                  `json:"authentic"`
	Certificate            CertificateInspection `json:"certificate"`
	TransparencyLogEntries []TlogEntryInspection `json:"transparencyLogEntries"`
	SignedTimestamps       []time.Time           `json:"signedTimestamps"`
	Statement              *in_toto.Statement    `json:"statement"`
}

type CertificateInspection struct {
	certificate.Summary
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
}

type TlogEntryInspection struct {
	IntegratedTime time.Time
	LogID          string
}

func runInspect(opts *Options) error {
	attestations, err := verification.GetLocalAttestations(opts.BundlePath)
	if err != nil {
		return fmt.Errorf("读取证明(attestation)失败")
	}

	inspectedBundles := []BundleInspection{}
	unsafeSigstorePolicy := verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithoutIdentitiesUnsafe())

	for _, a := range attestations {
		inspectedBundle := BundleInspection{}

		// 我们舍弃 verificationResult，以避免哪怕只是暗示该 bundle “已验证”
		// 使用这种 Unsafe 策略时，你无法有意义地“验证”一个 bundle！
		_, err := opts.SigstoreVerifier.Verify([]*api.Attestation{a}, unsafeSigstorePolicy)

		// 供后续迭代思考:
		// 如果存在 err，我们继续执行，因为我们希望能够检查(inspect)那些
		// 我们可能没有其受信任材料的 bundle。
		// 但也许我们应该打印该错误？
		if err == nil {
			inspectedBundle.Authentic = true
		}

		entity := a.Bundle
		verificationContent, err := entity.VerificationContent()
		if err != nil {
			return fmt.Errorf("获取验证内容(verification content)失败: %w", err)
		}

		// 如果存在证书(certificate)，则生成其摘要(summary)
		if leafCert := verificationContent.Certificate(); leafCert != nil {

			certSummary, err := certificate.SummarizeCertificate(leafCert)
			if err != nil {
				return fmt.Errorf("生成证书(certificate)摘要(summary)失败: %w", err)
			}

			inspectedBundle.Certificate = CertificateInspection{
				Summary:   certSummary,
				NotBefore: leafCert.NotBefore,
				NotAfter:  leafCert.NotAfter,
			}

		}

		// 解析签名内容并取出语句(statement)
		sigContent, err := entity.SignatureContent()
		if err != nil {
			return fmt.Errorf("获取签名内容(signature content)失败: %w", err)
		}

		if envelope := sigContent.EnvelopeContent(); envelope != nil {
			stmt, err := envelope.Statement()
			if err != nil {
				return fmt.Errorf("获取信封(envelope)语句(statement)失败: %w", err)
			}

			inspectedBundle.Statement = stmt
		}

		// 获取观察者时间戳(observer timestamps)
		tlogTimestamps, err := dumpTlogs(entity)
		if err != nil {
			return fmt.Errorf("导出透明日志(tlog)失败: %w", err)
		}
		inspectedBundle.TransparencyLogEntries = tlogTimestamps

		signedTimestamps, err := dumpSignedTimestamps(entity)
		if err != nil {
			return fmt.Errorf("导出 TSA 时间戳失败: %w", err)
		}
		inspectedBundle.SignedTimestamps = signedTimestamps

		inspectedBundles = append(inspectedBundles, inspectedBundle)
	}

	inspectionResult := BundleInspectResult{InspectedBundles: inspectedBundles}

	// 如果用户提供了 --format=json 标志(flag)，则以 JSON 格式打印结果
	if opts.exporter != nil {
		if err = opts.exporter.Write(opts.Logger.IO, inspectionResult); err != nil {
			return fmt.Errorf("写入 JSON 输出失败")
		}
		return nil
	}

	printInspectionSummary(opts.Logger, inspectionResult.InspectedBundles)

	return nil
}

func printInspectionSummary(logger *io.Handler, bundles []BundleInspection) {
	logger.Printf("正在检查(inspect) bundle…\n")
	logger.Printf("找到 %s:\n---\n", text.Pluralize(len(bundles), "attestation"))

	bundleSummaries := make([][][]string, len(bundles))
	for i, iB := range bundles {
		bundleSummaries[i] = [][]string{
			{"Authentic", formatAuthentic(iB.Authentic, iB.Certificate.CertificateIssuer)},
			{"Source Repo", formatNwo(iB.Certificate.SourceRepositoryURI)},
			{"PredicateType", iB.Statement.GetPredicateType()},
			{"SubjectAlternativeName", iB.Certificate.SubjectAlternativeName},
			{"RunInvocationURI", iB.Certificate.RunInvocationURI},
			{"CertificateNotBefore", iB.Certificate.NotBefore.Format(time.RFC3339)},
		}
	}

	// "SubjectAlternativeName" 有 22 个字符
	maxNameLength := 22

	scheme := logger.ColorScheme
	for i, bundle := range bundleSummaries {
		for _, pair := range bundle {
			colName := pair[0]
			dots := maxNameLength - len(colName)
			logger.OutPrintf("%s:%s %s\n", scheme.Bold(colName), strings.Repeat(".", dots), pair[1])
		}
		if i < len(bundleSummaries)-1 {
			logger.OutPrintln("---")
		}
	}
}

func formatNwo(longUrl string) string {
	repo, err := ghrepo.FromFullName(longUrl)
	if err != nil {
		return longUrl
	}

	return ghrepo.FullName(repo)
}

func formatAuthentic(authentic bool, certIssuer string) string {
	if strings.HasSuffix(certIssuer, "O=GitHub\\, Inc.") {
		certIssuer = "(GitHub)"
	} else if strings.HasSuffix(certIssuer, "O=sigstore.dev") {
		certIssuer = "(Sigstore PGI)"
	} else {
		certIssuer = "(未知)"
	}

	return strconv.FormatBool(authentic) + " " + certIssuer
}

func dumpTlogs(entity *bundle.Bundle) ([]TlogEntryInspection, error) {
	inspectedTlogEntries := []TlogEntryInspection{}

	entries, err := entity.TlogEntries()
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		inspectedEntry := TlogEntryInspection{
			IntegratedTime: entry.IntegratedTime(),
			LogID:          entry.LogKeyID(),
		}

		inspectedTlogEntries = append(inspectedTlogEntries, inspectedEntry)
	}

	return inspectedTlogEntries, nil
}

func dumpSignedTimestamps(entity *bundle.Bundle) ([]time.Time, error) {
	timestamps := []time.Time{}

	signedTimestamps, err := entity.Timestamps()
	if err != nil {
		return nil, err
	}

	for _, signedTsBytes := range signedTimestamps {
		tsaTime, err := timestamp.ParseResponse(signedTsBytes)

		if err != nil {
			return nil, err
		}

		timestamps = append(timestamps, tsaTime.Time)
	}

	return timestamps, nil
}
