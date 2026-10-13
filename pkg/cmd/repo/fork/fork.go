package fork

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/repo/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const defaultRemoteName = "origin"

type iprompter interface {
	Confirm(string, bool) (bool, error)
}

type ForkOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Remotes    func() (ghContext.Remotes, error)
	Since      func(time.Time) time.Duration
	BackOff    backoff.BackOff
	Prompter   iprompter

	GitArgs           []string
	Repository        string
	Clone             bool
	Remote            bool
	PromptClone       bool
	PromptRemote      bool
	RemoteName        string
	Organization      string
	ForkName          string
	Rename            bool
	DefaultBranchOnly bool
}

type errWithExitCode interface {
	error
	ExitCode() int
}

// TODO warn about useless flags (--remote, --remote-name) when running from outside a repository
// TODO output over STDOUT not STDERR
// TODO remote-name has no effect on its own; error that or change behavior

func NewCmdFork(f *cmdutil.Factory, runF func(*ForkOptions) error) *cobra.Command {
	opts := &ForkOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
		BaseRepo:   f.BaseRepo,
		Remotes:    f.Remotes,
		Prompter:   f.Prompter,
		Since:      time.Since,
	}

	cmd := &cobra.Command{
		Use: "fork [<repository>] [-- <gitflags>...]",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() == 0 && len(args[1:]) > 0 {
				return cmdutil.FlagErrorf("传入 git clone 旗标时需要提供仓库参数")
			}
			return nil
		},
		Short: "创建仓库的派生(fork)",
		Long: heredoc.Docf(`
			创建仓库的派生(fork)。

			不带参数时, 为当前仓库创建派生; 否则, 为指定的
			仓库创建派生。

			默认情况下, 新的派生会被设为您的 %[1]sorigin%[1]s 远程, 并且任何现有的
			origin 远程会被重命名为 %[1]supstream%[1]s。若要更改此行为, 可以通过
			%[1]s--remote-name%[1]s 为新派生的远程设置名称。

			%[1]supstream%[1]s 远程将被设为默认远程仓库。

			可在 %[1]s--%[1]s 之后传入额外的 %[1]sgit clone%[1]s 旗标。
		`, "`"),
		Example: heredoc.Doc(`
			# 派生一个仓库
			$ gh repo fork owner/repo

			# 派生一个仓库并在本地克隆
			$ gh repo fork owner/repo --clone

			# 派生一个仓库但不克隆, 并跳过提示
			$ gh repo fork owner/repo --clone=false
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			promptOk := opts.IO.CanPrompt()
			if len(args) > 0 {
				opts.Repository = args[0]
				opts.GitArgs = args[1:]
			}

			if cmd.Flags().Changed("org") && opts.Organization == "" {
				return cmdutil.FlagErrorf("--org 不能为空")
			}

			if opts.RemoteName == "" {
				return cmdutil.FlagErrorf("--remote-name 不能为空")
			} else if !cmd.Flags().Changed("remote-name") {
				opts.Rename = true // 任何现有的 'origin' 都将被重命名为 upstream
			}

			if opts.Repository != "" && cmd.Flags().Changed("remote") {
				return cmdutil.FlagErrorf("提供了仓库参数时, 不支持 `--remote` 旗标")
			}

			if promptOk {
						// 若未指定, 则可以对它们进行提示。
				opts.PromptClone = !cmd.Flags().Changed("clone")
				opts.PromptRemote = !cmd.Flags().Changed("remote")
			}

			if runF != nil {
				return runF(opts)
			}
			return forkRun(opts)
		},
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if err == pflag.ErrHelp {
			return err
		}
		return cmdutil.FlagErrorf("%w\n请使用 `--` 分隔 git clone 旗标", err)
	})

	cmd.Flags().BoolVar(&opts.Clone, "clone", false, "克隆派生仓库")
	cmd.Flags().BoolVar(&opts.Remote, "remote", false, "为派生仓库添加 git 远程")
	cmd.Flags().StringVar(&opts.RemoteName, "remote-name", defaultRemoteName, "指定新远程的名称")
	cmd.Flags().StringVar(&opts.Organization, "org", "", "在组织中创建派生")
	cmd.Flags().StringVar(&opts.ForkName, "fork-name", "", "重命名派生仓库")
	cmd.Flags().BoolVar(&opts.DefaultBranchOnly, "default-branch-only", false, "派生中仅包含默认分支")

	return cmd
}

func forkRun(opts *ForkOptions) error {
	var repoToFork ghrepo.Interface
	var err error
	inParent := false // 是否正在为我们当前所处的仓库创建派生
	if opts.Repository == "" {
		baseRepo, err := opts.BaseRepo()
		if err != nil {
			return fmt.Errorf("无法确定基础仓库: %w", err)
		}
		inParent = true
		repoToFork = baseRepo
	} else {
		repoArg := opts.Repository

		if isURL(repoArg) {
			parsedURL, err := url.Parse(repoArg)
			if err != nil {
				return fmt.Errorf("无法识别参数: %w", err)
			}

			repoToFork, err = ghrepo.FromURL(parsedURL)
			if err != nil {
				return fmt.Errorf("无法识别参数: %w", err)
			}

		} else if strings.HasPrefix(repoArg, "git@") {
			parsedURL, err := git.ParseURL(repoArg)
			if err != nil {
				return fmt.Errorf("无法识别参数: %w", err)
			}
			repoToFork, err = ghrepo.FromURL(parsedURL)
			if err != nil {
				return fmt.Errorf("无法识别参数: %w", err)
			}
		} else {
			repoToFork, err = ghrepo.FromFullName(repoArg)
			if err != nil {
				return fmt.Errorf("参数错误: %w", err)
			}
		}
	}

	connectedToTerminal := opts.IO.IsStdoutTTY() && opts.IO.IsStderrTTY() && opts.IO.IsStdinTTY()

	cs := opts.IO.ColorScheme()
	stderr := opts.IO.ErrOut

	httpClient, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法创建客户端: %w", err)
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	opts.IO.StartProgressIndicator()
	forkedRepo, err := api.ForkRepo(apiClient, repoToFork, opts.Organization, opts.ForkName, opts.DefaultBranchOnly)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return fmt.Errorf("派生失败: %w", err)
	}

	// 这有点奇怪。通过 GitHub API 无法高效地判断给定用户是否已经派生过给定仓库。我们还注意到,
	// 创建派生的 API 端点即使派生已存在, 也会直接返回派生仓库数据 -- 状态码或其它内容均无变化。
	// 因此我们检查创建时间来判断仓库是否为全新创建; 若不是, 则假定派生已存在并报告错误。
	createdAgo := opts.Since(forkedRepo.CreatedAt)
	if createdAgo > time.Minute {
		if connectedToTerminal {
			fmt.Fprintf(stderr, "%s %s %s\n",
				cs.Yellow("!"),
				cs.Bold(ghrepo.FullName(forkedRepo)),
				"已存在")
		} else {
			fmt.Fprintf(stderr, "%s 已存在\n", ghrepo.FullName(forkedRepo))
		}
	} else {
		if connectedToTerminal {
			fmt.Fprintf(stderr, "%s 已创建派生 %s\n", cs.SuccessIconWithColor(cs.Green), cs.Bold(ghrepo.FullName(forkedRepo)))
		} else {
			fmt.Fprintln(opts.IO.Out, ghrepo.GenerateRepoURL(forkedRepo, ""))
		}
	}

	// Rename the new repo if necessary
	if opts.ForkName != "" && !strings.EqualFold(forkedRepo.RepoName(), shared.NormalizeRepoName(opts.ForkName)) {
		forkedRepo, err = api.RenameRepo(apiClient, forkedRepo, opts.ForkName)
		if err != nil {
			return fmt.Errorf("重命名派生失败: %w", err)
		}
		if connectedToTerminal {
			fmt.Fprintf(stderr, "%s 已将派生重命名为 %s\n", cs.SuccessIconWithColor(cs.Green), cs.Bold(ghrepo.FullName(forkedRepo)))
		}
	}

	if (inParent && (!opts.Remote && !opts.PromptRemote)) || (!inParent && (!opts.Clone && !opts.PromptClone)) {
		return nil
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	protocolConfig := cfg.GitProtocol(repoToFork.RepoHost())
	protocolIsConfiguredByUser := protocolConfig.Source == gh.ConfigUserProvided
	protocol := protocolConfig.Value

	gitClient := opts.GitClient
	ctx := context.Background()

	if inParent {
		remotes, err := opts.Remotes()
		if err != nil {
			return err
		}

		if !protocolIsConfiguredByUser {
			if remote, err := remotes.FindByRepo(repoToFork.RepoOwner(), repoToFork.RepoName()); err == nil {
				scheme := ""
				if remote.FetchURL != nil {
					scheme = remote.FetchURL.Scheme
				}
				if remote.PushURL != nil {
					scheme = remote.PushURL.Scheme
				}
				if scheme != "" {
					protocol = scheme
				} else {
					protocol = "https"
				}
			}
		}

		if remote, err := remotes.FindByRepo(forkedRepo.RepoOwner(), forkedRepo.RepoName()); err == nil {
			if connectedToTerminal {
				fmt.Fprintf(stderr, "%s 使用现有远程 %s\n", cs.SuccessIcon(), cs.Bold(remote.Name))
			}
			return nil
		}

		remoteDesired := opts.Remote
		if opts.PromptRemote {
			remoteDesired, err = opts.Prompter.Confirm("是否要为派生仓库添加远程?", false)
			if err != nil {
				return err
			}
		}

		if remoteDesired {
			remoteName := opts.RemoteName
			remotes, err := opts.Remotes()
			if err != nil {
				return err
			}

			if _, err := remotes.FindByName(remoteName); err == nil {
				if opts.Rename {
					renameTarget := "upstream"
					renameCmd, err := gitClient.Command(ctx, "remote", "rename", remoteName, renameTarget)
					if err != nil {
						return err
					}
					_, err = renameCmd.Output()
					if err != nil {
						return err
					}

					if connectedToTerminal {
						fmt.Fprintf(stderr, "%s 已将远程 %s 重命名为 %s\n", cs.SuccessIcon(), cs.Bold(remoteName), cs.Bold(renameTarget))
					}
				} else {
					return fmt.Errorf("名为 '%s' 的 git 远程已存在", remoteName)
				}
			}

			forkedRepoCloneURL := ghrepo.FormatRemoteURL(forkedRepo, protocol)

			_, err = gitClient.AddRemote(ctx, remoteName, forkedRepoCloneURL, []string{})
			if err != nil {
				return fmt.Errorf("添加远程失败: %w", err)
			}

			if connectedToTerminal {
				fmt.Fprintf(stderr, "%s 已添加远程 %s\n", cs.SuccessIcon(), cs.Bold(remoteName))
			}
		}
	} else {
		cloneDesired := opts.Clone
		if opts.PromptClone {
			cloneDesired, err = opts.Prompter.Confirm("是否要克隆派生仓库?", false)
			if err != nil {
				return err
			}
		}
		if cloneDesired {
			// 允许在测试中注入替代的 BackOff。
			if opts.BackOff == nil {
				bo := backoff.NewConstantBackOff(2 * time.Second)
				opts.BackOff = bo
			}

			cloneDir, err := backoff.RetryWithData(func() (string, error) {
				forkedRepoURL := ghrepo.FormatRemoteURL(forkedRepo, protocol)
				dir, err := gitClient.Clone(ctx, forkedRepoURL, opts.GitArgs)
				if err == nil {
					return dir, nil
				}
				var execError errWithExitCode
				if errors.As(err, &execError) && execError.ExitCode() == 128 {
					return "", err
				}
				return "", backoff.Permanent(err)
			}, backoff.WithContext(backoff.WithMaxRetries(opts.BackOff, 3), ctx))

			if err != nil {
				return fmt.Errorf("克隆派生仓库失败: %w", err)
			}

			gc := gitClient.Copy()
			gc.RepoDir = cloneDir
			upstreamURL := ghrepo.FormatRemoteURL(repoToFork, protocol)
			upstreamRemote := "upstream"

			if _, err := gc.AddRemote(ctx, upstreamRemote, upstreamURL, []string{}); err != nil {
				return err
			}

			if err := gc.SetRemoteResolution(ctx, upstreamRemote, "base"); err != nil {
				return err
			}

			if err := gc.Fetch(ctx, upstreamRemote, ""); err != nil {
				return err
			}

			if connectedToTerminal {
				fmt.Fprintf(stderr, "%s 已克隆派生仓库\n", cs.SuccessIcon())
				fmt.Fprintf(stderr, "%s 仓库 %s 已被设为默认仓库。要了解默认仓库的更多信息, 请运行: gh repo set-default --help\n", cs.WarningIcon(), cs.Bold(ghrepo.FullName(repoToFork)))
			}
		}
	}

	return nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http:/") || strings.HasPrefix(s, "https:/")
}
