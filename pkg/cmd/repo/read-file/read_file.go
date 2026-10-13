package readfile

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safepaths"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// fileFields are the JSON fields selectable via the --json flag.
var fileFields = []string{
	"name",
	"path",
	"gitSHA",
	"size",
	"url",
	"htmlUrl",
	"gitUrl",
	"downloadUrl",
	"type",
	"encoding",
	"content",
}

// ReadFileOptions holds the configuration for the read-file command.
type ReadFileOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Exporter   cmdutil.Exporter

	Path    string
	Ref     string
	Output  string
	Clobber bool

	AllowEscapeSequences bool
}

// NewCmdReadFile creates the `gh repo read-file` command.
func NewCmdReadFile(f *cmdutil.Factory, runF func(*ReadFileOptions) error) *cobra.Command {
	opts := &ReadFileOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		BaseRepo:   f.BaseRepo,
	}

	cmd := &cobra.Command{
		Use:   "read-file <path> [flags]",
		Short: "读取仓库中的文件(预览)",
		Long: heredoc.Docf(`
			无需克隆即可读取 GitHub 仓库中文件的内容。

			此命令处于预览阶段，可能会随时变更，恕不另行通知。

			默认情况下，从默认分支读取文件。使用 %[1]s--ref%[1]s 标志可以从特定的分支、标签或提交读取。

			在 TTY 模式下运行时，内容通过分页器(pager)显示。当 stdout 被管道传输或重定向时，
			原始内容会被直接写出。若要将文件保存到磁盘，请使用 %[1]s--output%[1]s 标志。

			默认情况下，该命令拒绝输出包含终端转义序列的文件，因为它们可能操纵您的终端。
			传入 %[1]s--allow-escape-sequences%[1]s 可以强制读取文件。
			此检查仅适用于终端和管道输出；使用 %[1]s--output%[1]s 写入磁盘时始终包含原始字节，
			就如同传入了 %[1]s--allow-escape-sequences%[1]s 一样。
		`, "`"),
		Example: heredoc.Doc(`
			# 从默认分支读取文件
			$ gh repo read-file README.md --repo cli/cli

			# 从特定 ref 读取文件
			$ gh repo read-file README.md --repo cli/cli --ref v2.50.0

			# 将文件保存到磁盘
			$ gh repo read-file README.md --repo cli/cli --output download/README.md

			# 以 JSON 格式打印选定的字段
			$ gh repo read-file README.md --repo cli/cli --json name,path,size,type

			# 读取包含终端转义序列的文件
			$ gh repo read-file path/to/file --repo OWNER/REPO --allow-escape-sequences
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			opts.Path = args[0]

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--json` 或 `--output` 其中之一",
				opts.Exporter != nil,
				opts.Output != "",
			); err != nil {
				return err
			}

			if runF != nil {
				return runF(opts)
			}

			return readFileRun(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Ref, "ref", "", "要读取内容的分支、标签或提交")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "将文件写入 `path` 而不是 stdout")
	cmd.Flags().BoolVar(&opts.Clobber, "clobber", false, "若输出路径已存在则覆盖")
	cmd.Flags().BoolVar(&opts.AllowEscapeSequences, "allow-escape-sequences", false, "允许打印终端转义序列")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, fileFields)

	cmdutil.EnableRepoOverride(cmd, f)

	return cmd
}

func readFileRun(opts *ReadFileOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	repo, err := opts.BaseRepo()
	if err != nil {
		return fmt.Errorf("%w。请在 git 仓库内运行此命令，或使用 `--repo` 标志指定仓库", err)
	}

	file, err := fetchFile(httpClient, repo, opts.Path, opts.Ref)
	if err != nil {
		return err
	}

	// 如果 API 未内联返回文件内容，它会将编码(encoding)设置为 "none"(例如对大文件)。
	contentAvailable := file.Encoding != "none"

	if opts.Exporter != nil {
		// 仅当调用方实际选择了 content 字段时才获取文件内容。
		if !contentAvailable && slices.Contains(opts.Exporter.Fields(), "content") {
			if err := loadContent(httpClient, repo, file, opts.Ref); err != nil {
				return err
			}
		}
		return opts.Exporter.Write(opts.IO, file)
	}

	if !contentAvailable {
		if err := loadContent(httpClient, repo, file, opts.Ref); err != nil {
			return err
		}
	}

	if opts.Output != "" {
		dest, err := writeToOutput(file, opts.Output, opts.Clobber)
		if err != nil {
			return err
		}
		if opts.IO.IsStdoutTTY() {
			cs := opts.IO.ColorScheme()
			fmt.Fprintf(opts.IO.ErrOut, "%s 已将 %s 写入 %s\n", cs.SuccessIcon(), file.Path, dest)
		}
		return nil
	}

	if len(file.Content) == 0 {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.ErrOut, "%s 文件为空\n", cs.WarningIcon())
		return nil
	}

	// read-file 在下面对转义序列自行防护，因此在直通(passthrough)模式下
	// 通过 ContentOut 写入原始字节。若保持净化(sanitization)开启，
	// 将损坏二进制文件并剥离 --allow-escape-sequences 显式允许的转义序列。
	opts.IO.SetContentSanitization(false)

	if mime, ok := iostreams.BinaryContentType(file.Content); ok {
		if opts.IO.IsStdoutTTY() {
			return fmt.Errorf("二进制文件(%s, %s)；请使用 --output 保存到文件或将 stdout 通过管道输出",
				mime, text.FormatSize(int64(file.Size)))
		}
		_, err = opts.IO.ContentOut.Write(file.Content)
		return err
	}

	// 无论在 TTY 还是非 TTY 模式下，除非使用 --allow-escape-sequences，
	// 否则拒绝终端转义序列，以防恶意文件操纵下游终端。
	if !opts.AllowEscapeSequences && iostreams.ContainsEscapeSequence(file.Content) {
		return errors.New("文件包含终端转义序列；请使用 --allow-escape-sequences 强制读取")
	}

	if opts.IO.IsStdoutTTY() {
		if err := opts.IO.StartPager(); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)时出错: %v\n", err)
		}
		defer opts.IO.StopPager()
	}

	_, err = opts.IO.ContentOut.Write(file.Content)
	return err
}

