package browse

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	emptyCommitFlag = "last"
)

type BrowseOptions struct {
	BaseRepo         func() (ghrepo.Interface, error)
	Browser          browser.Browser
	HttpClient       func() (*http.Client, error)
	IO               *iostreams.IOStreams
	PathFromRepoRoot func() string
	GitClient        gitClient

	SelectorArg string

	Branch          string
	Commit          string
	ProjectsFlag    bool
	ReleasesFlag    bool
	SettingsFlag    bool
	WikiFlag        bool
	ActionsFlag     bool
	BlameFlag       bool
	NoBrowserFlag   bool
	HasRepoOverride bool
}

func NewCmdBrowse(f *cmdutil.Factory, runF func(*BrowseOptions) error) *cobra.Command {
	opts := &BrowseOptions{
		Browser:    f.Browser,
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
		PathFromRepoRoot: func() string {
			return f.GitClient.PathFromRoot(context.Background())
		},
		GitClient: &localGitClient{client: f.GitClient},
	}

	cmd := &cobra.Command{
		Short: "在浏览器中打开仓库、议题(issue)、拉取请求(pull request)等页面",
		Long: heredoc.Doc(`
			从终端切换到 Web 浏览器，查看以下内容并与之交互:

			- 议题(issue)
			- 拉取请求(pull request)
			- 仓库内容
			- 仓库主页
			- 仓库设置
		`),
		Use:  "browse [<number> | <path> | <commit-sha>]",
		Args: cobra.MaximumNArgs(1),
		Example: heredoc.Doc(`
			# 打开当前仓库的主页
			$ gh browse

			# 打开当前仓库的 script 目录
			$ gh browse script/

			# 打开编号为 217 的议题(issue)或拉取请求(pull request)
			$ gh browse 217

			# 打开提交(commit)页面
			$ gh browse 77507cd94ccafcf568f8560cfecde965fcfa63

			# 打开仓库设置
			$ gh browse --settings

			# 打开 main.go 并定位到第 312 行
			$ gh browse main.go:312

			# 打开 main.go 的 blame 视图并定位到第 312 行
			$ gh browse main.go:312 --blame

			# 在 bug-fix 分支的最新位置打开 main.go
			$ gh browse main.go --branch bug-fix

			# 在提交(commit) 775007cd 处打开 main.go
			$ gh browse main.go --commit=77507cd94ccafcf568f8560cfecde965fcfa63
		`),
		Annotations: map[string]string{
			"help:arguments": heredoc.Doc(`
				可以通过参数指定浏览器打开的位置，支持以下格式:
				- 使用编号(number)指定议题(issue)或拉取请求(pull request)，例如 "123"; 或
				- 使用路径(path)打开文件夹和文件，例如 "cmd/gh/main.go"; 或
				- 使用提交 SHA(commit SHA)
			`),
			"help:environment": heredoc.Doc(`
				如需配置默认浏览器以外的 Web 浏览器，请使用 BROWSER 环境变量。
			`),
		},
		GroupID: "core",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if err := cmdutil.MutuallyExclusive(
				"使用 `--projects`、`--releases`、`--settings`、`--actions` 或 `--wiki` 时不支持位置参数",
				opts.SelectorArg != "",
				opts.ProjectsFlag,
				opts.ReleasesFlag,
				opts.SettingsFlag,
				opts.WikiFlag,
				opts.ActionsFlag,
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"`--branch`、`--commit`、`--projects`、`--releases`、`--settings`、`--actions` 和 `--wiki` 中最多只能指定一个",
				opts.Branch != "",
				opts.Commit != "",
				opts.ProjectsFlag,
				opts.ReleasesFlag,
				opts.SettingsFlag,
				opts.WikiFlag,
				opts.ActionsFlag,
			); err != nil {
				return err
			}

			if opts.BlameFlag && opts.SelectorArg == "" {
				return cmdutil.FlagErrorf("`--blame` 需要一个文件路径参数")
			}

			if (isNumber(opts.SelectorArg) || isCommit(opts.SelectorArg)) && (opts.Branch != "" || opts.Commit != "") {
				return cmdutil.FlagErrorf("使用 `--branch` 或 `--commit` 时，%q 是无效参数", opts.SelectorArg)
			}

			if cmd.Flags().Changed("repo") || os.Getenv("GH_REPO") != "" {
				opts.GitClient = &remoteGitClient{opts.BaseRepo, opts.HttpClient}
				opts.HasRepoOverride = true
			}

			if runF != nil {
				return runF(opts)
			}
			return runBrowse(opts)
		},
	}

	cmdutil.EnableRepoOverride(cmd, f)
	cmd.Flags().BoolVarP(&opts.ProjectsFlag, "projects", "p", false, "打开仓库的项目(projects)页面")
	cmd.Flags().BoolVarP(&opts.ReleasesFlag, "releases", "r", false, "打开仓库的发布(releases)页面")
	cmd.Flags().BoolVarP(&opts.WikiFlag, "wiki", "w", false, "打开仓库的维基(wiki)页面")
	cmd.Flags().BoolVarP(&opts.ActionsFlag, "actions", "a", false, "打开仓库的动作(actions)页面")
	cmd.Flags().BoolVarP(&opts.SettingsFlag, "settings", "s", false, "打开仓库的设置(settings)页面")
	cmd.Flags().BoolVar(&opts.BlameFlag, "blame", false, "打开文件的 blame 视图")
	cmd.Flags().BoolVarP(&opts.NoBrowserFlag, "no-browser", "n", false, "打印目标 URL 而不打开浏览器")
	cmd.Flags().StringVarP(&opts.Commit, "commit", "c", "", "通过传入提交 SHA(commit SHA)选择其他提交(commit)，默认为最后一次提交")
	cmd.Flags().StringVarP(&opts.Branch, "branch", "b", "", "通过传入分支名称选择其他分支")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "branch")

	// 保持向后兼容: commit 标志曾经是一个布尔标志。
	cmd.Flags().Lookup("commit").NoOptDefVal = emptyCommitFlag

	return cmd
}

