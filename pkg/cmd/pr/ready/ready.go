package ready

import (
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ReadyOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Finder shared.PRFinder

	SelectorArg string
	Undo        bool
}

func NewCmdReady(f *cmdutil.Factory, runF func(*ReadyOptions) error) *cobra.Command {
	opts := &ReadyOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "ready [<number> | <url> | <branch>]",
		Short: "将拉取请求(PR)标记为可审核(ready for review)",
		Long: heredoc.Docf(`
			将拉取请求(PR)标记为可审核(ready for review)。

			若未提供参数，将把属于当前分支的拉取请求(PR)标记为可审核。

			若您的套餐(plan)支持，可使用 %[1]s--undo%[1]s 转换为草稿(draft)
		`, "`"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("使用 --repo flag 时必须提供位置参数")
			}

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			if runF != nil {
				return runF(opts)
			}
			return readyRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Undo, "undo", false, `将拉取请求(PR)转换为"草稿(draft)"`)
	return cmd
}

func readyRun(opts *ReadyOptions) error {
	cs := opts.IO.ColorScheme()

	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "number", "state", "isDraft"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	if !pr.IsOpen() {
		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d 已关闭。只有草稿(draft)拉取请求才能被标记为\"可审核(ready for review)\"\n", cs.FailureIcon(), ghrepo.FullName(baseRepo), pr.Number)
		return cmdutil.SilentError
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	if opts.Undo { // 转换为草稿(draft)
		if pr.IsDraft {
			fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d 已是\"草稿(draft)\"状态\n", cs.WarningIcon(), ghrepo.FullName(baseRepo), pr.Number)
			return nil
		}
		err = api.ConvertPullRequestToDraft(apiClient, baseRepo, pr)
		if err != nil {
			return fmt.Errorf("API 调用失败: %w", err)
		}

		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d 已转换为\"草稿(draft)\"\n", cs.SuccessIconWithColor(cs.Green), ghrepo.FullName(baseRepo), pr.Number)
	} else { // 标记为可审核(ready for review)
		if !pr.IsDraft {
			fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d 已是\"可审核(ready for review)\"状态\n", cs.WarningIcon(), ghrepo.FullName(baseRepo), pr.Number)
			return nil
		}

		err = api.PullRequestReady(apiClient, baseRepo, pr)
		if err != nil {
			return fmt.Errorf("API 调用失败: %w", err)
		}

		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d 已被标记为\"可审核(ready for review)\"\n", cs.SuccessIconWithColor(cs.Green), ghrepo.FullName(baseRepo), pr.Number)
	}

	return nil
}
