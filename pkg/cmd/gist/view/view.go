package view

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/gist/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type browser interface {
	Browse(string) error
}

type ViewOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)
	Browser    browser
	Prompter   prompter.Prompter

	Selector  string
	Filename  string
	Raw       bool
	Web       bool
	ListFiles bool

	AllowEscapeSequences bool
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "view [<id> | <url>]",
		Short: "查看 gist",
		Long:  `查看指定的 gist 或从最近的 gist 中选择。`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Selector = args[0]
			}

			if !opts.IO.IsStdoutTTY() {
				opts.Raw = true
			}

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Raw, "raw", "r", false, "打印原始内容而非渲染后的 gist 内容")
	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开 gist")
	cmd.Flags().BoolVar(&opts.ListFiles, "files", false, "列出 gist 中的文件名")
	cmd.Flags().StringVarP(&opts.Filename, "filename", "f", "", "显示 gist 中的单个文件")
	cmd.Flags().BoolVar(&opts.AllowEscapeSequences, "allow-escape-sequences", false, "允许打印终端转义序列")

	return cmd
}

func viewRun(opts *ViewOptions) error {
	gistID := opts.Selector

	if opts.AllowEscapeSequences {
		opts.IO.SetContentSanitization(false)
	}

	client, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	hostname, _ := cfg.Authentication().DefaultHost()

	cs := opts.IO.ColorScheme()
	if gistID == "" {
		if !opts.IO.CanPrompt() {
			return cmdutil.FlagErrorf("非交互式运行时必须指定 gist ID 或 URL")
		}

		gist, err := shared.PromptGists(opts.Prompter, client, hostname, cs)
		if err != nil {
			return err
		}

		if gist.ID == "" {
			fmt.Fprintln(opts.IO.Out, "未找到 gist。")
			return nil
		}
		gistID = gist.ID
	}

	if opts.Web {
		gistURL := gistID
		if !strings.Contains(gistURL, "/") {
			gistURL = ghinstance.GistPrefix(hostname) + gistID
		}
		if opts.IO.IsStderrTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(gistURL))
		}
		return opts.Browser.Browse(gistURL)
	}

	if strings.Contains(gistID, "/") {
		id, err := shared.GistIDFromURL(gistID)
		if err != nil {
			return err
		}
		gistID = id
	}

	gist, err := shared.GetGist(client, hostname, gistID)
	if err != nil {
		return err
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	render := func(gf *shared.GistFile) error {
		// 将文件内容视为不可信的外部字节。被截断的
		// 路径会从原始 URL 获取完整内容。
		content := iostreams.NewUntrusted(gf.Content)
		if gf.Truncated {
			fullContent, err := shared.GetRawGistFile(client, hostname, safeurl.NewImmutableSafeURL(gf.RawURL))
			if err != nil {
				return err
			}

			content = fullContent
		}

		if shared.IsBinaryContents(content.RawBytes()) {
			if len(gist.Files) == 1 || opts.Filename != "" {
				return fmt.Errorf("错误: 文件为二进制文件")
			}
			_, err = fmt.Fprintln(opts.IO.Out, cs.Muted("(跳过渲染二进制内容)"))
			return nil
		}

		if strings.Contains(gf.Type, "markdown") && !opts.Raw {
			// Markdown 渲染会向 Out 输出应用样式的输出，因此其
			// 输入在此处被净化; --allow-escape-sequences 作用于
			// 下面的原始转储。
			rendered, err := markdown.Render(content.String(),
				markdown.WithTheme(opts.IO.TerminalTheme()),
				markdown.WithWrap(opts.IO.TerminalWidth()))
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(opts.IO.Out, rendered)
			return err
		}

		// 原始转储。在终端上，ContentOut 会让转义序列失效。
		// 当输出被管道传递时，拒绝携带转义序列的内容，
		// 而不是静默改写字节; --allow-escape-sequences
		// 强制使用原始内容。
		if !opts.AllowEscapeSequences && !opts.IO.IsStdoutTTY() {
			if iostreams.ContainsEscapeSequence(content.RawBytes()) {
				return errors.New("gist 文件包含终端转义序列; 传入 --allow-escape-sequences 仍可查看它")
			}
			opts.IO.SetContentSanitization(false)
		}
		raw := content.Raw()
		if _, err := fmt.Fprint(opts.IO.ContentOut, raw); err != nil {
			return err
		}
		if !strings.HasSuffix(raw, "\n") {
			_, err := fmt.Fprint(opts.IO.ContentOut, "\n")
			return err
		}

		return nil
	}

	if opts.Filename != "" {
		gistFile, ok := gist.Files[opts.Filename]
		if !ok {
			return fmt.Errorf("gist 中没有这样的文件: %q", opts.Filename)
		}
		return render(gistFile)
	}

	if gist.Description != "" && !opts.ListFiles {
		fmt.Fprintf(opts.IO.Out, "%s\n\n", cs.Bold(gist.Description))
	}

	showFilenames := len(gist.Files) > 1
	filenames := make([]string, 0, len(gist.Files))
	for fn := range gist.Files {
		filenames = append(filenames, fn)
	}

	sort.Slice(filenames, func(i, j int) bool {
		return strings.ToLower(filenames[i]) < strings.ToLower(filenames[j])
	})

	if opts.ListFiles {
		for _, fn := range filenames {
			fmt.Fprintln(opts.IO.Out, fn)
		}
		return nil
	}

	for i, fn := range filenames {
		if showFilenames {
			fmt.Fprintf(opts.IO.Out, "%s\n\n", cs.Muted(fn))
		}
		if err := render(gist.Files[fn]); err != nil {
			return err
		}
		if i < len(filenames)-1 {
			fmt.Fprint(opts.IO.Out, "\n")
		}
	}

	return nil
}