// loadContent 在 Contents API 未内联返回原始文件字节时获取它们。
// API 仅对大文件省略内联内容，并用 "none" 编码标记；
// 其余情况(包括空文件)均以 base64 编码返回，因此无需再获取。
func loadContent(httpClient *http.Client, repo ghrepo.Interface, file *repoFile, ref string) error {
	if file.Encoding != "none" {
		return nil
	}

	raw, err := fetchRawFile(httpClient, repo, file.Path, ref)
	if err != nil {
		return err
	}
	file.Content = raw
	return nil
}

// lstatResult 捕获命令所需的输出路径信息子集，
// 通过不跟随该路径上的符号链接来确定。
type lstatResult struct {
	isDir     bool
	isSymlink bool
}

// lstat 是 [os.Lstat] 的简化封装，报告路径是否为目录或符号链接，
// 使用 Lstat 以便不跟随路径上的符号链接。
func lstat(path string) (*lstatResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	return &lstatResult{
		isDir:     info.IsDir(),
		isSymlink: info.Mode()&os.ModeSymlink != 0,
	}, nil
}

// 这些变量间接引用写入输出时使用的文件系统操作，
// 以便测试可以在不触碰真实文件系统的情况下替换它们。
var (
	lstatF          = lstat
	writeOutputFile = rootedWriteOutputFile
)

// writeToOutput writes file content to a local path and returns the final destination.
// Symlink output paths are refused. A directory target receives the file under
// its remote basename using the local operating system's path rules, and existing
// regular files are only replaced when clobber is true.
func writeToOutput(file *repoFile, output string, clobber bool) (string, error) {
	dest := output
	outputDir := ""

	// 尾部分隔符表明用户希望 dest 是一个目录，即使它尚不存在。
	asDir := strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(os.PathSeparator))

	if lr, err := lstatF(trimTrailingPathSeparators(dest)); err == nil {
		if lr.isSymlink {
			return "", fmt.Errorf("输出路径是符号链接")
		}
		if lr.isDir {
			asDir = true
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	name := dest
	if asDir {
		name = filepath.Base(file.Name)
		if name == "." || !filepath.IsLocal(name) {
			return "", fmt.Errorf("invalid output filename %q", file.Name)
		}
		outputDir = dest
		dest = filepath.Join(dest, name)
	}

	if lr, err := lstatF(dest); err == nil {
		if lr.isSymlink {
			return "", fmt.Errorf("输出路径是符号链接")
		}
		if !clobber {
			return "", fmt.Errorf("输出路径已存在: %q(使用 --clobber 覆盖)", dest)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := writeOutputFile(outputDir, name, file.Content, 0o644, clobber); err != nil {
		if os.IsExist(err) && !clobber {
			return "", fmt.Errorf("output path already exists: %q (use --clobber to overwrite)", dest)
		}
		return "", err
	}

	return dest, nil
}

func trimTrailingPathSeparators(path string) string {
	end := len(path)
	for end > 0 && os.IsPathSeparator(path[end-1]) {
		end--
	}
	if end == 0 || end <= len(filepath.VolumeName(path)) {
		return path
	}
	return path[:end]
}

func rootedWriteOutputFile(dir, name string, data []byte, perm os.FileMode, replace bool) error {
	if dir == "" {
		return safepaths.WriteFile(name, data, perm, 0o755, replace)
	}

	root, err := safepaths.OpenRootDir(dir, 0o755)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(name, data, perm, 0o755, replace)
}
