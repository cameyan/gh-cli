package create

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/repo/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type errWithExitCode interface {
	error
	ExitCode() int
}

type iprompter interface {
	Input(string, string) (string, error)
	Select(string, string, []string) (int, error)
	Confirm(string, bool) (bool, error)
}

type CreateOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	Prompter   iprompter
	BackOff    backoff.BackOff

	Name               string
	Description        string
	Homepage           string
	Team               string
	Template           string
	Public             bool
	Private            bool
	Internal           bool
	Visibility         string
	Push               bool
	Clone              bool
	Source             string
	Remote             string
	GitIgnoreTemplate  string
	LicenseTemplate    string
	DisableIssues      bool
	DisableWiki        bool
	Interactive        bool
	IncludeAllBranches bool
	AddReadme          bool
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
	}

	var enableIssues bool
	var enableWiki bool

	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a new repository",
		Long: heredoc.Docf(`
			创建一个新的 GitHub 仓库。

			若要以交互方式创建仓库, 使用不带参数的 %[1]sgh repo create%[1]s。

			若要以非交互方式创建远程仓库, 请提供仓库名称, 以及 %[1]s--public%[1]s、%[1]s--private%[1]s 或 %[1]s--internal%[1]s 之一。
			传入 %[1]s--clone%[1]s 可将新仓库克隆到本地。

			若 %[1]sOWNER/REPO%[1]s 名称参数中的 %[1]sOWNER/%[1]s 部分被省略,
			则默认为当前认证用户的名称。

			若要从现有的本地仓库创建远程仓库, 请通过 %[1]s--source%[1]s 指定源目录。
			默认情况下, 远程仓库名称将与源目录名称相同。

			传入 %[1]s--push%[1]s 可将本地提交推送到新仓库。若仓库是裸仓库(bare), 则会镜像所有引用(refs)。

			可与 %[1]s--gitignore%[1]s 搭配使用的语言或平台 .gitignore 模板, 请见 <https://github.com/github/gitignore>。

			可与 %[1]s--license%[1]s 搭配使用的许可证关键字, 请运行 %[1]sgh repo license list%[1]s 或访问 <https://choosealicense.com>。

			仓库将使用已配置的仓库默认分支创建, 参见 <https://docs.github.com/en/account-and-profile/setting-up-and-managing-your-personal-account-on-github/managing-user-account-settings/managing-the-default-branch-name-for-your-repositories>.
		`, "`"),
		Example: heredoc.Doc(`
			# 以交互方式创建仓库
			$ gh repo create

			# 创建一个新的远程仓库并克隆到本地
			$ gh repo create my-project --public --clone

			# 在其他组织中创建一个新的远程仓库
			$ gh repo create my-org/my-project --public

			# 从当前目录创建远程仓库
			$ gh repo create my-project --private --source=. --remote=upstream
		`),
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.Name = args[0]
			}

			if len(args) == 0 && cmd.Flags().NFlag() == 0 {
				if !opts.IO.CanPrompt() {
					return cmdutil.FlagErrorf("非交互模式下至少需要一个参数")
				}
				opts.Interactive = true
			} else {
				// exactly one visibility flag required
				if !opts.Public && !opts.Private && !opts.Internal {
					return cmdutil.FlagErrorf("非交互模式下运行时, 需要 `--public`、`--private` 或 `--internal` 之一")
				}
				err := cmdutil.MutuallyExclusive(
					"`--public`、`--private` 或 `--internal` 中只能提供一个",
					opts.Public, opts.Private, opts.Internal)
				if err != nil {
					return err
				}

				if opts.Public {
					opts.Visibility = "PUBLIC"
				} else if opts.Private {
					opts.Visibility = "PRIVATE"
				} else {
					opts.Visibility = "INTERNAL"
				}
			}

			if opts.Source == "" {
				if opts.Remote != "" {
					return cmdutil.FlagErrorf("`--remote` 选项只能与 `--source` 搭配使用")
				}
				if opts.Push {
					return cmdutil.FlagErrorf("`--push` 选项只能与 `--source` 搭配使用")
				}
				if opts.Name == "" && !opts.Interactive {
					return cmdutil.FlagErrorf("创建新的远程仓库需要提供名称参数")
				}

			} else if opts.Clone || opts.GitIgnoreTemplate != "" || opts.LicenseTemplate != "" || opts.Template != "" {
				return cmdutil.FlagErrorf("`--source` 选项不支持与 `--clone`、`--template`、`--license` 或 `--gitignore` 搭配使用")
			}

			if opts.Template != "" && (opts.GitIgnoreTemplate != "" || opts.LicenseTemplate != "") {
				return cmdutil.FlagErrorf("提供了模板时, 不会添加 .gitignore 和许可证模板")
			}

			if opts.Template != "" && opts.AddReadme {
				return cmdutil.FlagErrorf("`--add-readme` 选项不支持与 `--template` 搭配使用")
			}

			if cmd.Flags().Changed("enable-issues") {
				opts.DisableIssues = !enableIssues
			}
			if cmd.Flags().Changed("enable-wiki") {
				opts.DisableWiki = !enableWiki
			}
			if opts.Template != "" && opts.Team != "" {
				return cmdutil.FlagErrorf("`--template` 选项不支持与 `--team` 搭配使用")
			}

			if opts.Template == "" && opts.IncludeAllBranches {
				return cmdutil.FlagErrorf("`--include-all-branches` 选项仅在使用 `--template` 时受支持")
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Description, "description", "d", "", "仓库描述")
	cmd.Flags().StringVarP(&opts.Homepage, "homepage", "h", "", "仓库主页 `URL`")
	cmd.Flags().StringVarP(&opts.Team, "team", "t", "", "授予访问权限的组织团队 `name`")
	cmd.Flags().StringVarP(&opts.Template, "template", "p", "", "基于模板 `repository` 创建新仓库")
	cmd.Flags().BoolVar(&opts.Public, "public", false, "将新仓库设为公开")
	cmd.Flags().BoolVar(&opts.Private, "private", false, "将新仓库设为私有")
	cmd.Flags().BoolVar(&opts.Internal, "internal", false, "将新仓库设为内部")
	cmd.Flags().StringVarP(&opts.GitIgnoreTemplate, "gitignore", "g", "", "为仓库指定 gitignore 模板")
	cmd.Flags().StringVarP(&opts.LicenseTemplate, "license", "l", "", "为仓库指定开源许可证")
	cmd.Flags().StringVarP(&opts.Source, "source", "s", "", "指定用作源的本地仓库路径")
	cmd.Flags().StringVarP(&opts.Remote, "remote", "r", "", "为新仓库指定远程名称")
	cmd.Flags().BoolVar(&opts.Push, "push", false, "将本地提交推送到新仓库")
	cmd.Flags().BoolVarP(&opts.Clone, "clone", "c", false, "将新仓库克隆到当前目录")
	cmd.Flags().BoolVar(&opts.DisableIssues, "disable-issues", false, "在新仓库中禁用议题(issues)")
	cmd.Flags().BoolVar(&opts.DisableWiki, "disable-wiki", false, "在新仓库中禁用维基(wiki)")
	cmd.Flags().BoolVar(&opts.IncludeAllBranches, "include-all-branches", false, "包含模板仓库中的所有分支")
	cmd.Flags().BoolVar(&opts.AddReadme, "add-readme", false, "为新仓库添加 README 文件")

	// deprecated flags
	cmd.Flags().BoolP("confirm", "y", false, "跳过确认提示")
	cmd.Flags().BoolVar(&enableIssues, "enable-issues", true, "在新仓库中启用议题(issues)")
	cmd.Flags().BoolVar(&enableWiki, "enable-wiki", true, "在新仓库中启用维基(wiki)")

	_ = cmd.Flags().MarkDeprecated("confirm", "传入任意参数以跳过确认提示")
	_ = cmd.Flags().MarkDeprecated("enable-issues", "使用 `--disable-issues` 禁用议题")
	_ = cmd.Flags().MarkDeprecated("enable-wiki", "使用 `--disable-wiki` 禁用维基")

	_ = cmd.RegisterFlagCompletionFunc("gitignore", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		cfg, err := opts.Config()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		hostname, _ := cfg.Authentication().DefaultHost()
		results, err := api.RepoGitIgnoreTemplates(httpClient, hostname)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return results, cobra.ShellCompDirectiveNoFileComp
	})

	_ = cmd.RegisterFlagCompletionFunc("license", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		cfg, err := opts.Config()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		hostname, _ := cfg.Authentication().DefaultHost()
		licenses, err := api.RepoLicenses(httpClient, hostname)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var results []string
		for _, license := range licenses {
			results = append(results, fmt.Sprintf("%s\t%s", license.Key, license.Name))
		}
		return results, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

func createRun(opts *CreateOptions) error {
	if opts.Interactive {
		cfg, err := opts.Config()
		if err != nil {
			return err
		}
		host, _ := cfg.Authentication().DefaultHost()
		answer, err := opts.Prompter.Select("您想做什么?", "", []string{
			fmt.Sprintf("在 %s 上从零开始创建新仓库", host),
			fmt.Sprintf("在 %s 上从模板仓库创建新仓库", host),
			fmt.Sprintf("将现有的本地仓库推送到 %s", host),
		})
		if err != nil {
			return err
		}
		switch answer {
		case 0:
			return createFromScratch(opts)
		case 1:
			return createFromTemplate(opts)
		case 2:
			return createFromLocal(opts)
		}
	}

	if opts.Source == "" {
		return createFromScratch(opts)
	}
	return createFromLocal(opts)
}

// 在远程主机上创建新仓库
func createFromScratch(opts *CreateOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	var repoToCreate ghrepo.Interface
	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	if opts.Interactive {
		opts.Name, opts.Description, opts.Visibility, err = interactiveRepoInfo(httpClient, host, opts.Prompter, "")
		if err != nil {
			return err
		}
		opts.AddReadme, err = opts.Prompter.Confirm("您想添加 README 文件吗?", false)
		if err != nil {
			return err
		}
		opts.GitIgnoreTemplate, err = interactiveGitIgnore(httpClient, host, opts.Prompter)
		if err != nil {
			return err
		}
		opts.LicenseTemplate, err = interactiveLicense(httpClient, host, opts.Prompter)
		if err != nil {
			return err
		}

		targetRepo := shared.NormalizeRepoName(opts.Name)
		if idx := strings.IndexRune(opts.Name, '/'); idx > 0 {
			targetRepo = opts.Name[0:idx+1] + shared.NormalizeRepoName(opts.Name[idx+1:])
		}
		confirmed, err := opts.Prompter.Confirm(
			fmt.Sprintf(`将在 %s 上以 %s 可见性创建 "%s"。是否继续?`, host, strings.ToLower(opts.Visibility), targetRepo),
			true)
		if err != nil {
			return err
		} else if !confirmed {
			return cmdutil.CancelError
		}
	}

	if strings.Contains(opts.Name, "/") {
		var err error
		repoToCreate, err = ghrepo.FromFullName(opts.Name)
		if err != nil {
			return fmt.Errorf("argument error: %w", err)
		}
	} else {
		repoToCreate = ghrepo.NewWithHost("", opts.Name, host)
	}

	input := repoCreateInput{
		Name:               repoToCreate.RepoName(),
		Visibility:         opts.Visibility,
		OwnerLogin:         repoToCreate.RepoOwner(),
		TeamSlug:           opts.Team,
		Description:        opts.Description,
		HomepageURL:        opts.Homepage,
		HasIssuesEnabled:   !opts.DisableIssues,
		HasWikiEnabled:     !opts.DisableWiki,
		GitIgnoreTemplate:  opts.GitIgnoreTemplate,
		LicenseTemplate:    opts.LicenseTemplate,
		IncludeAllBranches: opts.IncludeAllBranches,
		InitReadme:         opts.AddReadme,
	}

	var templateRepoMainBranch string
	if opts.Template != "" {
		var templateRepo ghrepo.Interface
		apiClient := api.NewClientFromHTTP(httpClient)

		templateRepoName := opts.Template
		if !strings.Contains(templateRepoName, "/") {
			currentUser, err := api.CurrentLoginName(apiClient, host)
			if err != nil {
				return err
			}
			templateRepoName = currentUser + "/" + templateRepoName
		}
		templateRepo, err = ghrepo.FromFullName(templateRepoName)
		if err != nil {
			return fmt.Errorf("argument error: %w", err)
		}

		repo, err := api.GitHubRepo(apiClient, templateRepo)
		if err != nil {
			return err
		}

		input.TemplateRepositoryID = repo.ID
		templateRepoMainBranch = repo.DefaultBranchRef.Name
	}

	repo, err := repoCreate(httpClient, repoToCreate.RepoHost(), input)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	isTTY := opts.IO.IsStdoutTTY()
	if isTTY {
		fmt.Fprintf(opts.IO.Out,
			"%s 已在 %s 上创建仓库 %s\n  %s\n",
			cs.SuccessIconWithColor(cs.Green),
			host,
			ghrepo.FullName(repo),
			repo.URL)
	} else {
		fmt.Fprintln(opts.IO.Out, repo.URL)
	}

	if opts.Interactive {
		var err error
		opts.Clone, err = opts.Prompter.Confirm("是否在本地克隆新仓库?", true)
		if err != nil {
			return err
		}
	}

	if opts.Clone {
		protocol := cfg.GitProtocol(repo.RepoHost()).Value
		remoteURL := ghrepo.FormatRemoteURL(repo, protocol)

		if !opts.AddReadme && opts.LicenseTemplate == "" && opts.GitIgnoreTemplate == "" && opts.Template == "" {
			// 克隆空仓库或模板
			if err := localInit(opts.GitClient, remoteURL, repo.RepoName()); err != nil {
				return err
			}
		} else if err := cloneWithRetry(opts, remoteURL, templateRepoMainBranch); err != nil {
			return err
		}

	}

	return nil
}

// 基于模板仓库在远程主机上创建新仓库
func createFromTemplate(opts *CreateOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	opts.Name, opts.Description, opts.Visibility, err = interactiveRepoInfo(httpClient, host, opts.Prompter, "")
	if err != nil {
		return err
	}

	if !strings.Contains(opts.Name, "/") {
		username, _, err := userAndOrgs(httpClient, host)
		if err != nil {
			return err
		}
		opts.Name = fmt.Sprintf("%s/%s", username, opts.Name)
	}
	repoToCreate, err := ghrepo.FromFullName(opts.Name)
	if err != nil {
		return fmt.Errorf("argument error: %w", err)
	}

	templateRepo, err := interactiveRepoTemplate(httpClient, host, repoToCreate.RepoOwner(), opts.Prompter)
	if err != nil {
		return err
	}
	input := repoCreateInput{
		Name:                 repoToCreate.RepoName(),
		Visibility:           opts.Visibility,
		OwnerLogin:           repoToCreate.RepoOwner(),
		TeamSlug:             opts.Team,
		Description:          opts.Description,
		HomepageURL:          opts.Homepage,
		HasIssuesEnabled:     !opts.DisableIssues,
		HasWikiEnabled:       !opts.DisableWiki,
		GitIgnoreTemplate:    opts.GitIgnoreTemplate,
		LicenseTemplate:      opts.LicenseTemplate,
		IncludeAllBranches:   opts.IncludeAllBranches,
		InitReadme:           opts.AddReadme,
		TemplateRepositoryID: templateRepo.ID,
	}
	templateRepoMainBranch := templateRepo.DefaultBranchRef.Name

	targetRepo := shared.NormalizeRepoName(opts.Name)
	if idx := strings.IndexRune(opts.Name, '/'); idx > 0 {
		targetRepo = opts.Name[0:idx+1] + shared.NormalizeRepoName(opts.Name[idx+1:])
	}
	confirmed, err := opts.Prompter.Confirm(
		fmt.Sprintf(`将在 %s 上以 %s 可见性创建 "%s"。是否继续?`, host, strings.ToLower(opts.Visibility), targetRepo),
		true)
	if err != nil {
		return err
	} else if !confirmed {
		return cmdutil.CancelError
	}

	repo, err := repoCreate(httpClient, repoToCreate.RepoHost(), input)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.Out,
		"%s 已在 %s 上创建仓库 %s\n  %s\n",
		cs.SuccessIconWithColor(cs.Green),
		host,
		ghrepo.FullName(repo),
		repo.URL)

	opts.Clone, err = opts.Prompter.Confirm("是否在本地克隆新仓库?", true)
	if err != nil {
		return err
	}

	if opts.Clone {
		protocol := cfg.GitProtocol(repo.RepoHost()).Value
		remoteURL := ghrepo.FormatRemoteURL(repo, protocol)

		if err := cloneWithRetry(opts, remoteURL, templateRepoMainBranch); err != nil {
			return err
		}
	}

	return nil
}

