package view

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/template"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	Exporter   cmdutil.Exporter
	Config     func() (gh.Config, error)

	RepoArg string
	Web     bool
	Branch  string
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := ViewOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		BaseRepo:   f.BaseRepo,
		Browser:    f.Browser,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "view [<repository>]",
		Short: "查看仓库",
		Long: heredoc.Docf(`
			显示 GitHub 仓库的描述和 README。

			未提供参数时，显示当前目录对应的仓库。

			使用 %[1]s--web%[1]s 时，改为在网页浏览器中打开仓库。

			使用 %[1]s--branch%[1]s 时，查看仓库的特定分支。
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.RepoArg = args[0]
			}
			if runF != nil {
				return runF(&opts)
			}
			return viewRun(&opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开仓库")
	cmd.Flags().StringVarP(&opts.Branch, "branch", "b", "", "查看仓库的特定分支")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.RepositoryFields)

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "branch")

	return cmd
}

var defaultFields = []string{"name", "owner", "description"}

func viewRun(opts *ViewOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	var toView ghrepo.Interface
	apiClient := api.NewClientFromHTTP(httpClient)
	if opts.RepoArg == "" {
		var err error
		toView, err = opts.BaseRepo()
		if err != nil {
			return err
		}
	} else {
		viewURL := opts.RepoArg
		if !strings.Contains(viewURL, "/") {
			cfg, err := opts.Config()
			if err != nil {
				return err
			}
			hostname, _ := cfg.Authentication().DefaultHost()
			currentUser, err := api.CurrentLoginName(apiClient, hostname)
			if err != nil {
				return err
			}
			viewURL = currentUser + "/" + viewURL
		}
		toView, err = ghrepo.FromFullName(viewURL)
		if err != nil {
			return fmt.Errorf("参数错误: %w", err)
		}
	}

	var readme *RepoReadme
	fields := defaultFields
	if opts.Exporter != nil {
		fields = opts.Exporter.Fields()
	}

	repo, err := api.FetchRepository(apiClient, toView, fields)
	if err != nil {
		return err
	}

	if !opts.Web && opts.Exporter == nil {
		readme, err = RepositoryReadme(httpClient, toView, opts.Branch)
		if err != nil && !errors.Is(err, NotFoundError) {
			return err
		}
	}

	openURL := generateBranchURL(toView, opts.Branch)
	if opts.Web {
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
		}
		return opts.Browser.Browse(openURL)
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, repo)
	}

	fullName := ghrepo.FullName(toView)
	stdout := opts.IO.Out

	if !opts.IO.IsStdoutTTY() {
		fmt.Fprintf(stdout, "名称(name):\t%s\n", fullName)
		fmt.Fprintf(stdout, "描述(description):\t%s\n", repo.Description)
		if readme != nil {
			fmt.Fprintln(stdout, "--")
			fmt.Fprint(stdout, readme.Content)
			fmt.Fprintln(stdout)
		}

		return nil
	}

	repoTmpl := heredoc.Doc(`
		{{.FullName}}
		{{.Description}}

		{{.Readme}}

		{{.View}}
	`)

	tmpl, err := template.New("repo").Parse(repoTmpl)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()

	var readmeContent string
	if readme == nil {
		readmeContent = cs.Muted("该仓库没有 README")
	} else if isMarkdownFile(readme.Filename) {
		var err error
		readmeContent, err = markdown.Render(readme.Content,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithWrap(opts.IO.TerminalWidth()),
			markdown.WithBaseURL(readme.BaseURL))
		if err != nil {
			return fmt.Errorf("渲染 markdown 时出错: %w", err)
		}
	} else {
		readmeContent = readme.Content
	}

	description := repo.Description
	if description == "" {
		description = cs.Muted("未提供描述")
	}

	repoData := struct {
		FullName    string
		Description string
		Readme      string
		View        string
	}{
		FullName:    cs.Bold(fullName),
		Description: description,
		Readme:      readmeContent,
		View:        cs.Mutedf("在 GitHub 上查看此仓库: %s", openURL),
	}

	return tmpl.Execute(stdout, repoData)
}

func isMarkdownFile(filename string) bool {
	// 有点粗糙，但我假设 90% 的情况下后缀就是 .md。基于这个假设，
	// 似乎不值得为它执行一次正则匹配。
	return strings.HasSuffix(filename, ".md") ||
		strings.HasSuffix(filename, ".markdown") ||
		strings.HasSuffix(filename, ".mdown") ||
		strings.HasSuffix(filename, ".mkdown")
}

func generateBranchURL(r ghrepo.Interface, branch string) string {
	if branch == "" {
		return ghrepo.GenerateRepoURL(r, "")
	}

	return ghrepo.GenerateRepoURL(r, "tree/%s", url.QueryEscape(branch))
}
