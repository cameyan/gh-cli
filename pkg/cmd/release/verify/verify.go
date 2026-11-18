package verify

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	v1 "github.com/in-toto/attestation/go/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	att_io "github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/iostreams"

	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

type VerifyOptions struct {
	TagName     string
	BaseRepo    ghrepo.Interface
	Exporter    cmdutil.Exporter
	TrustedRoot string
}

type VerifyConfig struct {
	HttpClient  *http.Client
	IO          *iostreams.IOStreams
	Opts        *VerifyOptions
	AttClient   api.Client
	AttVerifier shared.Verifier
}

func NewCmdVerify(f *cmdutil.Factory, runF func(config *VerifyConfig) error) *cobra.Command {
	opts := &VerifyOptions{}

	cmd := &cobra.Command{
		Use:   "verify [<tag>]",
		Short: "校验发布(release)的证明(attestation)",
		Args:  cobra.MaximumNArgs(1),
		Long: heredoc.Doc(`
			校验 GitHub Release 是否附带有效的、经密码学签名的证明(attestation)。

			证明(attestation)是 GitHub 就某个发布(release)及其资产所作的声明。

			该命令检查指定的发布(release)(若未提供 tag，则为最新发布)是否具有有效证明。
			它会获取该发布对应的证明，并打印证明中引用的所有资产(asset)的元数据，包括其摘要(digest)。
		`),
		Example: heredoc.Doc(`
			# 校验最新发布(release)
			gh release verify

			# 按标签(tag)校验指定发布
			gh release verify v1.2.3

			# 按标签校验指定发布，并以 JSON 格式输出证明
			gh release verify v1.2.3 --format json
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.TagName = args[0]
			}

			baseRepo, err := f.BaseRepo()
			if err != nil {
				return fmt.Errorf("确定基础仓库失败: %w", err)
			}

			opts.BaseRepo = baseRepo

			httpClient, err := f.HttpClient()
			if err != nil {
				return err
			}

			externalClient, err := f.ExternalHttpClient()
			if err != nil {
				return err
			}

			io := f.IOStreams
			attClient := api.NewLiveClient(httpClient, externalClient, baseRepo.RepoHost(), att_io.NewHandler(io))

			attVerifier := &shared.AttestationVerifier{
				AttClient:          attClient,
				ExternalHttpClient: externalClient,
				IO:                 io,
				TrustedRoot:        opts.TrustedRoot,
			}

			config := &VerifyConfig{
				Opts:        opts,
				HttpClient:  httpClient,
				AttClient:   attClient,
				AttVerifier: attVerifier,
				IO:          io,
			}

			if runF != nil {
				return runF(config)
			}
			return verifyRun(config)
		},
	}
	cmdutil.AddFormatFlags(cmd, &opts.Exporter)
	cmd.Flags().StringVarP(&opts.TrustedRoot, "custom-trusted-root", "", "", "trusted_root.jsonl 文件的路径; 通常用于离线校验。")
	cmd.Flags().MarkHidden("custom-trusted-root")

	return cmd
}

func verifyRun(config *VerifyConfig) error {
	ctx := context.Background()
	opts := config.Opts
	baseRepo := opts.BaseRepo
	tagName := opts.TagName

	if tagName == "" {
		release, err := shared.FetchLatestRelease(ctx, config.HttpClient, baseRepo)
		if err != nil {
			return err
		}
		tagName = release.TagName
	}

	// 获取发布标签(tag)的 ref
	ref, err := shared.FetchRefSHA(ctx, config.HttpClient, baseRepo, tagName)
	if err != nil {
		return err
	}

	releaseRefDigest := artifact.NewDigestedArtifactForRelease(ref, shared.DigestAlgForRef(ref))

	// 查找发布标签(tag) SHA 对应的全部证明(attestation)
	attestations, err := config.AttClient.GetByDigest(api.FetchParams{
		Digest:        releaseRefDigest.DigestWithAlg(),
		PredicateType: "release",
		Owner:         baseRepo.RepoOwner(),
		Repo:          baseRepo.RepoOwner() + "/" + baseRepo.RepoName(),
		Initiator:     "github",
		// TODO: 允许通过 flag 设置该值。
		// 上限设为 100 以确保获取给定 SHA 的全部证明(attestation)。
		// 虽然一个 SHA 可以对应多个证明，但每个发布标签(tag)只关联一个证明。
		Limit: 100,
	})
	if err != nil {
		return fmt.Errorf("未找到标签(tag) %s (%s) 的证明(attestation)", tagName, releaseRefDigest.DigestWithAlg())
	}

	// 按标签(tag)名过滤证明(attestation)
	filteredAttestations, err := shared.FilterAttestationsByTag(attestations, tagName)
	if err != nil {
		return fmt.Errorf("解析标签(tag) %s 的证明(attestation)出错: %w", tagName, err)
	}

	if len(filteredAttestations) == 0 {
		return fmt.Errorf("在 %s 中未找到发布(release) %s 的证明(attestation)", baseRepo.RepoName(), tagName)
	}

	if len(filteredAttestations) > 1 {
		return fmt.Errorf("在 %s 中发现发布(release) %s 的重复证明(attestation)", baseRepo.RepoName(), tagName)
	}

	// 校验证明(attestation)
	verified, err := config.AttVerifier.VerifyAttestation(releaseRefDigest, filteredAttestations[0])
	if err != nil {
		return fmt.Errorf("校验标签(tag) %s 的证明(attestation)失败: %w", tagName, err)
	}

	// 如果通过 --json flag 提供了导出器(exporter)，则以 JSON 格式将结果写入终端
	if opts.Exporter != nil {
		return opts.Exporter.Write(config.IO, verified)
	}

	io := config.IO
	cs := io.ColorScheme()
	fmt.Fprintf(io.Out, "已将标签(tag) %s 解析为 %s\n", tagName, releaseRefDigest.DigestWithAlg())
	fmt.Fprint(io.Out, "已从 GitHub API 加载证明(attestation)\n")
	fmt.Fprintf(io.Out, cs.Green("%s 发布(release) %s 校验通过!\n"), cs.SuccessIcon(), tagName)
	fmt.Fprintln(io.Out)

	if err := printVerifiedSubjects(io, verified); err != nil {
		return err
	}

	return nil
}

func printVerifiedSubjects(io *iostreams.IOStreams, att *verification.AttestationProcessingResult) error {
	cs := io.ColorScheme()
	w := io.Out

	statement := att.Attestation.Bundle.GetDsseEnvelope().Payload
	var statementData v1.Statement

	err := protojson.Unmarshal([]byte(statement), &statementData)
	if err != nil {
		return err
	}

	// 若 subject 少于两个，则没有可展示的资产(asset)
	if len(statementData.Subject) < 2 {
		return nil
	}

	fmt.Fprintln(w, cs.Bold("资产(asset)"))
	table := tableprinter.New(io, tableprinter.WithHeader("名称", "摘要(digest)"))

	for _, s := range statementData.Subject {
		name := s.Name
		digest := s.Digest

		if name != "" {
			digestStr := ""
			for key, value := range digest {
				digestStr = key + ":" + value
			}

			table.AddField(name)
			table.AddField(digestStr)
			table.EndRow()
		}
	}
	err = table.Render()
	if err != nil {
		return err
	}
	fmt.Fprintln(w)

	return nil
}
