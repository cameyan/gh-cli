package archive

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmdutil"

	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ArchiveOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Confirmed  bool
	IO         *iostreams.IOStreams
	RepoArg    string
	Prompter   prompter.Prompter
}

func NewCmdArchive(f *cmdutil.Factory, runF func(*ArchiveOptions) error) *cobra.Command {
	opts := &ArchiveOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		BaseRepo:   f.BaseRepo,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "archive [<repository>]",
		Short: "归档仓库",
		Long: heredoc.Doc(`归档一个 GitHub 仓库。

不带参数时，归档当前仓库。`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.RepoArg = args[0]
			}

			if !opts.Confirmed && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互模式下运行时，需要 --yes")
			}

			if runF != nil {
				return runF(opts)
			}

			return archiveRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Confirmed, "confirm", false, "跳过确认提示")
	_ = cmd.Flags().MarkDeprecated("confirm", "请改用 `--yes`")
	cmd.Flags().BoolVarP(&opts.Confirmed, "yes", "y", false, "跳过确认提示")
	return cmd
}

func archiveRun(opts *ArchiveOptions) error {
	cs := opts.IO.ColorScheme()
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	var toArchive ghrepo.Interface

	if opts.RepoArg == "" {
		toArchive, err = opts.BaseRepo()
		if err != nil {
			return err
		}
	} else {
		repoSelector := opts.RepoArg
		if !strings.Contains(repoSelector, "/") {
			cfg, err := opts.Config()
			if err != nil {
				return err
			}

			hostname, _ := cfg.Authentication().DefaultHost()

			currentUser, err := api.CurrentLoginName(apiClient, hostname)
			if err != nil {
				return err
			}
			repoSelector = currentUser + "/" + repoSelector
		}

		toArchive, err = ghrepo.FromFullName(repoSelector)
		if err != nil {
			return err
		}
	}

	fields := []string{"name", "owner", "isArchived", "id"}
	repo, err := api.FetchRepository(apiClient, toArchive, fields)
	if err != nil {
		return err
	}

	fullName := ghrepo.FullName(toArchive)
	if repo.IsArchived {
		fmt.Fprintf(opts.IO.ErrOut, "%s 仓库 %s 已处于归档状态\n", cs.WarningIcon(), fullName)
		return nil
	}

	if !opts.Confirmed {
		confirmed, err := opts.Prompter.Confirm(fmt.Sprintf("是否归档 %s?", fullName), false)
		if err != nil {
			return fmt.Errorf("提示失败: %w", err)
		}
		if !confirmed {
			return cmdutil.CancelError
		}
	}

	err = archiveRepo(httpClient, repo)
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.Out,
			"%s 已归档仓库 %s\n",
			cs.SuccessIcon(),
			fullName)
	}

	return nil
}
