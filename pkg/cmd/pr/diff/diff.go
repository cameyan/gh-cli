package diff

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/go-gh/v2/pkg/asciisanitizer"
	"github.com/spf13/cobra"
	"golang.org/x/text/transform"
)

type DiffOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Browser    browser.Browser

	Finder shared.PRFinder

	SelectorArg string
	UseColor    bool
	Patch       bool
	NameOnly    bool
	BrowserMode bool
	Exclude     []string

	AllowEscapeSequences bool
}

func NewCmdDiff(f *cmdutil.Factory, runF func(*DiffOptions) error) *cobra.Command {
	opts := &DiffOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
	}

	var colorFlag string

	cmd := &cobra.Command{
		Use:   "diff [<number> | <url> | <branch>]",
		Short: "查看拉取请求(PR)中的变更",
		Long: heredoc.Docf(`
			查看拉取请求(PR)中的变更。

			若未提供参数，将选择属于当前分支的拉取请求(PR)。

			使用 %[1]s--web%[1]s flag 时，改为在 web 浏览器中打开拉取请求(PR)的差异(diff)。

			使用 %[1]s--exclude%[1]s 可过滤掉匹配 glob 模式的文件。该模式在所有平台上
			均使用正斜杠作为路径分隔符。可以重复使用该 flag 以排除多个模式。

			默认情况下，差异(diff)中的终端转义序列会被中和，因为它们可能操纵您的终端。
			传入 %[1]s--allow-escape-sequences%[1]s 可原样打印差异(diff)，
			例如将补丁(patch)通过管道传给其他程序时。
		`, "`"),
		Example: heredoc.Doc(`
			# 查看当前分支的差异(diff)
			$ gh pr diff

			# 查看指定 PR 的差异(diff)
			$ gh pr diff 123

			# 从差异(diff)输出中排除文件
			$ gh pr diff --exclude '*.yml' --exclude 'generated/*'

			# 按文件名排除匹配的文件
			$ gh pr diff --name-only --exclude '*.generated.*'
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 `--repo` flag 时必须提供位置参数")
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			switch colorFlag {
			case "always":
				opts.UseColor = true
			case "auto":
				opts.UseColor = opts.IO.ColorEnabled()
			case "never":
				opts.UseColor = false
			default:
				return fmt.Errorf("不支持的颜色选项 %q", colorFlag)
			}

			if runF != nil {
				return runF(opts)
			}
			return diffRun(opts)
		},
	}

	cmdutil.StringEnumFlag(cmd, &colorFlag, "color", "", "auto", []string{"always", "never", "auto"}, "在差异(diff)输出中使用颜色")
	cmd.Flags().BoolVar(&opts.Patch, "patch", false, "以补丁(patch)格式显示差异(diff)")
	cmd.Flags().BoolVar(&opts.NameOnly, "name-only", false, "仅显示变更文件的名称")
	cmd.Flags().BoolVarP(&opts.BrowserMode, "web", "w", false, "在浏览器中打开拉取请求(PR)的差异(diff)")
	cmd.Flags().StringSliceVarP(&opts.Exclude, "exclude", "e", nil, "从差异(diff)中排除匹配 glob `patterns` 的文件")
	cmd.Flags().BoolVar(&opts.AllowEscapeSequences, "allow-escape-sequences", false, "允许打印终端转义序列")

	return cmd
}

func diffRun(opts *DiffOptions) error {
	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"number"},
	}

	if opts.BrowserMode {
		findOptions.Fields = []string{"url"}
	}

	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	if opts.BrowserMode {
		openUrl := fmt.Sprintf("%s/files", pr.URL)
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openUrl))
		}
		return opts.Browser.Browse(openUrl)
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	if opts.NameOnly {
		opts.Patch = false
	}

	diffReadCloser, err := fetchDiff(httpClient, baseRepo, pr.Number, opts.Patch)
	if err != nil {
		return fmt.Errorf("无法找到拉取请求(PR)的差异(diff): %w", err)
	}
	defer diffReadCloser.Close()

	var diff io.Reader = diffReadCloser
	if len(opts.Exclude) > 0 {
		filtered, err := filterDiff(diff, opts.Exclude)
		if err != nil {
			return err
		}
		diff = filtered
	}
	// 终端通过 ContentOut 将转义序列以惰性形式显示；管道输出则是忠实的，
	// 因此携带转义序列的差异(diff)会被拒绝，而不是被静默更改。
	// --allow-escape-sequences 在两种情况下都以原始形式输出。
	// 带颜色的路径始终进行中和，因为它面向终端。
	if opts.AllowEscapeSequences {
		opts.IO.SetContentSanitization(false)
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}

	if opts.NameOnly {
		return changedFilesNames(opts.IO.ContentOut, diff)
	}

	if opts.UseColor {
		return colorDiffLines(opts.IO.Out, sanitizedReader(diff))
	}

	if !opts.AllowEscapeSequences && !opts.IO.IsStdoutTTY() {
		data, err := io.ReadAll(diff)
		if err != nil {
			return err
		}
		if iostreams.ContainsEscapeSequence(data) {
			return errors.New("该差异(diff)包含终端转义序列; 传入 --allow-escape-sequences 仍然输出")
		}
		opts.IO.SetContentSanitization(false)
		_, err = opts.IO.ContentOut.Write(data)
		return err
	}

	_, err = io.Copy(opts.IO.ContentOut, diff)
	return err
}

func fetchDiff(httpClient *http.Client, baseRepo ghrepo.Interface, prNumber int, asPatch bool) (io.ReadCloser, error) {
	path, err := safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "pulls", strconv.Itoa(prNumber))
	if err != nil {
		return nil, err
	}
	acceptType := "application/vnd.github.v3.diff"
	if asPatch {
		acceptType = "application/vnd.github.v3.patch"
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).Request(baseRepo.RepoHost(), http.MethodGet, path.String(), nil,
		api.WithHeader("Accept", acceptType))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		defer resp.Body.Close()
		return nil, api.UnexpectedStatusError(resp)
	}

	return resp.Body, nil
}

const lineBufferSize = 4096

var (
	colorHeader   = []byte("\x1b[1;37m")
	colorAddition = []byte("\x1b[32m")
	colorRemoval  = []byte("\x1b[31m")
	colorReset    = []byte("\x1b[m")
)

func colorDiffLines(w io.Writer, r io.Reader) error {
	diffLines := bufio.NewReaderSize(r, lineBufferSize)
	wasPrefix := false
	needsReset := false

	for {
		diffLine, isPrefix, err := diffLines.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("读取拉取请求(PR)差异(diff)出错: %w", err)
		}

		var color []byte
		if !wasPrefix {
			if isHeaderLine(diffLine) {
				color = colorHeader
			} else if isAdditionLine(diffLine) {
				color = colorAddition
			} else if isRemovalLine(diffLine) {
				color = colorRemoval
			}
		}

		if color != nil {
			if _, err := w.Write(color); err != nil {
				return err
			}
			needsReset = true
		}

		if _, err := w.Write(diffLine); err != nil {
			return err
		}

		if !isPrefix {
			if needsReset {
				if _, err := w.Write(colorReset); err != nil {
					return err
				}
				needsReset = false
			}
			if _, err := w.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
		wasPrefix = isPrefix
	}
	return nil
}

var diffHeaderPrefixes = []string{"+++", "---", "diff", "index"}

func isHeaderLine(l []byte) bool {
	dl := string(l)
	for _, p := range diffHeaderPrefixes {
		if strings.HasPrefix(dl, p) {
			return true
		}
	}
	return false
}

func isAdditionLine(l []byte) bool {
	return len(l) > 0 && l[0] == '+'
}

func isRemovalLine(l []byte) bool {
	return len(l) > 0 && l[0] == '-'
}

func changedFilesNames(w io.Writer, r io.Reader) error {
	diff, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	// 这个正则表达式有点复杂。我们要查找的是如下格式的行:
	// diff --git a/9114-triage b/9114-triage
	// diff --git "a/hello-\360\237\230\200-world" "b/hello-\360\237\230\200-world"
	//
	// 从这些行中我们要提取:
	// 9114-triage
	// "hello-\360\237\230\200-world"
	//
	// 注意 b/ 会被移除，但在第二种情况下前面的引号会保留。
	// 这对 git 处理会被 core.quotePath 加引号的文件名非常重要。
	// https://git-scm.com/docs/git-config#Documentation/git-config.txt-corequotePath
	//
	// 因此我们捕获引号(如果存在)，以及 b/ 之后的所有内容。
	// 然后我们将这两个捕获组拼接在一起，对于上面的示例，结果为:
	// `` + 9114-triage
	// `"`` + hello-\360\237\230\200-world"
	//
	// 这里我用 `` 表示字符串，以避免与 " 字符混淆。
	matches := diffHeaderRegexp.FindAllStringSubmatch(string(diff), -1)

	for _, val := range matches {
		name := strings.TrimSpace(val[1] + val[2])
		if _, err := w.Write([]byte(name + "\n")); err != nil {
			return err
		}
	}

	return nil
}

func sanitizedReader(r io.Reader) io.Reader {
	return transform.NewReader(r, &asciisanitizer.Sanitizer{})
}

var diffHeaderRegexp = regexp.MustCompile(`(?:^|\n)diff\s--git.*\s("?)b/(.*)`)

// filterDiff 读取统一格式(unified)的差异(diff)，返回移除了匹配任一排除模式的
// 文件条目后的新 reader。
func filterDiff(r io.Reader, excludePatterns []string) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var result bytes.Buffer
	for _, section := range splitDiffSections(string(data)) {
		name := extractFileName(section)
		if name != "" && matchesAny(name, excludePatterns) {
			continue
		}
		result.WriteString(section)
	}
	return &result, nil
}

// splitDiffSections 将统一格式(unified)的差异(diff)字符串按文件拆分为多个部分。
// 每个部分以 "diff --git" 开头，包含直到(但不包括)下一个 "diff --git" 行之前的全部内容。
func splitDiffSections(diff string) []string {
	marker := "\ndiff --git "
	parts := strings.Split(diff, marker)
	if len(parts) == 1 {
		return []string{diff}
	}
	sections := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == 0 {
			if len(p) > 0 {
				sections = append(sections, p+"\n")
			}
		} else {
			sections = append(sections, "diff --git "+p)
		}
	}
	return sections
}

func extractFileName(section string) string {
	m := diffHeaderRegexp.FindStringSubmatch(section)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1] + m[2])
}

func matchesAny(name string, excludePatterns []string) bool {
	for _, p := range excludePatterns {
		if matched, _ := path.Match(p, name); matched {
			return true
		}
		// 同时对基名(basename)进行匹配，使 "*.yml" 能匹配 "dir/file.yml"
		if matched, _ := path.Match(p, path.Base(name)); matched {
			return true
		}
	}
	return false
}
