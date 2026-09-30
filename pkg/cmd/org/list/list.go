package list

import (
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	HttpClient func() (*http.Client, error)

	Limit int
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := ListOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.MaximumNArgs(1),
		Short: "列出已认证用户所属的组织。",
		Example: heredoc.Doc(`
			# 列出前 30 个组织
			$ gh org list

			# 列出更多组织
			$ gh org list --limit 100
		`),
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit 值: %v", opts.Limit)
			}

			if runF != nil {
				return runF(&opts)
			}
			return listRun(&opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 30, "要列出的组织数量上限")

	return cmd
}

func listRun(opts *ListOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	listResult, err := listOrgs(httpClient, host, opts.Limit)
	if err != nil {
		return err
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器出错: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.IO.IsStdoutTTY() {
		header := listHeader(listResult.User, len(listResult.Organizations), listResult.TotalCount)
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", header)
	}

	for _, org := range listResult.Organizations {
		fmt.Fprintln(opts.IO.Out, org.Login)
	}

	return nil
}

func listHeader(user string, resultCount, totalCount int) string {
	if totalCount == 0 {
		return "@" + user + " 没有关联任何组织"
	}

	return fmt.Sprintf("正在显示 %d 个组织（共 %d 个）", resultCount, totalCount)
}
