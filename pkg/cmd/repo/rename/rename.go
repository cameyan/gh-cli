package rename

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type iprompter interface {
	Input(string, string) (string, error)
	Confirm(string, bool) (bool, error)
}

type RenameOptions struct {
	HttpClient      func() (*http.Client, error)
	GitClient       *git.Client
	IO              *iostreams.IOStreams
	Prompter        iprompter
	Config          func() (gh.Config, error)
	BaseRepo        func() (ghrepo.Interface, error)
	Remotes         func() (ghContext.Remotes, error)
	DoConfirm       bool
	HasRepoOverride bool
	newRepoSelector string
}

func NewCmdRename(f *cmdutil.Factory, runf func(*RenameOptions) error) *cobra.Command {
	opts := &RenameOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Remotes:    f.Remotes,
		Config:     f.Config,
		Prompter:   f.Prompter,
	}

	var confirm bool

	cmd := &cobra.Command{
		Use:   "rename [<new-name>]",
		Short: "重命名仓库",
		Long: heredoc.Docf(`
			重命名 GitHub 仓库。

			%[1]s<new-name>%[1]s 是所需的新仓库名称，不包含所有者。

			默认情况下，重命名当前仓库。否则，重命名 %[1]s--repo%[1]s 指定的仓库。

			若要将仓库所有权转移给其他用户账户或组织，
			您必须在 %[1]sgithub.com%[1]s 上执行额外的步骤。

			有关转移仓库所有权的更多信息，请参见:
			<https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository>
			`, "`"),
		Example: heredoc.Doc(`
			# 重命名当前仓库(foo/bar -> foo/baz)
			$ gh repo rename baz

			# 重命名指定仓库(qux/quux -> qux/baz)
			$ gh repo rename -R qux/quux baz
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo
			opts.HasRepoOverride = cmd.Flags().Changed("repo")

			if len(args) > 0 {
				opts.newRepoSelector = args[0]
			} else if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须提供新名称参数")
			}

			if len(args) == 1 && !confirm && !opts.HasRepoOverride {
				if !opts.IO.CanPrompt() {
					return cmdutil.FlagErrorf("传入单个参数时必须提供 --yes 标志")
				}
				opts.DoConfirm = true
			}

			if runf != nil {
				return runf(opts)
			}

			return renameRun(opts)
		},
	}

	cmdutil.EnableRepoOverride(cmd, f)
	cmd.Flags().BoolVar(&confirm, "confirm", false, "跳过确认提示")
	_ = cmd.Flags().MarkDeprecated("confirm", "use `--yes` instead")
	cmd.Flags().BoolVarP(&confirm, "yes", "y", false, "跳过确认提示")

	return cmd
}

func renameRun(opts *RenameOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	newRepoName := opts.newRepoSelector

	currRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if newRepoName == "" {
		if newRepoName, err = opts.Prompter.Input(fmt.Sprintf(
			"将 %s 重命名为:", ghrepo.FullName(currRepo)), ""); err != nil {
			return err
		}
	}

	if strings.Contains(newRepoName, "/") {
		return fmt.Errorf("新仓库名称不能包含 '/' 字符 - 若要将仓库转移给新所有者，请参见 <https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository>。")
	}

	if opts.DoConfirm {
		var confirmed bool
		if confirmed, err = opts.Prompter.Confirm(fmt.Sprintf(
			"将 %s 重命名为 %s？", ghrepo.FullName(currRepo), newRepoName), false); err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	newRepo, err := api.RenameRepo(apiClient, currRepo, newRepoName)
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.Out, "%s 已重命名仓库 %s\n", cs.SuccessIcon(), ghrepo.FullName(newRepo))
	}

	if opts.HasRepoOverride {
		return nil
	}

	remote, err := updateRemote(currRepo, newRepo, opts)
	if err != nil {
		if remote != nil {
			fmt.Fprintf(opts.IO.ErrOut, "%s 警告: 无法更新远程 %q: %v\n", cs.WarningIcon(), remote.Name, err)
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "%s 警告: 无法更新远程: %v\n", cs.WarningIcon(), err)
		}
	} else if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.Out, "%s 已更新 %q 远程\n", cs.SuccessIcon(), remote.Name)
	}

	return nil
}

func updateRemote(repo ghrepo.Interface, renamed ghrepo.Interface, opts *RenameOptions) (*ghContext.Remote, error) {
	cfg, err := opts.Config()
	if err != nil {
		return nil, err
	}

	protocol := cfg.GitProtocol(repo.RepoHost()).Value

	remotes, err := opts.Remotes()
	if err != nil {
		return nil, err
	}

	remote, err := remotes.FindByRepo(repo.RepoOwner(), repo.RepoName())
	if err != nil {
		return nil, err
	}

	remoteURL := ghrepo.FormatRemoteURL(renamed, protocol)
	err = opts.GitClient.UpdateRemoteURL(context.Background(), remote.Name, remoteURL)

	return remote, err
}
