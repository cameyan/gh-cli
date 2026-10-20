package readdir

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// dirEntryFields 是可通过 --json 标志选择的 JSON 字段。
var dirEntryFields = []string{
	"name",
	"path",
	"nameRaw",
	"pathRaw",
	"type",
	"gitType",
	"mode",
	"modeOctal",
	"gitSHA",
	"size",
	"submodule",
}

// ReadDirOptions 保存 read-dir 命令的配置。
type ReadDirOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Exporter   cmdutil.Exporter

	Path string
	Ref  string
}

// NewCmdReadDir 创建 `gh repo read-dir` 命令。
func NewCmdReadDir(f *cmdutil.Factory, runF func(*ReadDirOptions) error) *cobra.Command {
	opts := &ReadDirOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		BaseRepo:   f.BaseRepo,
	}

	cmd := &cobra.Command{
		Use:   "read-dir [<path>] [flags]",
		Short: "列出仓库中的目录(预览)",
		Long: heredoc.Docf(`
			无需克隆即可列出 GitHub 仓库中目录的内容。

			此命令处于预览阶段，可能会随时变更，恕不另行通知。

			默认情况下，从默认分支列出目录。使用 %[1]s--ref%[1]s 选项可以从特定的分支、标签或提交列出。未指定路径时，将列出仓库根目录。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出默认分支的根目录
			$ gh repo read-dir --repo cli/cli

			# 列出子目录
			$ gh repo read-dir docs --repo cli/cli

			# 列出特定 ref 上的目录
			$ gh repo read-dir docs --repo cli/cli --ref v2.50.0

			# 以 JSON 格式打印选定的字段
			$ gh repo read-dir docs --repo cli/cli --json name,path,type,size
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.Path = args[0]
			}

			if runF != nil {
				return runF(opts)
			}

			return readDirRun(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Ref, "ref", "", "要列出内容的分支、标签或提交")

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, dirEntryFields)

	cmdutil.EnableRepoOverride(cmd, f)

	return cmd
}

func readDirRun(opts *ReadDirOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	repo, err := opts.BaseRepo()
	if err != nil {
		return fmt.Errorf("%w。请在 git 仓库内运行此命令，或使用 `--repo` 标志指定仓库。", err)
	}

	dir, err := fetchTree(httpClient, repo, opts.Path, opts.Ref)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, dir)
	}

	if len(dir.Entries) == 0 {
		location := ghrepo.FullName(repo)
		if opts.Path != "" {
			location = fmt.Sprintf("%s/%s", location, strings.TrimPrefix(opts.Path, "/"))
		}
		fmt.Fprintf(opts.IO.ErrOut, "%s 中未找到任何条目\n", location)
		return nil
	}

	if !opts.IO.IsStdoutTTY() {
		return writeTSV(opts.IO, dir)
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器时出错: %v\n", err)
	}
	defer opts.IO.StopPager()

	location := ghrepo.FullName(repo)
	if opts.Path != "" {
		location = fmt.Sprintf("%s/%s", location, strings.TrimPrefix(opts.Path, "/"))
	}
	noun := "个条目"
	fmt.Fprintf(opts.IO.Out, "显示 %s 中的 %d %s\n\n", location, len(dir.Entries), noun)

	return writeTable(opts.IO, dir)
}

// writeTSV 为非 TTY 输出写入制表符分隔的列表: 类型、名称、
// 八进制权限模式和原始字节大小，无表头。
func writeTSV(io *iostreams.IOStreams, dir *repoDir) error {
	var sb strings.Builder
	for _, e := range dir.Entries {
		fmt.Fprintf(&sb, "%s\t%s\t%s\t%d\n", e.Type, e.Name, e.modeOctal(), e.Size)
	}
	_, err := io.Out.Write([]byte(sb.String()))
	return err
}

// writeTable 将 TTY 列表输出为 TYPE/NAME/SIZE 表格。名称按条目类型着色，
// 目录和子模块显示 "-"，因为 git 未报告它们的大小。
func writeTable(io *iostreams.IOStreams, dir *repoDir) error {
	cs := io.ColorScheme()
	tp := tableprinter.New(io, tableprinter.WithHeader("类型", "名称", "大小"))
	for _, e := range dir.Entries {
		entryType := e.Type
		if e.Type == "file" && e.isExecutable() {
			entryType = "file*"
		}

		var color string
		switch e.Type {
		case "dir":
			color = "blue"
		case "symlink":
			color = "cyan"
		case "submodule":
			color = "yellow"
		case "file":
			if e.isExecutable() {
				color = "green"
			}
		}

		size := "-"
		if e.Type == "file" || e.Type == "symlink" {
			size = text.FormatSize(int64(e.Size))
		}

		tp.AddField(entryType)
		tp.AddField(e.Name, tableprinter.WithColor(cs.ColorFromString(color)))
		tp.AddField(size)
		tp.EndRow()
	}
	return tp.Render()
}
