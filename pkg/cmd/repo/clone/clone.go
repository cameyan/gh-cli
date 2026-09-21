package clone

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type CloneOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams

	GitArgs      []string
	Repository   string
	UpstreamName string
	NoUpstream   bool
}

func NewCmdClone(f *cmdutil.Factory, runF func(*CloneOptions) error) *cobra.Command {
	opts := &CloneOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		DisableFlagsInUseLine: true,

		Use:   "clone <repository> [<directory>] [-- <gitflags>...]",
		Args:  cmdutil.MinimumArgs(1, "无法克隆: 需要提供仓库参数"),
		Short: "在本地克隆仓库",
		Long: heredoc.Docf(`
			在本地克隆 GitHub 仓库。可在 %[1]s--%[1]s 之后列出
			额外的 %[1]sgit clone%[1]s 旗标。

			若 %[1]sOWNER/REPO%[1]s 仓库参数中的 %[1]sOWNER/%[1]s 部分被省略,
			则默认为当前认证用户的名称。

			当仓库参数中未提供协议方案(scheme)时, 将从您的配置中选择 %[1]sgit_protocol%[1]s,
			可通过 %[1]sgh config get git_protocol%[1]s 查看。若提供了协议
			方案, 则将使用指定的协议克隆仓库。

			若该仓库是一个派生(fork), 其父仓库将被添加为一个额外的
			名为 %[1]supstream%[1]s 的 git 远程。远程名称可通过 %[1]s--upstream-remote-name%[1]s 配置。
			%[1]s--upstream-remote-name%[1]s 选项支持 %[1]s@owner%[1]s 值, 它将以父仓库的所有者命名
			该远程。

			若该仓库是一个派生(fork), 其父仓库将被设为默认远程仓库。
			若要跳过此行为, 请使用 %[1]s--no-upstream%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 从指定的组织克隆仓库
			$ gh repo clone cli/cli

			# 从您自己的账号克隆仓库
			$ gh repo clone myrepo

			# 克隆仓库, 并覆盖 git 协议配置
			$ gh repo clone https://github.com/cli/cli
			$ gh repo clone git@github.com:cli/cli.git

			# 将仓库克隆到自定义目录
			$ gh repo clone cli/cli workspace/cli

			# 使用额外的 git clone 旗标克隆仓库
			$ gh repo clone cli/cli -- --depth=1

			# 克隆派生(fork)仓库, 且不添加 upstream 远程
			$ gh repo clone myfork --no-upstream
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Repository = args[0]
			opts.GitArgs = args[1:]

			if runF != nil {
				return runF(opts)
			}

			return cloneRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.UpstreamName, "upstream-remote-name", "u", "upstream", "克隆派生(fork)仓库时的上游(upstream)远程名称")
	cmd.Flags().BoolVar(&opts.NoUpstream, "no-upstream", false, "克隆派生(fork)仓库时不添加上游(upstream)远程")
	cmd.MarkFlagsMutuallyExclusive("upstream-remote-name", "no-upstream")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if err == pflag.ErrHelp {
			return err
		}
		return cmdutil.FlagErrorf("%w\n请使用 '--' 分隔 git clone 旗标。", err)
	})

	return cmd
}

func cloneRun(opts *CloneOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	repositoryIsURL := strings.Contains(opts.Repository, ":")
	repositoryIsFullName := !repositoryIsURL && strings.Contains(opts.Repository, "/")

	var repo ghrepo.Interface
	var protocol string

	if repositoryIsURL {
		initialURL, err := git.ParseURL(opts.Repository)
		if err != nil {
			return err
		}

		repoURL := simplifyURL(initialURL)
		repo, err = ghrepo.FromURL(repoURL)
		if err != nil {
			return err
		}
		if repoURL.Scheme == "git+ssh" {
			repoURL.Scheme = "ssh"
		}
		protocol = repoURL.Scheme
	} else {
		var fullName string
		if repositoryIsFullName {
			fullName = opts.Repository
		} else {
			host, _ := cfg.Authentication().DefaultHost()
			currentUser, err := api.CurrentLoginName(apiClient, host)
			if err != nil {
				return err
			}
			fullName = currentUser + "/" + opts.Repository
		}

		repo, err = ghrepo.FromFullName(fullName)
		if err != nil {
			return err
		}

		protocol = cfg.GitProtocol(repo.RepoHost()).Value
	}

	wantsWiki := strings.HasSuffix(repo.RepoName(), ".wiki")
	if wantsWiki {
		repoName := strings.TrimSuffix(repo.RepoName(), ".wiki")
		repo = ghrepo.NewWithHost(repo.RepoOwner(), repoName, repo.RepoHost())
	}

	// 从 API 加载仓库, 以获取规范大小写形式的用户名/仓库名称
	canonicalRepo, err := api.GitHubRepo(apiClient, repo)
	if err != nil {
		return err
	}
	canonicalCloneURL, err := remoteURL(canonicalRepo, protocol)
	if err != nil {
		return err
	}

	// 若仓库启用了维基(wiki)且 wantsWiki 为 true, 则创建一个新的克隆 URL
	if wantsWiki {
		if !canonicalRepo.HasWikiEnabled {
			return fmt.Errorf("仓库 '%s' 没有维基(wiki)", ghrepo.FullName(canonicalRepo))
		}
		canonicalCloneURL = strings.TrimSuffix(canonicalCloneURL, ".git") + ".wiki.git"
	}

	gitClient := opts.GitClient
	ctx := context.Background()
	cloneDir, err := gitClient.Clone(ctx, canonicalCloneURL, opts.GitArgs)
	if err != nil {
		return err
	}

	// 若该仓库是一个派生(fork), 则将父仓库添加为 upstream 远程, 并将其设为默认仓库。
	if canonicalRepo.Parent != nil {
		gc := gitClient.Copy()
		gc.RepoDir = cloneDir

		if opts.NoUpstream {
			if err := gc.SetRemoteResolution(ctx, "origin", "base"); err != nil {
				return err
			}
		} else {
			protocol := cfg.GitProtocol(canonicalRepo.Parent.RepoHost()).Value
			upstreamURL, err := remoteURL(canonicalRepo.Parent, protocol)
			if err != nil {
				return err
			}

			upstreamName := opts.UpstreamName
			if opts.UpstreamName == "@owner" {
				upstreamName = canonicalRepo.Parent.RepoOwner()
			}

			if _, err := gc.AddRemote(ctx, upstreamName, upstreamURL, []string{canonicalRepo.Parent.DefaultBranchRef.Name}); err != nil {
				return err
			}

			if err := gc.Fetch(ctx, upstreamName, ""); err != nil {
				return err
			}

			if err := gc.SetRemoteBranches(ctx, upstreamName, `*`); err != nil {
				return err
			}

			if err := gc.SetRemoteResolution(ctx, upstreamName, "base"); err != nil {
				return err
			}

			connectedToTerminal := opts.IO.IsStdoutTTY()
			if connectedToTerminal {
				cs := opts.IO.ColorScheme()
				fmt.Fprintf(opts.IO.ErrOut, "%s 仓库 %s 已被设为默认仓库。要了解默认仓库的更多信息, 请运行: gh repo set-default --help\n", cs.WarningIcon(), cs.Bold(ghrepo.FullName(canonicalRepo.Parent)))
			}
		}
	}
	return nil
}

func remoteURL(repo *api.Repository, protocol string) (string, error) {
	if protocol != "ssh" {
		return ghrepo.FormatRemoteURL(repo, protocol), nil
	}
	if repo.SSHURL == "" {
		return "", fmt.Errorf("无效的 API 响应: 仓库 %s 缺少 sshUrl", ghrepo.FullName(repo))
	}
	return repo.SSHURL, nil
}

// simplifyURL 去除给定 URL 中多余的部分, 例如多余的路径段(即
// `/owner/repo` 之外的任何内容), 查询字符串或片段。此函数
// 从不返回错误。
//
// 此函数的设计初衷是让用户可以使用与仓库相关的任意 URL 来克隆仓库, 例如:
//   - (目录树)              github.com/owner/repo/blob/main/foo/bar
//   - (行级深链接)          github.com/owner/repo/blob/main/foo/bar#L168
//   - (议题/PR 评论)        github.com/owner/repo/pull/999#issue-9999999999
//   - (提交历史)            github.com/owner/repo/commits/main/?author=foo
func simplifyURL(u *url.URL) *url.URL {
	result := &url.URL{
		Scheme: u.Scheme,
		User:   u.User,
		Host:   u.Host,
		Path:   u.Path,
	}

	pathParts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3)
	if len(pathParts) <= 2 {
		return result
	}

	result.Path = strings.Join(pathParts[0:2], "/")
	return result
}
