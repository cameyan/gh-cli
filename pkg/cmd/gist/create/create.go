package create

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/gist/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type CreateOptions struct {
	IO *iostreams.IOStreams

	Description      string
	Public           bool
	Filenames        []string
	FilenameOverride string
	WebMode          bool

	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)
	Browser    browser.Browser
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := CreateOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
	}

	cmd := &cobra.Command{
		Use:   "create [<filename>... | <pattern>... | -]",
		Short: "创建新的 gist",
		Long: heredoc.Docf(`
			使用给定内容创建新的 GitHub gist。

			gist 可以由一个或多个文件创建。也可以传入 %[1]s-%[1]s 作为
			文件名，以从标准输入读取。

			默认情况下，gist 为私密; 使用 %[1]s--public%[1]s 可创建公开列出的 gist。
		`, "`"),
		Example: heredoc.Doc(`
			# 将文件 'hello.py' 发布为公开 gist
			$ gh gist create --public hello.py

			# 创建带描述的 gist
			$ gh gist create hello.py -d "我在 Python 中的 Hello-World 程序"

			# 创建包含多个文件的 gist
			$ gh gist create hello.py world.py cool.txt

			# 使用通配模式创建包含多个文件的 gist
			$ gh gist create *.md *.txt artifact.*

			# 从标准输入读取以创建 gist
			$ gh gist create -

			# 从另一条命令通过管道传递的输出创建 gist
			$ cat cool.txt | gh gist create
		`),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return nil
			}
			if opts.IO.IsStdinTTY() {
				return cmdutil.FlagErrorf("未传入文件名，且 STDIN 上没有任何内容")
			}
			return nil
		},
		Aliases: []string{"new"},
		RunE: func(c *cobra.Command, args []string) error {
			opts.Filenames = args

			if runF != nil {
				return runF(&opts)
			}
			return createRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Description, "desc", "d", "", "此 gist 的描述")
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "使用创建的 gist 打开网页浏览器")
	cmd.Flags().BoolVarP(&opts.Public, "public", "p", false, "公开列出此 gist (默认为 \"secret\")")
	cmd.Flags().StringVarP(&opts.FilenameOverride, "filename", "f", "", "提供从标准输入读取时要使用的文件名")
	return cmd
}

func createRun(opts *CreateOptions) error {

	readFromStdInArg, filenames := cmdutil.Partition(opts.Filenames, func(f string) bool {
		return f == "-"
	})

	filenames, err := cmdutil.GlobPaths(filenames)
	if err != nil {
		return err
	}

	filenames = append(filenames, readFromStdInArg...)

	if len(filenames) == 0 {
		filenames = []string{"-"}
	}

	files, err := processFiles(opts.IO.In, opts.FilenameOverride, filenames)
	if err != nil {
		return fmt.Errorf("收集待提交的文件失败: %w", err)
	}

	errOut := opts.IO.ErrOut
	cs := opts.IO.ColorScheme()
	gistName := guessGistName(files)

	processMessage := "正在创建 gist..."
	if gistName != "" {
		if len(files) > 1 {
			processMessage = "正在创建包含多个文件的 gist"
		} else {
			processMessage = fmt.Sprintf("正在创建 gist %s", gistName)
		}
	}
	fmt.Fprintf(errOut, "%s %s\n", cs.Muted("-"), processMessage)

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	opts.IO.StartProgressIndicator()
	gist, err := createGist(httpClient, host, opts.Description, opts.Public, files)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if httpError, ok := errors.AsType[api.HTTPError](err); ok {
			if httpError.StatusCode == http.StatusUnprocessableEntity {
				if detectEmptyFiles(files) {
					fmt.Fprintf(errOut, "%s 创建 gist 失败: %s\n", cs.FailureIcon(), "gist 文件不能为空")
					return cmdutil.SilentError
				}
			}
		}
		return fmt.Errorf("%s 创建 gist 失败: %w", cs.Red("X"), err)
	}

	completionMessage := fmt.Sprintf("已创建 %s gist", cs.Green("secret"))
	if opts.Public {
		completionMessage = fmt.Sprintf("已创建 %s gist", cs.Red("public"))
	}
	if gistName != "" {
		completionMessage += " " + gistName
	}
	fmt.Fprintf(errOut, "%s %s\n", cs.SuccessIconWithColor(cs.Green), completionMessage)

	if opts.WebMode {
		fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s。\n", text.DisplayURL(gist.HTMLURL))

		return opts.Browser.Browse(gist.HTMLURL)
	}

	fmt.Fprintln(opts.IO.Out, gist.HTMLURL)

	return nil
}

func processFiles(stdin io.ReadCloser, filenameOverride string, filenames []string) (map[string]*shared.GistFile, error) {
	fs := map[string]*shared.GistFile{}

	if len(filenames) == 0 {
		return nil, errors.New("未传入文件")
	}

	for i, f := range filenames {
		var filename string
		var content []byte
		var err error

		if f == "-" {
			if filenameOverride != "" {
				filename = filenameOverride
			} else {
				filename = fmt.Sprintf("gistfile%d.txt", i)
			}
			content, err = io.ReadAll(stdin)
			if err != nil {
				return fs, fmt.Errorf("从 stdin 读取失败: %w", err)
			}
			stdin.Close()

			if shared.IsBinaryContents(content) {
				return nil, fmt.Errorf("不支持二进制文件内容")
			}
		} else {
			isBinary, err := shared.IsBinaryFile(f)
			if err != nil {
				return fs, fmt.Errorf("读取文件 %s 失败: %w", f, err)
			}
			if isBinary {
				return nil, fmt.Errorf("上传 %s 失败: 不支持二进制文件", f)
			}

			content, err = os.ReadFile(f)
			if err != nil {
				return fs, fmt.Errorf("读取文件 %s 失败: %w", f, err)
			}

			filename = filepath.Base(f)
		}

		fs[filename] = &shared.GistFile{
			Content: string(content),
		}
	}

	return fs, nil
}

func guessGistName(files map[string]*shared.GistFile) string {
	filenames := make([]string, 0, len(files))
	gistName := ""

	re := regexp.MustCompile(`^gistfile\d+\.txt$`)
	for k := range files {
		if !re.MatchString(k) {
			filenames = append(filenames, k)
		}
	}

	if len(filenames) > 0 {
		sort.Strings(filenames)
		gistName = filenames[0]
	}

	return gistName
}

func createGist(client *http.Client, hostname, description string, public bool, files map[string]*shared.GistFile) (*shared.Gist, error) {
	body := &shared.Gist{
		Description: description,
		Public:      public,
		Files:       files,
	}

	requestBody := &bytes.Buffer{}
	enc := json.NewEncoder(requestBody)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}

	path, err := safeurl.JoinPath("gists")
	if err != nil {
		return nil, err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api 客户端机械式推广的一部分。
	// 后续可考虑是否将 api 客户端注入此调用点，而非在此构造
	resp, err := api.NewClientFromHTTP(client).Request(hostname, http.MethodPost, path.String(), requestBody,
		api.WithEndpointScopes("gist"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &shared.Gist{}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(result); err != nil {
		return nil, err
	}

	return result, nil
}

func detectEmptyFiles(files map[string]*shared.GistFile) bool {
	for _, file := range files {
		if strings.TrimSpace(file.Content) == "" {
			return true
		}
	}
	return false
}
