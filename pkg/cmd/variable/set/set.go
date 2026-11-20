package set

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

type iprompter interface {
	Input(string, string) (string, error)
}

type SetOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter

	VariableName    string
	OrgName         string
	EnvName         string
	Body            string
	Visibility      string
	RepositoryNames []string
	EnvFile         string
}

func NewCmdSet(f *cmdutil.Factory, runF func(*SetOptions) error) *cobra.Command {
	opts := &SetOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "set <variable-name>",
		Short: "创建或更新变量",
		Long: heredoc.Doc(`
			在以下任一级别上为变量设置值:
			- 仓库(默认): 可供仓库中的 GitHub Actions 运行或 Dependabot 使用
			- 环境: 可供仓库中部署环境的 GitHub Actions 运行使用
			- 组织: 可供组织内的 GitHub Actions 运行或 Dependabot 使用

			组织变量可以选择性地限制为仅对特定仓库可用。
		`),
		Example: heredoc.Doc(`
			# 在交互式提示中为当前仓库添加变量值
			$ gh variable set MYVARIABLE

			# 从环境变量中读取变量值
			$ gh variable set MYVARIABLE --body "$ENV_VALUE"

			# 从文件中读取变量值
			$ gh variable set MYVARIABLE < myfile.txt

			# 为当前仓库的部署环境设置变量
			$ gh variable set MYVARIABLE --env myenvironment

			# 设置组织级变量，对公共和私有仓库均可见
			$ gh variable set MYVARIABLE --org myOrg --visibility all

			# 设置组织级变量，仅对特定仓库可见
			$ gh variable set MYVARIABLE --org myOrg --repos repo1,repo2,repo3

			# 设置从 ".env" 文件导入的多个变量值
			$ gh variable set -f .env
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if err := cmdutil.MutuallyExclusive("仅需指定 `--org` 或 `--env` 其中之一", opts.OrgName != "", opts.EnvName != ""); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive("仅需指定 `--body` 或 `--env-file` 其中之一", opts.Body != "", opts.EnvFile != ""); err != nil {
				return err
			}

			if len(args) == 0 {
				if opts.EnvFile == "" {
					return cmdutil.FlagErrorf("必须传递 name 参数")
				}
			} else {
				opts.VariableName = args[0]
			}

			if cmd.Flags().Changed("visibility") {
				if opts.OrgName == "" {
					return cmdutil.FlagErrorf("`--visibility` 仅在 `--org` 下可用")
				}

				if opts.Visibility != shared.Selected && len(opts.RepositoryNames) > 0 {
					return cmdutil.FlagErrorf("`--repos` 仅在 `--visibility=selected` 下可用")
				}

				if opts.Visibility == shared.Selected && len(opts.RepositoryNames) == 0 {
					return cmdutil.FlagErrorf("`--repos` 列表在使用 `--visibility=selected` 时需要提供")
				}
			} else {
				if len(opts.RepositoryNames) > 0 {
					opts.Visibility = shared.Selected
				}
			}

			if runF != nil {
				return runF(opts)
			}

			return setRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OrgName, "org", "o", "", "设置 `organization` 变量")
	cmd.Flags().StringVarP(&opts.EnvName, "env", "e", "", "设置部署 `environment` 变量")
	cmdutil.StringEnumFlag(cmd, &opts.Visibility, "visibility", "v", shared.Private, []string{shared.All, shared.Private, shared.Selected}, "设置组织变量的可见性")
	cmd.Flags().StringSliceVarP(&opts.RepositoryNames, "repos", "r", []string{}, "可以访问组织变量的 `repositories` 列表")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "变量的值(如果未指定，则从标准输入读取)")
	cmd.Flags().StringVarP(&opts.EnvFile, "env-file", "f", "", "从 dotenv 格式的 `file` 中加载变量名和值")

	return cmd
}

func setRun(opts *SetOptions) error {
	variables, err := getVariablesFromOptions(opts)
	if err != nil {
		return err
	}

	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法设置 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	orgName := opts.OrgName
	envName := opts.EnvName

	var host string
	var baseRepo ghrepo.Interface
	if orgName == "" {
		baseRepo, err = opts.BaseRepo()
		if err != nil {
			return err
		}
		host = baseRepo.RepoHost()
	} else {
		cfg, err := opts.Config()
		if err != nil {
			return err
		}
		host, _ = cfg.Authentication().DefaultHost()
	}

	entity, err := shared.GetVariableEntity(orgName, envName)
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	repositoryIDs, err := getRepoIds(client, host, opts.OrgName, opts.RepositoryNames)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	setc := make(chan setResult)
	for key, value := range variables {
		k := key
		v := value
		go func() {
			setOpts := setOptions{
				Entity:        entity,
				Environment:   envName,
				Key:           k,
				Organization:  orgName,
				Repository:    baseRepo,
				RepositoryIDs: repositoryIDs,
				Value:         v,
				Visibility:    opts.Visibility,
			}
			setc <- setVariable(client, host, setOpts)
		}()
	}

	var errs []error
	cs := opts.IO.ColorScheme()
	for i := 0; i < len(variables); i++ {
		result := <-setc
		if result.Err != nil {
			errs = append(errs, result.Err)
			continue
		}
		if !opts.IO.IsStdoutTTY() {
			continue
		}
		target := orgName
		if orgName == "" {
			target = ghrepo.FullName(baseRepo)
		}
		if envName != "" {
			target += " environment " + envName
		}
		fmt.Fprintf(opts.IO.Out, "%s %s 变量 %s 用于 %s\n", cs.SuccessIcon(), result.Operation, result.Key, target)
	}

	return errors.Join(errs...)
}

func getVariablesFromOptions(opts *SetOptions) (map[string]string, error) {
	variables := make(map[string]string)

	if opts.EnvFile != "" {
		var r io.Reader
		if opts.EnvFile == "-" {
			defer opts.IO.In.Close()
			r = opts.IO.In
		} else {
			f, err := os.Open(opts.EnvFile)
			if err != nil {
				return nil, fmt.Errorf("无法打开环境变量文件: %w", err)
			}
			defer f.Close()
			r = f
		}
		envs, err := godotenv.Parse(r)
		if err != nil {
			return nil, fmt.Errorf("解析环境变量文件时出错: %w", err)
		}
		if len(envs) == 0 {
			return nil, fmt.Errorf("文件中未找到变量")
		}
		maps.Copy(variables, envs)
		return variables, nil
	}

	body, err := getBody(opts)
	if err != nil {
		return nil, fmt.Errorf("不理解的变量体: %w", err)
	}
	variables[opts.VariableName] = body

	return variables, nil
}

func getBody(opts *SetOptions) (string, error) {
	if opts.Body != "" {
		return opts.Body, nil
	}

	if opts.IO.CanPrompt() {
		bodyInput, err := opts.Prompter.Input("粘贴你的变量", "")
		if err != nil {
			return "", err
		}
		fmt.Fprintln(opts.IO.Out)
		return bodyInput, nil
	}

	body, err := io.ReadAll(opts.IO.In)
	if err != nil {
		return "", fmt.Errorf("无法从标准输入读取数据: %w", err)
	}

	return string(bytes.TrimRight(body, "\r\n")), nil
}

func getRepoIds(client *api.Client, host, owner string, repositoryNames []string) ([]int64, error) {
	if len(repositoryNames) == 0 {
		return nil, nil
	}
	repos := make([]ghrepo.Interface, 0, len(repositoryNames))
	for _, repositoryName := range repositoryNames {
		if repositoryName == "" {
			continue
		}
		var repo ghrepo.Interface
		if strings.Contains(repositoryName, "/") || owner == "" {
			var err error
			repo, err = ghrepo.FromFullNameWithHost(repositoryName, host)
			if err != nil {
				return nil, fmt.Errorf("无效的仓库名称")
			}
		} else {
			repo = ghrepo.NewWithHost(owner, repositoryName, host)
		}
		repos = append(repos, repo)
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("不支持将所选仓库重置为零")
	}
	repositoryIDs, err := api.GetRepoIDs(client, host, repos)
	if err != nil {
		return nil, fmt.Errorf("未能查找仓库 %v 的 ID: %w", repositoryNames, err)
	}
	return repositoryIDs, nil
}
