package get

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type GetOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	BaseRepo   func() (ghrepo.Interface, error)

	Exporter cmdutil.Exporter

	VariableName string
	OrgName      string
	EnvName      string
}

func NewCmdGet(f *cmdutil.Factory, runF func(*GetOptions) error) *cobra.Command {
	opts := &GetOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "get <variable-name>",
		Short: "获取变量",
		Long: heredoc.Doc(`
			在以下任一级别上获取一个变量:
			- 仓库(默认): 可供仓库中的 GitHub Actions 运行或 Dependabot 使用
			- 环境: 可用于仓库中部署环境的 GitHub Actions 运行
			- 组织: 适用于组织内的 GitHub Actions 运行或 Dependabot 使用
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if err := cmdutil.MutuallyExclusive("仅需指定 `--org` 或 `--env` 其中之一", opts.OrgName != "", opts.EnvName != ""); err != nil {
				return err
			}

			opts.VariableName = args[0]

			if runF != nil {
				return runF(opts)
			}

			return getRun(opts)
		},
	}
	cmd.Flags().StringVarP(&opts.OrgName, "org", "o", "", "获取一个组织变量")
	cmd.Flags().StringVarP(&opts.EnvName, "env", "e", "", "获取一个环境变量")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, shared.VariableJSONFields)

	return cmd
}

func getRun(opts *GetOptions) error {
	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法创建 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	orgName := opts.OrgName
	envName := opts.EnvName

	variableEntity, err := shared.GetVariableEntity(orgName, envName)
	if err != nil {
		return err
	}

	var baseRepo ghrepo.Interface
	if variableEntity == shared.Repository || variableEntity == shared.Environment {
		baseRepo, err = opts.BaseRepo()
		if err != nil {
			return err
		}
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	var path *safeurl.MutableSafeURL
	var host string
	switch variableEntity {
	case shared.Organization:
		path, err = safeurl.JoinPath("orgs", orgName, "actions", "variables", opts.VariableName)
		host, _ = cfg.Authentication().DefaultHost()
	case shared.Environment:
		path, err = safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "environments", envName, "variables", opts.VariableName)
		host = baseRepo.RepoHost()
	case shared.Repository:
		path, err = safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "actions", "variables", opts.VariableName)
		host = baseRepo.RepoHost()
	}
	if err != nil {
		return err
	}

	var variable shared.Variable
	if err = client.REST(host, "GET", path.String(), nil, &variable); err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("变量 %s 未找到", opts.VariableName)
		}

		return fmt.Errorf("获取变量 %s 失败: %w", opts.VariableName, err)
	}

	if opts.Exporter != nil {
		if variable.SelectedReposURL != "" {
			count, err := shared.SelectedRepositoryCount(client, host, safeurl.NewImmutableSafeURL(variable.SelectedReposURL))
			if err != nil {
				return fmt.Errorf("无法确定 %s 所处的存储库: %w", variable.Name, err)
			}
			variable.NumSelectedRepos = count
		}
		return opts.Exporter.Write(opts.IO, &variable)
	}

	fmt.Fprintf(opts.IO.Out, "%s\n", variable.Value)
	return nil
}
