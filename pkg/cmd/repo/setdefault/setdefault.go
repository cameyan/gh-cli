package base

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	ctx "context"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

func explainer() string {
	return heredoc.Doc(`
		此命令设置本地克隆仓库在查询 GitHub API 时使用的默认远程仓库。

		gh 在以下场景使用默认仓库:

		 - 查看和创建 PR
		 - 查看和创建 issue
		 - 查看和创建 release
		 - 使用 GitHub Actions

		### 注意: gh 在管理仓库和环境密钥时不使用默认仓库。`)
}

type iprompter interface {
	Select(string, string, []string) (int, error)
}

type SetDefaultOptions struct {
	IO         *iostreams.IOStreams
	Remotes    func() (context.Remotes, error)
	HttpClient func() (*http.Client, error)
	Prompter   iprompter
	GitClient  *git.Client

	Repo      ghrepo.Interface
	ViewMode  bool
	UnsetMode bool
}

func NewCmdSetDefault(f *cmdutil.Factory, runF func(*SetDefaultOptions) error) *cobra.Command {
	opts := &SetDefaultOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Remotes:    f.Remotes,
		Prompter:   f.Prompter,
		GitClient:  f.GitClient,
	}

	cmd := &cobra.Command{
		Use:   "set-default [<repository>]",
		Short: "为此目录配置默认仓库",
		Long:  explainer(),
		Example: heredoc.Doc(`
			# 交互式选择默认仓库
			$ gh repo set-default

			# 显式设置仓库
			$ gh repo set-default owner/repo

			# 使用 git 远程名称设置仓库
			$ gh repo set-default origin

			# 查看当前默认仓库
			$ gh repo set-default --view

			# 在交互式选择器中显示更多仓库选项
			$ git remote add newrepo https://github.com/owner/repo
			$ gh repo set-default
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if isLocal, err := opts.GitClient.IsLocalGitRepo(cmd.Context()); err != nil {
				return err
			} else if !isLocal {
				return errors.New("必须在 git 仓库内运行")
			}

			if len(args) > 0 {
				var err error
				opts.Repo, err = ghrepo.FromFullName(args[0])
				if err != nil {
					remotes, remoteErr := opts.Remotes()
					if remoteErr != nil {
						return remoteErr
					}

					remote, findErr := remotes.FindByName(args[0])
					if findErr != nil {
						return fmt.Errorf("给定参数不是有效的仓库或 git 远程: %w", err)
					}
					opts.Repo = remote.Repo
				}
			}

			if !opts.ViewMode && !opts.UnsetMode && !opts.IO.CanPrompt() && opts.Repo == nil {
				return cmdutil.FlagErrorf("非交互式运行时必须指定仓库")
			}

			if runF != nil {
				return runF(opts)
			}

			return setDefaultRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.ViewMode, "view", "v", false, "查看当前默认仓库")
	cmd.Flags().BoolVarP(&opts.UnsetMode, "unset", "u", false, "取消设置当前默认仓库")

	return cmd
}

func setDefaultRun(opts *SetDefaultOptions) error {
	remotes, err := opts.Remotes()
	if err != nil {
		return err
	}

	currentDefaultRepo, _ := remotes.ResolvedRemote()

	cs := opts.IO.ColorScheme()

	if opts.ViewMode {
		if currentDefaultRepo != nil {
			fmt.Fprintln(opts.IO.Out, displayRemoteRepoName(currentDefaultRepo))
		} else {
			fmt.Fprintf(opts.IO.ErrOut,
				"%s 尚未设置默认远程仓库。若要了解默认仓库的更多信息，请运行: gh repo set-default --help\n",
				cs.FailureIcon())
		}
		return nil
	}

	if opts.UnsetMode {
		var msg string
		if currentDefaultRepo != nil {
			if err := opts.GitClient.UnsetRemoteResolution(
				ctx.Background(), currentDefaultRepo.Name); err != nil {
				return err
			}
			msg = fmt.Sprintf("%s 已取消将 %s 设为默认仓库",
				cs.SuccessIcon(), ghrepo.FullName(currentDefaultRepo))
		} else {
			msg = "尚未设置默认仓库"
		}

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintln(opts.IO.Out, msg)
		}

		return nil
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	resolvedRemotes, err := context.ResolveRemotesToRepos(remotes, apiClient, "")
	if err != nil {
		return err
	}

	knownRepos, err := resolvedRemotes.NetworkRepos(0)
	if err != nil {
		return err
	}
	if len(knownRepos) == 0 {
		return errors.New("没有任何 git 远程对应有效的远程仓库")
	}

	var selectedRepo ghrepo.Interface

	if opts.Repo != nil {
		for _, knownRepo := range knownRepos {
			if ghrepo.IsSame(opts.Repo, knownRepo) {
				selectedRepo = opts.Repo
				break
			}
		}
		if selectedRepo == nil {
			return fmt.Errorf("%s 不对应任何 git 远程", ghrepo.FullName(opts.Repo))
		}
	}

	if selectedRepo == nil {
		if len(knownRepos) == 1 {
			selectedRepo = knownRepos[0]

			fmt.Fprintf(opts.IO.Out, "仅发现一个已知远程仓库，即 %s 上的 %s。\n",
				cs.Bold(ghrepo.FullName(selectedRepo)),
				cs.Bold(selectedRepo.RepoHost()))
		} else {
			var repoNames []string
			current := ""
			if currentDefaultRepo != nil {
				current = ghrepo.FullName(currentDefaultRepo)
			}

			for _, knownRepo := range knownRepos {
				repoNames = append(repoNames, ghrepo.FullName(knownRepo))
			}

			fmt.Fprintln(opts.IO.Out, explainer())
			fmt.Fprintln(opts.IO.Out)

			selected, err := opts.Prompter.Select("哪个仓库应作为默认仓库？", current, repoNames)
			if err != nil {
				return fmt.Errorf("无法显示提示: %w", err)
			}
			selectedName := repoNames[selected]

			owner, repo, _ := strings.Cut(selectedName, "/")
			selectedRepo = ghrepo.New(owner, repo)
		}
	}

	resolution := "base"
	selectedRemote, _ := resolvedRemotes.RemoteForRepo(selectedRepo)
	if selectedRemote == nil {
		sort.Stable(remotes)
		selectedRemote = remotes[0]
		resolution = ghrepo.FullName(selectedRepo)
	}

	if currentDefaultRepo != nil {
		if err := opts.GitClient.UnsetRemoteResolution(
			ctx.Background(), currentDefaultRepo.Name); err != nil {
			return err
		}
	}
	if err = opts.GitClient.SetRemoteResolution(
		ctx.Background(), selectedRemote.Name, resolution); err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "%s 已将 %s 设为当前目录的默认仓库\n", cs.SuccessIcon(), ghrepo.FullName(selectedRepo))
	}

	return nil
}

func displayRemoteRepoName(remote *context.Remote) string {
	if remote.Resolved == "" || remote.Resolved == "base" {
		return ghrepo.FullName(remote)
	}

	repo, err := ghrepo.FromFullName(remote.Resolved)
	if err != nil {
		return ghrepo.FullName(remote)
	}

	return ghrepo.FullName(repo)
}
