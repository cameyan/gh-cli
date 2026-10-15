package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safepaths"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DownloadOptions struct {
	HttpClient        func() (*http.Client, error)
	IO                *iostreams.IOStreams
	BaseRepo          func() (ghrepo.Interface, error)
	OverwriteExisting bool
	SkipExisting      bool
	TagName           string
	FilePatterns      []string
	Destination       string
	OutputFile        string

	// 同时下载的最大数量
	Concurrency int

	ArchiveType string

	AllowEscapeSequences bool
}

func NewCmdDownload(f *cmdutil.Factory, runF func(*DownloadOptions) error) *cobra.Command {
	opts := &DownloadOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "download [<tag>]",
		Short: "下载发布(release)资产(asset)",
		Long: heredoc.Docf(`
			从 GitHub release 下载资产(asset)。

			若未显式提供标签(tag)名参数，则从项目中的最新发布(release)下载资产。
			此时必须提供 %[1]s--pattern%[1]s 或 %[1]s--archive%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 下载指定发布(release)的全部资产
			$ gh release download v1.2.3

			# 只下载最新发布(release)的 Debian 软件包
			$ gh release download --pattern '*.deb'

			# 指定多个文件 glob 模式
			$ gh release download -p '*.deb' -p '*.rpm'

			# 下载某个发布(release)的源代码归档(archive)
			$ gh release download v1.2.3 --archive=zip
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if len(args) == 0 {
				if len(opts.FilePatterns) == 0 && opts.ArchiveType == "" {
					return cmdutil.FlagErrorf("下载最新发布(release)时必须提供 `--pattern` 或 `--archive`")
				}
			} else {
				opts.TagName = args[0]
			}

			if err := cmdutil.MutuallyExclusive("只能指定 `--clobber` 或 `--skip-existing` 之一", opts.OverwriteExisting, opts.SkipExisting); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive("只能指定 `--dir` 或 `--output` 之一", opts.Destination != ".", opts.OutputFile != ""); err != nil {
				return err
			}

			// 检查归档类型选项的有效性
			if err := checkArchiveTypeOption(opts); err != nil {
				return err
			}

			opts.Concurrency = 5

			if runF != nil {
				return runF(opts)
			}
			return downloadRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputFile, "output", "O", "", "将单个资产(asset)写入的 `file` (使用 \"-\" 写入标准输出)")
	cmd.Flags().StringVarP(&opts.Destination, "dir", "D", ".", "下载文件的目标 `directory`")
	cmd.Flags().StringArrayVarP(&opts.FilePatterns, "pattern", "p", nil, "只下载匹配 glob 模式的资产")
	cmd.Flags().StringVarP(&opts.ArchiveType, "archive", "A", "", "以指定的 `format` (zip 或 tar.gz)下载源代码归档(archive)")
	cmd.Flags().BoolVar(&opts.OverwriteExisting, "clobber", false, "覆盖同名已有文件")
	cmd.Flags().BoolVar(&opts.SkipExisting, "skip-existing", false, "存在同名文件时跳过下载")
	cmd.Flags().BoolVar(&opts.AllowEscapeSequences, "allow-escape-sequences", false, "将资产(asset)写入标准输出时允许打印终端转义序列")

	cmdutil.DisableAuthCheck(cmd)

	return cmd
}

func checkArchiveTypeOption(opts *DownloadOptions) error {
	if len(opts.ArchiveType) == 0 {
		return nil
	}

	if err := cmdutil.MutuallyExclusive(
		"只能指定 '--pattern' 或 '--archive' 之一",
		true, // ArchiveType 长度 > 0
		len(opts.FilePatterns) > 0,
	); err != nil {
		return err
	}

	if opts.ArchiveType != "zip" && opts.ArchiveType != "tar.gz" {
		return cmdutil.FlagErrorf("`--archive` 的取值必须是 \"zip\" 或 \"tar.gz\"")
	}
	return nil
}

func downloadRun(opts *DownloadOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicatorWithLabel("正在查找要下载的资产(asset)")
	defer opts.IO.StopProgressIndicator()

	ctx := context.Background()

	var release *shared.Release
	if opts.TagName == "" {
		release, err = shared.FetchLatestRelease(ctx, httpClient, baseRepo)
		if err != nil {
			return err
		}
	} else {
		release, err = shared.FetchRelease(ctx, httpClient, baseRepo, opts.TagName)
		if err != nil {
			return err
		}
	}

	var toDownload []shared.ReleaseAsset
	isArchive := false
	if opts.ArchiveType != "" {
		var archiveURL string
		if opts.ArchiveType == "tar.gz" {
			archiveURL = release.TarballURL
		} else {
			archiveURL = release.ZipballURL
		}

		if archiveURL == "" {
			errMessage := fmt.Sprintf(
				"标签(tag)为 %q 的发布(release) %q 没有 %q 归档(archive)资产。",
				release.Name, release.TagName, opts.ArchiveType,
			)
			if release.IsDraft {
				errMessage += " 这很可能是因为它还是草稿(draft)。"
			}
			return errors.New(errMessage)
		}

		// 创建指向 ZipballURL 或 TarballURL 的无名伪资产(asset)
		toDownload = append(toDownload, shared.ReleaseAsset{APIURL: archiveURL})
		isArchive = true
	} else {
		for _, a := range release.Assets {
			if len(opts.FilePatterns) > 0 && !matchAny(opts.FilePatterns, a.Name) {
				continue
			}
			// 注意: 如果将来需要在更多操作系统上检查保留文件名，
			// 应改用构建约束(build constraints)模式，而不是在运行时检查操作系统。
			if runtime.GOOS == "windows" && isWindowsReservedFilename(a.Name) {
				return fmt.Errorf("由于资产(asset)使用了保留文件名 %q，无法下载发布(release)", a.Name)
			}
			toDownload = append(toDownload, a)
		}
	}

	if len(toDownload) == 0 {
		if len(release.Assets) > 0 {
			return errors.New("没有匹配文件模式的资产(asset)")
		}
		return errors.New("没有可下载的资产(asset)")
	}

	if len(toDownload) > 1 && opts.OutputFile != "" {
		return fmt.Errorf("使用 `--output` 无法写入多个资产(asset)，当前有 %d 个资产", len(toDownload))
	}

	// 写入标准输出的资产(asset)属于外部内容。它经由 ContentOut 流转，以保证输出可审计；
	// 安全决策(拒绝写入终端的二进制内容或文本中的转义序列，除非使用 --allow-escape-sequences)
	// 在下方每次复制时单独做出。写入文件时保留原始字节。
	opts.IO.SetContentSanitization(false)

	dest := &destinationWriter{
		file:         opts.OutputFile,
		dir:          opts.Destination,
		skipExisting: opts.SkipExisting,
		overwrite:    opts.OverwriteExisting,
		stdout:       opts.IO.ContentOut,
		allowEscapes: opts.AllowEscapeSequences,
		isTTY:        opts.IO.IsStdoutTTY(),
	}
	defer dest.Close()

	targets := make([]downloadTarget, len(toDownload))
	for i, a := range toDownload {
		targets[i] = downloadTarget{
			url:  safeurl.NewImmutableSafeURL(a.APIURL),
			name: a.Name,
		}
	}

	return downloadAssets(dest, httpClient, targets, opts.Concurrency, isArchive, opts.IO)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if isMatch, err := filepath.Match(p, name); err == nil && isMatch {
			return true
		}
	}
	return false
}

type downloadTarget struct {
	url  safeurl.SafeURL
	name string
}

func downloadAssets(dest *destinationWriter, httpClient *http.Client, toDownload []downloadTarget, numWorkers int, isArchive bool, io *iostreams.IOStreams) error {
	if numWorkers == 0 {
		return errors.New("并发 worker 数量必须大于 0")
	}

	jobs := make(chan downloadTarget, len(toDownload))
	results := make(chan error, len(toDownload))

	if len(toDownload) < numWorkers {
		numWorkers = len(toDownload)
	}

	for w := 1; w <= numWorkers; w++ {
		go func() {
			for a := range jobs {
				io.StartProgressIndicatorWithLabel(fmt.Sprintf("正在下载 %s", a.name))
				results <- downloadAsset(dest, httpClient, a.url, a.name, isArchive)
			}
		}()
	}

	for _, a := range toDownload {
		jobs <- a
	}
	close(jobs)

	var downloadError error
	for range toDownload {
		if err := <-results; err != nil && !errors.Is(err, errSkipped) {
			downloadError = err
		}
	}

	io.StopProgressIndicator()

	return downloadError
}

func downloadAsset(dest *destinationWriter, httpClient *http.Client, assetURL safeurl.SafeURL, fileName string, isArchive bool) error {
	if err := dest.Check(fileName); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, assetURL.String(), nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/octet-stream")
	if isArchive {
		// 由于 zipball/tarball API 端点的一个缺陷，必须在 Accept 请求头中加入 application/json
		req.Header.Set("Accept", "application/octet-stream, application/json")

		// 覆盖 HTTP 重定向逻辑，以避开"遗留(legacy)"的 Codeload 资源。这是客户端层
		// 而非请求层的设置，因此会先复制 client 再移交，而不是修改与其他所有调用方共享的那个 client。
		oldClient := *httpClient
		httpClient = &oldClient
		httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) == 1 {
				req.URL.Path = removeLegacyFromCodeloadPath(req.URL.Path)
			}
			return nil
		}
	}

	// 资产(asset) URL 由 API 提供，因此它是绝对地址，并按原样请求。使用 DoRequest 而非 Request，
	// 因为上面的 CheckRedirect 是 client 的属性，而 Request 委托给 go-gh，go-gh 仅凭 transport
	// 自行构建 client，会静默丢弃该属性。没有它，"遗留(legacy)"的 Codeload 路径永远不会被改写，
	// 归档(archive)将以错误的名称保存。
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).DoRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if len(fileName) == 0 {
		contentDisposition := resp.Header.Get("Content-Disposition")

		_, params, err := mime.ParseMediaType(contentDisposition)
		if err != nil {
			return fmt.Errorf("无法解析归档(archive)的文件名: %w", err)
		}
		if serverFileName, ok := params["filename"]; ok {
			fileName = filepath.Base(serverFileName)
		} else {
			return errors.New("无法确定归档(archive)的文件名")
		}
	}

	return dest.Copy(fileName, resp.Body)
}

var codeloadLegacyRE = regexp.MustCompile(`^(/[^/]+/[^/]+/)legacy\.`)

// removeLegacyFromCodeloadPath 将"遗留(legacy)"Codeload 归档(archive)的 URL 转换为
// 与在网页上从带标签(tag)的发布(release)下载"Source code (zip/tar.gz)"时相同的格式。
// 遗留 URL 的形式如下:
//
//	https://codeload.github.com/OWNER/REPO/legacy.zip/refs/tags/TAGNAME
//
// 移除 "legacy." 部分后即得到我们所要归档(archive)格式的有效 Codeload URL。
func removeLegacyFromCodeloadPath(p string) string {
	return codeloadLegacyRE.ReplaceAllString(p, "$1")
}

var errSkipped = errors.New("已跳过")

// destinationWriter 负责将内容写入目标文件
type destinationWriter struct {
	file         string
	dir          string
	skipExisting bool
	overwrite    bool
	stdout       io.Writer
	allowEscapes bool
	isTTY        bool
	root         *safepaths.Root
	rootOnce     sync.Once
	rootErr      error
}

func (w *destinationWriter) ensureRoot() error {
	if w.file != "" {
		return nil
	}
	w.rootOnce.Do(func() {
		w.root, w.rootErr = safepaths.OpenRootDir(w.dir, 0o755)
	})
	return w.rootErr
}

func (w *destinationWriter) Close() error {
	if w.root == nil {
		return nil
	}
	return w.root.Close()
}

func (w *destinationWriter) makePath(name string) string {
	if w.file == "" {
		return filepath.Join(w.dir, name)
	}
	return w.file
}

// Check 在目标位置已存在同名文件时返回错误
func (w *destinationWriter) Check(name string) error {
	if name == "" {
		// 跳过检查，因为文件名只有在 API 请求之后才能确定
		return nil
	}
	fp := w.makePath(name)
	if fp == "-" {
		// 写入标准输出时应始终继续
		return nil
	}
	if err := w.validateName(name); err != nil {
		return err
	}
	return w.check(fp)
}

func (w *destinationWriter) validateName(name string) error {
	if w.file != "" {
		return nil
	}
	return safepaths.ValidateChild(name)
}

func (w *destinationWriter) check(fp string) error {
	_, err := os.Lstat(fp)
	if err == nil {
		if w.skipExisting {
			return errSkipped
		}
		if !w.overwrite {
			return fmt.Errorf(
				"%s 已存在(使用 `--clobber` 覆盖文件，或使用 `--skip-existing` 跳过文件)",
				fp,
			)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Copy 将 r 中的数据写入由 name 指定的文件。
func (w *destinationWriter) Copy(name string, r io.Reader) (copyErr error) {
	fp := w.makePath(name)
	if fp == "-" {
		if w.allowEscapes {
			_, copyErr = io.Copy(w.stdout, r)
			return
		}
		copyErr = iostreams.CopyGuardedContent(w.stdout, r, w.isTTY)
		if binErr, ok := errors.AsType[iostreams.BinaryTerminalError](copyErr); ok {
			copyErr = fmt.Errorf("%w; 使用 `--output` 将其保存到文件，或传入 --allow-escape-sequences 仍然输出", binErr)
		} else if errors.Is(copyErr, iostreams.ErrEscapeSequence) {
			copyErr = errors.New("该资产(asset)包含终端转义序列; 使用 `--output` 将其保存到文件，或传入 --allow-escape-sequences 仍然输出")
		}
		return
	}
	if copyErr = w.validateName(name); copyErr != nil {
		return
	}
	if copyErr = w.check(fp); copyErr != nil {
		return
	}

	var f *os.File
	if w.file == "" {
		if copyErr = w.ensureRoot(); copyErr != nil {
			return
		}
		f, copyErr = w.root.Create(name, 0o644, 0o755, w.overwrite)
	} else {
		f, copyErr = safepaths.OpenFile(fp, 0o644, 0o755, w.overwrite)
	}
	if copyErr != nil {
		if os.IsExist(copyErr) {
			if w.skipExisting {
				return errSkipped
			}
			if !w.overwrite {
				return fmt.Errorf(
					"%s 已存在(使用 `--clobber` 覆盖文件，或使用 `--skip-existing` 跳过文件)",
					fp,
				)
			}
		}
		return
	}

	defer func() {
		if err := f.Close(); copyErr == nil && err != nil {
			copyErr = err
		}
	}()

	_, copyErr = io.Copy(f, r)
	return
}

func isWindowsReservedFilename(filename string) bool {
	// Windows 终端应当阻止创建这些文件，
	// 但该行为并未在所有终端上强制执行。阻止用户下载
	// 这些保留名称的文件，因为它们是恶意行为者的一种利用向量。
	// 保留文件名定义于:
	// https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file#win32-file-namespaces
	reservedFilenames := []string{"CON", "PRN", "AUX", "NUL", "COM0",
		"COM1", "COM2", "COM3", "COM4", "COM5",
		"COM6", "COM7", "COM8", "COM9", "COM¹",
		"COM²", "COM³", "LPT0", "LPT1", "LPT2",
		"LPT3", "LPT4", "LPT5", "LPT6", "LPT7",
		"LPT8", "LPT9", "LPT¹", "LPT²", "LPT³"}

	// 规范化大小写并移除文件名中的文件类型扩展名。
	filename = strings.ToUpper(strings.Split(filename, ".")[0])

	return slices.Contains(reservedFilenames, filename)
}