// 基于现有的本地仓库在远程主机上创建仓库
func createFromLocal(opts *CreateOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	isTTY := opts.IO.IsStdoutTTY()
	stdout := opts.IO.Out

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	host, _ := cfg.Authentication().DefaultHost()

	if opts.Interactive {
		var err error
		opts.Source, err = opts.Prompter.Input("本地仓库路径", ".")
		if err != nil {
			return err
		}
	}

	repoPath := opts.Source
	opts.GitClient.RepoDir = repoPath

	var baseRemote string
	if opts.Remote == "" {
		baseRemote = "origin"
	} else {
		baseRemote = opts.Remote
	}

	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}

	repoType, err := localRepoType(opts.GitClient)
	if err != nil {
		return err
	}
	if repoType == unknown {
		if repoPath == "." {
			return fmt.Errorf("当前目录不是 git 仓库, 请运行 `git init` 进行初始化")
		}
		return fmt.Errorf("%s 不是 git 仓库, 请运行 `git -C \"%s\" init` 进行初始化", absPath, repoPath)
	}

	committed, err := hasCommits(opts.GitClient)
	if err != nil {
		return err
	}
	if opts.Push {
		// 若无提交却尝试推送, 则立即失败
		if !committed {
			return fmt.Errorf("已启用 `--push`, 但在 %s 中未找到任何提交", absPath)
		}
	}

	if opts.Interactive {
		opts.Name, opts.Description, opts.Visibility, err = interactiveRepoInfo(httpClient, host, opts.Prompter, filepath.Base(absPath))
		if err != nil {
			return err
		}
	}

	var repoToCreate ghrepo.Interface

	// 仓库名称将取当前目录名或指定名称
	if opts.Name == "" {
		repoToCreate = ghrepo.NewWithHost("", filepath.Base(absPath), host)
	} else if strings.Contains(opts.Name, "/") {
		var err error
		repoToCreate, err = ghrepo.FromFullName(opts.Name)
		if err != nil {
			return fmt.Errorf("argument error: %w", err)
		}
	} else {
		repoToCreate = ghrepo.NewWithHost("", opts.Name, host)
	}

	input := repoCreateInput{
		Name:              repoToCreate.RepoName(),
		Visibility:        opts.Visibility,
		OwnerLogin:        repoToCreate.RepoOwner(),
		TeamSlug:          opts.Team,
		Description:       opts.Description,
		HomepageURL:       opts.Homepage,
		HasIssuesEnabled:  !opts.DisableIssues,
		HasWikiEnabled:    !opts.DisableWiki,
		GitIgnoreTemplate: opts.GitIgnoreTemplate,
		LicenseTemplate:   opts.LicenseTemplate,
	}

	repo, err := repoCreate(httpClient, repoToCreate.RepoHost(), input)
	if err != nil {
		return err
	}

	if isTTY {
		fmt.Fprintf(stdout,
			"%s 已在 %s 上创建仓库 %s\n  %s\n",
			cs.SuccessIconWithColor(cs.Green),
			host,
			ghrepo.FullName(repo),
			repo.URL)
	} else {
		fmt.Fprintln(stdout, repo.URL)
	}

	protocol := cfg.GitProtocol(repo.RepoHost()).Value
	remoteURL := ghrepo.FormatRemoteURL(repo, protocol)

	if opts.Interactive {
		addRemote, err := opts.Prompter.Confirm("是否添加远程?", true)
		if err != nil {
			return err
		}
		if !addRemote {
			return nil
		}

		baseRemote, err = opts.Prompter.Input("新的远程应叫什么名字?", "origin")
		if err != nil {
			return err
		}
	}

	if err := sourceInit(opts.GitClient, opts.IO, remoteURL, baseRemote); err != nil {
		return err
	}

	// 若没有任何提交, 则不提示推送
	if opts.Interactive && committed {
		msg := fmt.Sprintf("是否将当前分支的提交推送到 %q?", baseRemote)
		if repoType == bare {
			msg = fmt.Sprintf("是否将所有引用镜像到 %q?", baseRemote)
		}

		var err error
		opts.Push, err = opts.Prompter.Confirm(msg, true)
		if err != nil {
			return err
		}
	}

	if opts.Push && repoType == working {
		err := opts.GitClient.Push(context.Background(), baseRemote, "HEAD")
		if err != nil {
			return err
		}

		if isTTY {
			fmt.Fprintf(stdout, "%s 已将提交推送到 %s\n", cs.SuccessIcon(), remoteURL)
		}
	}

	if opts.Push && repoType == bare {
		cmd, err := opts.GitClient.AuthenticatedCommand(context.Background(), git.AllMatchingCredentialsPattern, "push", baseRemote, "--mirror")
		if err != nil {
			return err
		}
		if err = cmd.Run(); err != nil {
			return err
		}

		if isTTY {
			fmt.Fprintf(stdout, "%s 已将所有引用镜像到 %s\n", cs.SuccessIcon(), remoteURL)
		}
	}

	return nil
}