func runBrowse(opts *BrowseOptions) error {
	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return fmt.Errorf("无法确定基础仓库: %w", err)
	}

	if opts.Commit != "" && opts.Commit == emptyCommitFlag {
		commit, err := opts.GitClient.LastCommit()
		if err != nil {
			return err
		}
		opts.Commit = commit.Sha
	}

	section, err := parseSection(baseRepo, opts)
	if err != nil {
		return err
	}
	url := ghrepo.GenerateRepoURL(baseRepo, "%s", section)

	if opts.NoBrowserFlag {
		client, err := opts.HttpClient()
		if err != nil {
			return err
		}

		exist, err := api.RepoExists(api.NewClientFromHTTP(client), baseRepo)
		if err != nil {
			return err
		}
		if !exist {
			return fmt.Errorf("%s 不存在", text.DisplayURL(url))
		}
		_, err = fmt.Fprintln(opts.IO.Out, url)
		return err
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s。\n", text.DisplayURL(url))
	}
	return opts.Browser.Browse(url)
}

func parseSection(baseRepo ghrepo.Interface, opts *BrowseOptions) (string, error) {
	if opts.ProjectsFlag {
		return "projects", nil
	} else if opts.ReleasesFlag {
		return "releases", nil
	} else if opts.SettingsFlag {
		return "settings", nil
	} else if opts.WikiFlag {
		return "wiki", nil
	} else if opts.ActionsFlag {
		return "actions", nil
	}

	ref := opts.Branch
	if opts.Commit != "" {
		ref = opts.Commit
	}

	if ref == "" {
		if opts.SelectorArg == "" {
			return "", nil
		}
		if isNumber(opts.SelectorArg) {
			return fmt.Sprintf("issues/%s", strings.TrimPrefix(opts.SelectorArg, "#")), nil
		}
		if isCommit(opts.SelectorArg) {
			return fmt.Sprintf("commit/%s", opts.SelectorArg), nil
		}
	}

	if ref == "" {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return "", err
		}
		apiClient := api.NewClientFromHTTP(httpClient)
		ref, err = api.RepoDefaultBranch(apiClient, baseRepo)
		if err != nil {
			return "", fmt.Errorf("确定默认分支时出错: %w", err)
		}
	}

	filePath, rangeStart, rangeEnd, err := parseFile(*opts, opts.SelectorArg)
	if err != nil {
		return "", err
	}

	if rangeStart > 0 {
		var rangeFragment string
		if rangeEnd > 0 && rangeStart != rangeEnd {
			rangeFragment = fmt.Sprintf("L%d-L%d", rangeStart, rangeEnd)
		} else {
			rangeFragment = fmt.Sprintf("L%d", rangeStart)
		}
		if opts.BlameFlag {
			return fmt.Sprintf("blame/%s/%s#%s", escapePath(ref), escapePath(filePath), rangeFragment), nil
		}
		return fmt.Sprintf("blob/%s/%s?plain=1#%s", escapePath(ref), escapePath(filePath), rangeFragment), nil
	}

	if opts.BlameFlag {
		return fmt.Sprintf("blame/%s/%s", escapePath(ref), escapePath(filePath)), nil
	}

	return strings.TrimSuffix(fmt.Sprintf("tree/%s/%s", escapePath(ref), escapePath(filePath)), "/"), nil
}

