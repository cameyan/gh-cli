package delete

import (
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

type DeleteOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	BaseRepo   func() (ghrepo.Interface, error)

	VariableName string
	OrgName      string
	EnvName      string
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "delete <variable-name>",
		Short: "删除变量",
		Long: heredoc.Doc(`
			在以下任一级别删除一个变量:
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

			return removeRun(opts)
		},
		Aliases: []string{
			"remove",
		},
	}
	cmd.Flags().StringVarP(&opts.OrgName, "org", "o", "", "删除一个组织变量")
	cmd.Flags().StringVarP(&opts.EnvName, "env", "e", "", "删除一个环境变量")

	return cmd
}

func removeRun(opts *DeleteOptions) error {
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

	err = client.REST(host, "DELETE", path.String(), nil, nil)
	if err != nil {
		return fmt.Errorf("删除变量 %s 失败: %w", opts.VariableName, err)
	}

	if opts.IO.IsStdoutTTY() {
		var target string
		switch variableEntity {
		case shared.Organization:
			target = orgName
		case shared.Repository, shared.Environment:
			target = ghrepo.FullName(baseRepo)
		}

		cs := opts.IO.ColorScheme()
		if envName != "" {
			fmt.Fprintf(opts.IO.Out, "%[1]s 从 %[3]s 环境中的 %[4]s 上删除了变量 %[2]s\n", cs.SuccessIconWithColor(cs.Red), opts.VariableName, envName, target)
		} else {
			fmt.Fprintf(opts.IO.Out, "%[1]s 从 %[3]s 中删除了变量 %[2]s\n", cs.SuccessIconWithColor(cs.Red), opts.VariableName, target)
		}
	}

	return nil
}