func cloneWithRetry(opts *CreateOptions, remoteURL, branch string) error {
	// 允许在测试中注入替代的 BackOff。
	if opts.BackOff == nil {
		opts.BackOff = backoff.NewConstantBackOff(3 * time.Second)
	}

	var args []string
	if branch != "" {
		args = append(args, "--branch", branch)
	}

	ctx := context.Background()
	return backoff.Retry(func() error {
		stderr := &bytes.Buffer{}
		_, err := opts.GitClient.Clone(ctx, remoteURL, args, git.WithStderr(stderr))

		var execError errWithExitCode
		if errors.As(err, &execError) && execError.ExitCode() == 128 {
			return err
		} else {
			_, _ = io.Copy(opts.IO.ErrOut, stderr)
		}

		return backoff.Permanent(err)
	}, backoff.WithContext(backoff.WithMaxRetries(opts.BackOff, 3), ctx))
}

func sourceInit(gitClient *git.Client, io *iostreams.IOStreams, remoteURL, baseRemote string) error {
	cs := io.ColorScheme()
	remoteAdd, err := gitClient.Command(context.Background(), "remote", "add", baseRemote, remoteURL)
	if err != nil {
		return err
	}
	_, err = remoteAdd.Output()
	if err != nil {
		return fmt.Errorf("%s 无法添加远程 %q", cs.FailureIcon(), baseRemote)
	}
	if io.IsStdoutTTY() {
		fmt.Fprintf(io.Out, "%s 已添加远程 %s\n", cs.SuccessIcon(), remoteURL)
	}
	return nil
}

