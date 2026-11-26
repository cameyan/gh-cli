package delete

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/gist/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DeleteOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)
	Prompter   prompter.Prompter

	Selector  string
	Confirmed bool
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := DeleteOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete {<id> | <url>}",
		Short: "删除 gist",
		Long: heredoc.Docf(`
			删除 GitHub gist。

			要交互式删除 gist，请不带参数运行 %[1]sgh gist delete%[1]s。

			要以非交互方式删除 gist，请提供 gist 的 id 或 url。
		`, "`"),
		Example: heredoc.Doc(`
			# 交互式删除 gist
			$ gh gist delete

			# 以非交互方式删除 gist
			$ gh gist delete 1234
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !opts.IO.CanPrompt() && !opts.Confirmed {
				return cmdutil.FlagErrorf("非交互式运行时必须指定 --yes")
			}

			if !opts.IO.CanPrompt() && len(args) == 0 {
				return cmdutil.FlagErrorf("非交互式模式下必须指定 id 或 url 参数")
			}

			if len(args) == 1 {
				opts.Selector = args[0]
			}

			if runF != nil {
				return runF(&opts)
			}
			return deleteRun(&opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Confirmed, "yes", false, "无需提示直接确认删除")
	return cmd
}

func deleteRun(opts *DeleteOptions) error {
	client, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	gistID := opts.Selector
	if strings.Contains(gistID, "/") {
		id, err := shared.GistIDFromURL(gistID)
		if err != nil {
			return err
		}
		gistID = id
	}

	cs := opts.IO.ColorScheme()
	var gist *shared.Gist
	if gistID == "" {
		gist, err = shared.PromptGists(opts.Prompter, client, host, cs)
	} else {
		gist, err = shared.GetGist(client, host, gistID)
	}
	if err != nil {
		return err
	}
	if gist.ID == "" {
		fmt.Fprintln(opts.IO.Out, "未找到 gist。")
		return nil
	}

	if !opts.Confirmed {
		confirmed, err := opts.Prompter.Confirm(fmt.Sprintf("删除 gist %q 吗?", gist.Filename()), false)
		if err != nil {
			return err
		}
		if !confirmed {
			return cmdutil.CancelError
		}
	}

	apiClient := api.NewClientFromHTTP(client)
	if err := deleteGist(apiClient, host, gist.ID); err != nil {
		if errors.Is(err, shared.NotFoundErr) {
			return fmt.Errorf("无法删除 gist %q: 要么该 gist 未找到，要么它不属于你", gist.Filename())
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out,
			"%s gist %q 已删除\n",
			cs.SuccessIcon(),
			gist.Filename())
	}

	return nil
}

func deleteGist(apiClient *api.Client, hostname string, gistID string) error {
	path, err := safeurl.JoinPath("gists", gistID)
	if err != nil {
		return err
	}
	err = apiClient.REST(hostname, "DELETE", path.String(), nil, nil)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			return shared.NotFoundErr
		}
		return err
	}
	return nil
}
