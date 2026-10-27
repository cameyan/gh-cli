package download

import (
	"errors"
	"fmt"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/auth"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmdutil"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"

	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"
)

func NewDownloadCmd(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{}
	downloadCmd := &cobra.Command{
		Use:   "download [<file-path> | oci://<image-uri>] [--owner | --repo]",
		Args:  cmdutil.ExactArgs(1, "必须指定文件路径或容器镜像 URI，以及 --owner 或 --repo 之一"),
		Short: "下载制品证明以供离线使用",
		Long: heredoc.Docf(`
			### 注意: 该功能目前处于公开预览阶段，可能会有所变更。

			下载与制品关联的证明以供离线使用。

			该命令需要以下二者之一:
			* 指向制品的文件路径，或
			* 容器镜像 URI(例如 %[1]soci://<image-uri>%[1]s)
			  * (注意，如果提供 OCI URL，必须已通过其容器镜像仓库的身份认证)

			此外，该命令还需要以下二者之一:
			* %[1]s--repo%[1]s 标志(例如 --repo github/example)。
			* %[1]s--owner%[1]s 标志(例如 --owner github)。

			%[1]s--repo%[1]s 标志的值必须与制品所关联的 GitHub 仓库名称一致。

			%[1]s--owner%[1]s 标志的值必须与制品所关联仓库所属的 GitHub 组织名称一致。

			所有关联的签名包将写入当前目录下以制品摘要命名的文件中。例如，如果摘要为
			"sha256:1234"，文件将被命名为 "sha256:1234.jsonl"。

			冒号在 Windows 上是特殊字符，不能用于文件名。为兼容该限制，证明文件名将使用短横线(-)
			分隔算法与摘要。例如，如果摘要为 "sha256:1234"，文件将被命名为 "sha256-1234.jsonl"。
		`, "`"),
		Example: heredoc.Doc(`
			# 下载与组织关联的本地制品的证明
			$ gh attestation download example.bin -o github

			# 下载与仓库关联的本地制品的证明
			$ gh attestation download example.bin -R github/example

			# 下载与组织关联的 OCI 镜像的证明
			$ gh attestation download oci://example.com/foo/bar:latest -o github
		`),
		// PreRunE 用于在命令运行前校验标志(flag)
		// 如果返回错误，其消息将连同命令使用说明一起打印到终端
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// 创建整个 download 命令通用的日志记录器(logger)
			opts.Logger = io.NewHandler(f.IOStreams)

			// 设置制品(artifact)路径
			opts.ArtifactPath = args[0]

			// 检查提供的标志(flag)是否有效
			if err := opts.AreFlagsValid(); err != nil {
				return err
			}
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

			if opts.Hostname == "" {
				opts.Hostname, _ = ghauth.DefaultHost()
			}
			if err := auth.IsHostSupported(opts.Hostname); err != nil {
				return err
			}

			opts.APIClient = api.NewLiveClient(hc, externalClient, opts.Hostname, opts.Logger)
			opts.OCIClient = oci.NewLiveClient()
			opts.Store = NewLiveStore("")

			if runF != nil {
				return runF(opts)
			}

			if err := runDownload(opts); err != nil {
				return fmt.Errorf("下载制品的签名包失败: %v", err)
			}
			return nil
		},
	}

	downloadCmd.Flags().StringVarP(&opts.Owner, "owner", "o", "", "用于限定证明查询范围的 GitHub 组织")
	downloadCmd.Flags().StringVarP(&opts.Repo, "repo", "R", "", "仓库名称，格式为 <owner>/<repo>")
	downloadCmd.MarkFlagsMutuallyExclusive("owner", "repo")
	downloadCmd.MarkFlagsOneRequired("owner", "repo")
	downloadCmd.Flags().StringVarP(&opts.PredicateType, "predicate-type", "", "", "按提供的断言类型过滤证明")
	cmdutil.StringEnumFlag(downloadCmd, &opts.DigestAlgorithm, "digest-alg", "d", "sha256", []string{"sha256", "sha512"}, "用于计算制品摘要的算法")
	downloadCmd.Flags().IntVarP(&opts.Limit, "limit", "L", api.DefaultLimit, "最多获取的证明数量")
	downloadCmd.Flags().StringVarP(&opts.Hostname, "hostname", "", "", "配置要使用的主机")

	return downloadCmd
}

func runDownload(opts *Options) error {
	artifact, err := artifact.NewDigestedArtifact(opts.OCIClient, opts.ArtifactPath, opts.DigestAlgorithm)
	if err != nil {
		return fmt.Errorf("计算制品摘要失败: %v", err)
	}

	opts.Logger.VerbosePrintf("正在下载制品 %s 的受信任元数据\n\n", opts.ArtifactPath)

	if opts.APIClient == nil {
		return fmt.Errorf("未提供 APIClient")
	}
	params := api.FetchParams{
		Digest: artifact.DigestWithAlg(),
		Limit:  opts.Limit,
		Owner:  opts.Owner,
		Repo:   opts.Repo,
	}
	attestations, err := opts.APIClient.GetByDigest(params)
	if err != nil {
		if errors.Is(err, api.ErrNoAttestationsFound) {
			fmt.Fprintf(opts.Logger.IO.Out, "未找到 %s 的证明\n", opts.ArtifactPath)
			return nil
		}
		return fmt.Errorf("获取证明失败: %v", err)
	}

	// 对返回的证明(attestation)应用谓词类型(predicate type)过滤
	if opts.PredicateType != "" {
		filteredAttestations, err := api.FilterAttestations(opts.PredicateType, attestations)
		if err != nil {
			return fmt.Errorf("过滤证明失败: %v", err)
		}

		attestations = filteredAttestations
	}

	metadataFilePath, err := opts.Store.createMetadataFile(artifact.DigestWithAlg(), attestations)
	if err != nil {
		return fmt.Errorf("写入证明失败: %v", err)
	}
	fmt.Fprintf(opts.Logger.IO.Out, "证明已写入文件 %s。\n之前的任何内容都已被覆盖\n\n", metadataFilePath)

	fmt.Fprint(opts.Logger.IO.Out,
		opts.Logger.ColorScheme.Greenf(
			"受信任元数据现已位于 %s\n", metadataFilePath,
		),
	)

	return nil
}