// escapePath 对特殊字符进行 URL 编码，但保留斜杠不变
func escapePath(p string) string {
	return strings.ReplaceAll(url.PathEscape(p), "%2F", "/")
}

func parseFile(opts BrowseOptions, f string) (p string, start int, end int, err error) {
	if f == "" {
		return
	}

	parts := strings.SplitN(f, ":", 3)
	if len(parts) > 2 {
		err = fmt.Errorf("无效的文件参数: %q", f)
		return
	}

	p = filepath.ToSlash(parts[0])
	if !path.IsAbs(p) && !opts.HasRepoOverride {
		p = path.Join(opts.PathFromRepoRoot(), p)
		if p == "." || strings.HasPrefix(p, "..") {
			p = ""
		}
	}
	if len(parts) < 2 {
		return
	}

	if idx := strings.IndexRune(parts[1], '-'); idx >= 0 {
		start, err = strconv.Atoi(parts[1][:idx])
		if err != nil {
			err = fmt.Errorf("无效的文件参数: %q", f)
			return
		}
		end, err = strconv.Atoi(parts[1][idx+1:])
		if err != nil {
			err = fmt.Errorf("无效的文件参数: %q", f)
		}
		return
	}

	start, err = strconv.Atoi(parts[1])
	if err != nil {
		err = fmt.Errorf("无效的文件参数: %q", f)
	}
	end = start
	return
}

func isNumber(arg string) bool {
	_, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	return err == nil
}

// 支持 sha1 和 sha256
var commitHash = regexp.MustCompile(`\A[a-f0-9]{7,64}\z`)

func isCommit(arg string) bool {
	return commitHash.MatchString(arg)
}

// gitClient 用于实现对本地和远程 git 仓库均可执行的操作
type gitClient interface {
	LastCommit() (*git.Commit, error)
}

type localGitClient struct {
	client *git.Client
}

type remoteGitClient struct {
	repo       func() (ghrepo.Interface, error)
	httpClient func() (*http.Client, error)
}

func (gc *localGitClient) LastCommit() (*git.Commit, error) {
	return gc.client.LastCommit(context.Background())
}

func (gc *remoteGitClient) LastCommit() (*git.Commit, error) {
	httpClient, err := gc.httpClient()
	if err != nil {
		return nil, err
	}
	repo, err := gc.repo()
	if err != nil {
		return nil, err
	}
	commit, err := api.LastCommit(api.NewClientFromHTTP(httpClient), repo)
	if err != nil {
		return nil, err
	}
	return &git.Commit{Sha: commit.OID}, nil
}
