package verifyasset

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/cli/cli/v2/pkg/iostreams"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	att_io "github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

type VerifyAssetOptions struct {
	TagName       string
	BaseRepo      ghrepo.Interface
	Exporter      cmdutil.Exporter
	AssetFilePath string
	TrustedRoot   string
}

type VerifyAssetConfig struct {
	HttpClient  *http.Client
	IO          *iostreams.IOStreams
	Opts        *VerifyAssetOptions
	AttClient   api.Client
	AttVerifier shared.Verifier
}

func NewCmdVerifyAsset(f *cmdutil.Factory, runF func(*VerifyAssetConfig) error) *cobra.Command {
	opts := &VerifyAssetOptions{}

	cmd := &cobra.Command{
		Use:   "verify-asset [<tag>] <file-path>",
		Short: "校验给定资产(asset)是否源自某个发布(release)",
		Long: heredoc.Doc(`
			使用经密码学签名的证明(attestation)，校验给定的资产(asset)文件是否源自特定的 GitHub Release。

			证明(attestation)是 GitHub 就某个发布(release)及其资产所作的声明。

  			该命令检查您提供的资产是否与指定发布(release)的有效证明匹配(若未提供 tag，则为最新发布)。
			它通过验证资产的摘要(digest)是否与证明中的 subject 匹配，以及证明是否与该发布关联，来确保资产的完整性。
		`),
		Args: cobra.MaximumNArgs(2),
		Example: heredoc.Doc(`
			# 校验来自最新发布(release)的资产
			$ gh release verify-asset ./dist/my-asset.zip

			# 校验来自指定发布标签(tag)的资产
			$ gh release verify-asset v1.2.3 ./dist/my-asset.zip

			# 校验来自指定发布标签的资产，并以 JSON 格式输出证明
			$ gh release verify-asset v1.2.3 ./dist/my-asset.zip --format json
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				opts.TagName = args[0]
				opts.AssetFilePath = args[1]
			} else if len(args) == 1 {
				opts.AssetFilePath = args[0]
			} else {
				return cmdutil.FlagErrorf("必须指定资产(asset)文件路径")
			}

			opts.AssetFilePath = filepath.Clean(opts.AssetFilePath)

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

			config := &VerifyAssetConfig{
				Opts:        opts,
				HttpClient:  httpClient,
				AttClient:   attClient,
				AttVerifier: attVerifier,
				IO:          io,
			}

			if runF != nil {
				return runF(config)
			}

			return verifyAssetRun(config)
		},
	}
	cmdutil.AddFormatFlags(cmd, &opts.Exporter)
	cmd.Flags().StringVarP(&opts.TrustedRoot, "custom-trusted-root", "", "", "trusted_root.jsonl 文件的路径; 通常用于离线校验。")
	cmd.Flags().MarkHidden("custom-trusted-root")

	return cmd
}

func verifyAssetRun(config *VerifyAssetConfig) error {
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

	fileName := getFileName(opts.AssetFilePath)

	// 计算文件的摘要(digest)
	fileDigest, err := artifact.NewDigestedArtifact(nil, opts.AssetFilePath, "sha256")
	if err != nil {
		return err
	}

	ref, err := shared.FetchRefSHA(ctx, config.HttpClient, baseRepo, tagName)
	if err != nil {
		return err
	}

	releaseRefDigest := artifact.NewDigestedArtifactForRelease(ref, shared.DigestAlgForRef(ref))

	// 查找发布标签(tag) SHA 对应的证明(attestation)
	attestations, err := config.AttClient.GetByDigest(api.FetchParams{
		Digest:        releaseRefDigest.DigestWithAlg(),
		PredicateType: "release",
		Owner:         baseRepo.RepoOwner(),
		Repo:          baseRepo.RepoOwner() + "/" + baseRepo.RepoName(),
		// TODO: 允许通过 flag 设置该值。
		// 上限设为 100 以确保获取给定 SHA 的全部证明(attestation)。
		// 虽然一个 SHA 可以对应多个证明，但每个发布标签(tag)只关联一个证明。
		Initiator: "github",
		Limit:     100,
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
		return fmt.Errorf("在 %s/%s 中未找到发布(release) %s 的证明(attestation)", baseRepo.RepoOwner(), baseRepo.RepoName(), tagName)
	}

	// 按 subject 摘要(digest)过滤证明(attestation)
	filteredAttestations, err = shared.FilterAttestationsByFileDigest(filteredAttestations, fileDigest.Digest())
	if err != nil {
		return fmt.Errorf("解析摘要(digest) %s 的证明(attestation)出错: %w", fileDigest.DigestWithAlg(), err)
	}

	if len(filteredAttestations) == 0 {
		return fmt.Errorf("%s 的证明(attestation)不包含 subject %s", tagName, fileDigest.DigestWithAlg())
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
	fmt.Fprintf(io.Out, "已计算 %s 的摘要(digest): %s\n", fileName, fileDigest.DigestWithAlg())
	fmt.Fprintf(io.Out, "已将标签(tag) %s 解析为 %s\n", tagName, releaseRefDigest.DigestWithAlg())
	fmt.Fprint(io.Out, "已从 GitHub API 加载证明(attestation)\n\n")
	fmt.Fprintf(io.Out, cs.Green("%s 校验成功! %s 存在于发布(release) %s 中\n"), cs.SuccessIcon(), fileName, tagName)

	return nil
}

func getFileName(filePath string) string {
	// 从文件路径中获取文件名
	_, fileName := filepath.Split(filePath)
	return fileName
}
