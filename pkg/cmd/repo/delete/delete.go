package delete

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

type iprompter interface {
	ConfirmDeletion(string) error
}

type DeleteOptions struct {
	HttpClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter
	IO         *iostreams.IOStreams
	RepoArg    string
	Confirmed  bool
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		BaseRepo:   f.BaseRepo,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete [<repository>]",
		Short: "删除仓库",
		Long: heredoc.Docf(`
			删除一个 GitHub 仓库。

			不带参数时，删除当前仓库。否则，删除指定的仓库。
			
			出于安全考虑，当未提供仓库参数时，%[1]s--yes%[1]s 旗标将被忽略，并且系统会提示您进行确认。

			若要以非交互方式删除当前仓库，请显式指定仓库名称(例如: %[1]sgh repo delete owner/repo --yes%[1]s)。

			删除操作需要已授权 %[1]sdelete_repo%[1]s 权限范围。
			若要授权，请运行 %[1]sgh auth refresh -s delete_repo%[1]s
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.RepoArg = args[0]
			}

			// 未提供参数时忽略 --yes, 以防止误删
			if len(args) == 0 && opts.Confirmed {
				if !opts.IO.CanPrompt() {
					return cmdutil.FlagErrorf("无法以非交互方式删除当前仓库。请指定仓库名称或以交互方式运行")
				}
				_, _ = fmt.Fprintln(opts.IO.ErrOut, "警告: 由于未指定仓库，`--yes` 已被忽略")
				opts.Confirmed = false
			}

			if !opts.IO.CanPrompt() && !opts.Confirmed {
				return cmdutil.FlagErrorf("非交互模式下运行时，需要 --yes")
			}

			if runF != nil {
				return runF(opts)
			}

			return deleteRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Confirmed, "confirm", false, "无需提示直接确认删除")
	_ = cmd.Flags().MarkDeprecated("confirm", "请改用 `--yes`")
	cmd.Flags().BoolVar(&opts.Confirmed, "yes", false, "无需提示直接确认删除")
	return cmd
}

func deleteRun(opts *DeleteOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	var toDelete ghrepo.Interface

	if opts.RepoArg == "" {
		toDelete, err = opts.BaseRepo()
		if err != nil {
			return err
		}
	} else {
		repoSelector := opts.RepoArg
		if !strings.Contains(repoSelector, "/") {
			defaultHost, _ := ghauth.DefaultHost()
			currentUser, err := api.CurrentLoginName(apiClient, defaultHost)
			if err != nil {
				return err
			}
			repoSelector = currentUser + "/" + repoSelector
		}
		toDelete, err = ghrepo.FromFullName(repoSelector)
		if err != nil {
			return fmt.Errorf("参数错误: %w", err)
		}
	}
	fullName := ghrepo.FullName(toDelete)

	if !opts.Confirmed {
		if err := opts.Prompter.ConfirmDeletion(fullName); err != nil {
			return err
		}
	}

	err = deleteRepo(httpClient, toDelete)
	if err != nil {
		if httpErr, ok := errors.AsType[api.HTTPError](err); ok {
			statusCode := httpErr.HTTPError.StatusCode
			if statusCode == http.StatusMovedPermanently ||
				statusCode == http.StatusTemporaryRedirect ||
				statusCode == http.StatusPermanentRedirect {
				cs := opts.IO.ColorScheme()
				fmt.Fprintf(opts.IO.ErrOut, "%s 删除仓库失败: %s 已更名或已转移所有权\n", cs.FailureIcon(), fullName)
				return cmdutil.SilentError
			}
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out,
			"%s 已删除仓库 %s\n",
			cs.SuccessIcon(),
			fullName)
	}

	return nil
}