// 检查本地仓库是否已有提交
func hasCommits(gitClient *git.Client) (bool, error) {
	hasCommitsCmd, err := gitClient.Command(context.Background(), "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	_, err = hasCommitsCmd.Output()
	if err == nil {
		return true, nil
	}

	if execError, ok := errors.AsType[*exec.ExitError](err); ok {
		exitCode := int(execError.ExitCode())
		if exitCode == 128 {
			return false, nil
		}
		return false, err
	}
	return false, nil
}

type repoType int

const (
	unknown repoType = iota
	working
	bare
)

func localRepoType(gitClient *git.Client) (repoType, error) {
	projectDir, projectDirErr := gitClient.GitDir(context.Background())
	if projectDirErr != nil {
		if execError, ok := errors.AsType[errWithExitCode](projectDirErr); ok {
			if exitCode := int(execError.ExitCode()); exitCode == 128 {
				return unknown, nil
			}
			return unknown, projectDirErr
		}
	}

	switch projectDir {
	case ".":
		return bare, nil
	case ".git":
		return working, nil
	default:
		return unknown, nil
	}
}

// 将检出的分支克隆到指定路径
func localInit(gitClient *git.Client, remoteURL, path string) error {
	ctx := context.Background()
	gitInit, err := gitClient.Command(ctx, "init", path)
	if err != nil {
		return err
	}
	_, err = gitInit.Output()
	if err != nil {
		return err
	}

	// Copy the client so we do not modify the original client's RepoDir.
	gc := gitClient.Copy()
	gc.RepoDir = path

	gitRemoteAdd, err := gc.Command(ctx, "remote", "add", "origin", remoteURL)
	if err != nil {
		return err
	}
	_, err = gitRemoteAdd.Output()
	if err != nil {
		return err
	}

	return nil
}

func interactiveRepoTemplate(client *http.Client, hostname, owner string, prompter iprompter) (*api.Repository, error) {
	templateRepos, err := listTemplateRepositories(client, hostname, owner)
	if err != nil {
		return nil, err
	}
	if len(templateRepos) == 0 {
		return nil, fmt.Errorf("%s 没有模板仓库", owner)
	}

	var templates []string
	for _, repo := range templateRepos {
		templates = append(templates, repo.Name)
	}

	selected, err := prompter.Select("选择一个模板仓库", "", templates)
	if err != nil {
		return nil, err
	}
	return &templateRepos[selected], nil
}

func interactiveGitIgnore(client *http.Client, hostname string, prompter iprompter) (string, error) {
	confirmed, err := prompter.Confirm("您想添加 .gitignore 吗?", false)
	if err != nil {
		return "", err
	} else if !confirmed {
		return "", nil
	}

	templates, err := api.RepoGitIgnoreTemplates(client, hostname)
	if err != nil {
		return "", err
	}
	selected, err := prompter.Select("选择一个 .gitignore 模板", "", templates)
	if err != nil {
		return "", err
	}
	return templates[selected], nil
}

func interactiveLicense(client *http.Client, hostname string, prompter iprompter) (string, error) {
	confirmed, err := prompter.Confirm("您想添加许可证吗?", false)
	if err != nil {
		return "", err
	} else if !confirmed {
		return "", nil
	}

	licenses, err := api.RepoLicenses(client, hostname)
	if err != nil {
		return "", err
	}
	licenseNames := make([]string, 0, len(licenses))
	for _, license := range licenses {
		licenseNames = append(licenseNames, license.Name)
	}
	selected, err := prompter.Select("选择一个许可证", "", licenseNames)
	if err != nil {
		return "", err
	}
	return licenses[selected].Key, nil
}

// 名称, 描述和可见性
func interactiveRepoInfo(client *http.Client, hostname string, prompter iprompter, defaultName string) (string, string, string, error) {
	name, owner, err := interactiveRepoNameAndOwner(client, hostname, prompter, defaultName)
	if err != nil {
		return "", "", "", err
	}
	if owner != "" {
		name = fmt.Sprintf("%s/%s", owner, name)
	}

	description, err := prompter.Input("描述", "")
	if err != nil {
		return "", "", "", err
	}

	visibilityOptions := getRepoVisibilityOptions(owner)
	selected, err := prompter.Select("可见性", "Public", visibilityOptions)
	if err != nil {
		return "", "", "", err
	}

	return name, description, strings.ToUpper(visibilityOptions[selected]), nil
}

func getRepoVisibilityOptions(owner string) []string {
	visibilityOptions := []string{"Public", "Private"}
	// 组织还可以创建内部仓库
	if owner != "" {
		visibilityOptions = append(visibilityOptions, "Internal")
	}
	return visibilityOptions
}

func interactiveRepoNameAndOwner(client *http.Client, hostname string, prompter iprompter, defaultName string) (string, string, error) {
	name, err := prompter.Input("仓库名称", defaultName)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("仓库名称不能为空")
	}

	name, owner, err := splitNameAndOwner(name)
	if err != nil {
		return "", "", err
	}
	if owner != "" {
		// 用户显式提供了所有者前缀。
		return name, owner, nil
	}

	username, orgs, err := userAndOrgs(client, hostname)
	if err != nil {
		return "", "", err
	}
	if len(orgs) == 0 {
		// 用户不属于任何组织。
		// 将所有者留空以表示个人仓库。
		return name, "", nil
	}

	owners := append(orgs, username)
	sort.Strings(owners)
	selected, err := prompter.Select("仓库所有者", username, owners)
	if err != nil {
		return "", "", err
	}

	owner = owners[selected]
	if owner == username {
		// 将所有者留空以表示个人仓库。
		return name, "", nil
	}
	return name, owner, nil
}

func splitNameAndOwner(name string) (string, string, error) {
	if !strings.Contains(name, "/") {
		return name, "", nil
	}
	repo, err := ghrepo.FromFullName(name)
	if err != nil {
		return "", "", fmt.Errorf("argument error: %w", err)
	}
	return repo.RepoName(), repo.RepoOwner(), nil
}
