package upload

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type UploadOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	TagName string
	Assets  []*shared.AssetForUpload

	// 同时上传的最大数量
	Concurrency       int
	OverwriteExisting bool
}

func NewCmdUpload(f *cmdutil.Factory, runF func(*UploadOptions) error) *cobra.Command {
	opts := &UploadOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "upload <tag> <files>...",
		Short: "向发布(release)上传资产(asset)",
		Long: heredoc.Docf(`
			向 GitHub Release 上传资产(asset)文件。

			要为资产定义显示标签(label)，可在文件名后追加以 %[1]s#%[1]s 开头的文本。

			使用 %[1]s--clobber%[1]s 时，会先删除已有同名资产，再上传新资产。
			如果上传失败，原资产将会丢失。
		`, "`"),
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			opts.TagName = args[0]

			var err error
			opts.Assets, err = shared.AssetsFromArgs(args[1:])
			if err != nil {
				return err
			}

			opts.Concurrency = 5

			if runF != nil {
				return runF(opts)
			}
			return uploadRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.OverwriteExisting, "clobber", false, "删除并重新上传同名已有资产")

	return cmd
}

func uploadRun(opts *UploadOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	release, err := shared.FetchRelease(context.Background(), httpClient, baseRepo, opts.TagName)
	if err != nil {
		return err
	}

	var existingNames []string
	for _, a := range opts.Assets {
		sanitizedFileName := sanitizeFileName(a.Name)
		for _, ea := range release.Assets {
			if ea.Name == sanitizedFileName {
				a.ExistingURL = safeurl.NewImmutableSafeURL(ea.APIURL)
				existingNames = append(existingNames, ea.Name)
				break
			}
		}
	}

	if len(existingNames) > 0 && !opts.OverwriteExisting {
		return fmt.Errorf("已存在同名资产(asset): %v", existingNames)
	}

	uploadURL := release.UploadURL
	if idx := strings.IndexRune(uploadURL, '{'); idx > 0 {
		uploadURL = uploadURL[:idx]
	}

	opts.IO.StartProgressIndicator()
	err = shared.ConcurrentUpload(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(uploadURL), opts.Concurrency, opts.Assets)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		iofmt := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "成功上传 %s 到 %s\n",
			text.Pluralize(len(opts.Assets), "asset"),
			iofmt.Bold(release.TagName))
	}

	return nil
}

// 此方法尝试在客户端上模拟平台对上传资产所做的同名处理，
// 以便让 --clobber 逻辑正确工作，因为该功能仅存在于客户端中
func sanitizeFileName(name string) string {
	value := text.RemoveDiacritics(name)
	// 已剥离所有非 ASCII 字符，提供默认名称。
	if strings.HasPrefix(value, ".") {
		value = "default" + value
	}

	// 将特殊字符替换为分隔符
	value = regexp.MustCompile(`(?i)[^a-z0-9\-_\+@]+`).ReplaceAllLiteralString(value, ".")

	// 分隔符不允许连续出现多个。
	value = regexp.MustCompile(`\.{2,}`).ReplaceAllLiteralString(value, ".")

	// 移除首尾的分隔符。
	value = strings.Trim(value, ".")

	// 只剩下文件扩展名时，添加默认名称。
	if name != value && !strings.Contains(value, ".") {
		value = "default." + value
	}

	return value
}
