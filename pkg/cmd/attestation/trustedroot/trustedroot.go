package trustedroot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/auth"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
	o "github.com/cli/cli/v2/pkg/option"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"

	"github.com/MakeNowJust/heredoc"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/spf13/cobra"
)

type Options struct {
	TufUrl      string
	TufRootPath string
	VerifyOnly  bool
	Hostname    string
	TrustDomain string
}

type tufClientInstantiator func(o *tuf.Options) (*tuf.Client, error)

func NewTrustedRootCmd(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{}
	trustedRootCmd := cobra.Command{
		Use:   "trusted-root [--tuf-url <url> --tuf-root <file-path>] [--verify-only]",
		Args:  cobra.ExactArgs(0),
		Short: "输出 trusted_root.jsonl 的内容，通常用于离线验证",
		Long: heredoc.Docf(`
			输出 trusted_root.jsonl 文件的内容，通常用于离线验证。

			使用 %[1]sgh attestation verify%[1]s 时，如果您的机器已联网，该过程会自动完成。
			但若要进行离线验证，您需要通过 %[1]s--custom-trusted-root%[1]s 提供受信任根文件；
			该命令将帮助您为此获取 %[1]strusted_root.jsonl%[1]s 文件。

			您可以不带任何标志调用该命令，以获取同时覆盖 Sigstore Public Good Instance(PGI)
			和 GitHub 的 Sigstore 实例的受信任根文件。

			此外，您也可以使用 %[1]s--tuf-url%[1]s 指定自定义 TUF 仓库镜像的 URL，并且 %[1]s--tuf-root%[1]s
			应为您通过带外安全方式获取的 %[1]sroot.json%[1]s 文件的路径。

			如果您只想验证本地 TUF 仓库的完整性，而不需要 trusted_root.jsonl 文件的内容，请使用 %[1]s--verify-only%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 获取同时覆盖 Sigstore 公共善意实例(Public Good)和 GitHub 实例的 trusted_root.jsonl
			$ gh attestation trusted-root
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if ghauth.IsTenancy(opts.Hostname) {
				c, err := f.Config()
				if err != nil {
					return err
				}

				if !c.Authentication().HasActiveToken(opts.Hostname) {
					return fmt.Errorf("未针对 %s 完成身份认证", opts.Hostname)
				}
				logger := io.NewHandler(f.IOStreams)
				apiClient := api.NewLiveClient(hc, externalClient, opts.Hostname, logger)
				td, err := apiClient.GetTrustDomain()
				if err != nil {
					return err
				}
				opts.TrustDomain = td
			}

			if runF != nil {
				return runF(opts)
			}

			if err := getTrustedRoot(tuf.New, opts, externalClient); err != nil {
				return fmt.Errorf("验证 TUF 仓库失败: %w", err)
			}

			return nil
		},
	}

	cmdutil.DisableAuthCheck(&trustedRootCmd)
	trustedRootCmd.Flags().StringVarP(&opts.TufUrl, "tuf-url", "", "", "TUF 仓库镜像的 URL")
	trustedRootCmd.Flags().StringVarP(&opts.TufRootPath, "tuf-root", "", "", "磁盘上 TUF root.json 文件的路径")
	trustedRootCmd.MarkFlagsRequiredTogether("tuf-url", "tuf-root")
	trustedRootCmd.Flags().BoolVarP(&opts.VerifyOnly, "verify-only", "", false, "不输出 trusted_root.jsonl 的内容")
	trustedRootCmd.Flags().StringVarP(&opts.Hostname, "hostname", "", "", "配置要使用的主机")

	return &trustedRootCmd
}

type tufConfig struct {
	tufOptions *tuf.Options
	targets    []string
}

func getTrustedRoot(makeTUF tufClientInstantiator, opts *Options, hc *http.Client) error {
	var tufOptions []tufConfig
	var defaultTR = "trusted_root.json"

	tufOpt := verification.DefaultOptionsWithCacheSetting(o.None[string](), hc)
	// 禁用本地缓存，以便从 TUF 仓库(repository)获取最新响应
	tufOpt.CacheValidity = 0

	// 目标(target)将是默认的受信任根(trusted root)，或带信任域(trust domain)限定的那个
	ghTR := defaultTR
	if opts.TrustDomain != "" {
		ghTR = fmt.Sprintf("%s.%s", opts.TrustDomain, defaultTR)
	}

	if opts.TufUrl != "" && opts.TufRootPath != "" {
		tufRoot, err := os.ReadFile(opts.TufRootPath)
		if err != nil {
			return fmt.Errorf("读取根文件 %s 失败: %v", opts.TufRootPath, err)
		}

		tufOpt.Root = tufRoot
		tufOpt.RepositoryBaseURL = opts.TufUrl
		tufOptions = append(tufOptions, tufConfig{
			tufOptions: tufOpt,
			targets:    []string{ghTR},
		})
	} else {
		// 从 Sigstore 公共善意实例(public good)和 GitHub 私有实例两者获取
		tufOptions = append(tufOptions, tufConfig{
			tufOptions: tufOpt,
			targets:    []string{defaultTR},
		})

		tufOpt = verification.GitHubTUFOptions(o.None[string](), hc)
		tufOpt.CacheValidity = 0
		tufOptions = append(tufOptions, tufConfig{
			tufOptions: tufOpt,
			targets:    []string{ghTR},
		})
	}

	for _, tufOpt := range tufOptions {
		tufClient, err := makeTUF(tufOpt.tufOptions)
		if err != nil {
			return fmt.Errorf("创建 TUF 客户端失败: %v", err)
		}

		for _, target := range tufOpt.targets {
			t, err := tufClient.GetTarget(target)
			if err != nil {
				return fmt.Errorf("通过 TUF 获取受信任根 %s 失败: %w",
					target, err)
			}

			output := new(bytes.Buffer)
			err = json.Compact(output, t)
			if err != nil {
				return err
			}

			if !opts.VerifyOnly {
				fmt.Println(output)
			} else {
				fmt.Printf("用于 %s 的本地 TUF 仓库已更新并完成验证\n", tufOpt.tufOptions.RepositoryBaseURL)
			}
		}
	}

	return nil
}
