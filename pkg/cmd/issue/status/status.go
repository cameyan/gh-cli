package status

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type StatusOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	Exporter cmdutil.Exporter
}

func NewCmdStatus(f *cmdutil.Factory, runF func(*StatusOptions) error) *cobra.Command {
	opts := &StatusOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "显示相关议题的状态",
		Args:  cmdutil.NoArgsQuoteReminder,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 支持 `-R, --repo` 覆盖
			opts.BaseRepo = f.BaseRepo

			if runF != nil {
				return runF(opts)
			}
			return statusRun(opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.IssueFields)

	return cmd
}

var defaultFields = []string{
	"number",
	"title",
	"url",
	"state",
	"updatedAt",
	"labels",
}

func statusRun(opts *StatusOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	currentUser, err := api.CurrentLoginName(apiClient, baseRepo.RepoHost())
	if err != nil {
		return err
	}

	options := api.IssueStatusOptions{
		Username: currentUser,
		Fields:   defaultFields,
	}
	if opts.Exporter != nil {
		options.Fields = opts.Exporter.Fields()
	}
	issuePayload, err := api.IssueStatus(apiClient, baseRepo, options)
	if err != nil {
		return err
	}

	err = opts.IO.StartPager()
	if err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器出错: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.Exporter != nil {
		data := map[string]any{
			"createdBy": issuePayload.Authored.Issues,
			"assigned":  issuePayload.Assigned.Issues,
			"mentioned": issuePayload.Mentioned.Issues,
		}
		return opts.Exporter.Write(opts.IO, data)
	}

	out := opts.IO.Out

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "位于 %s 的相关议题\n", ghrepo.FullName(baseRepo))
	fmt.Fprintln(out, "")

	prShared.PrintHeader(opts.IO, "分配给您的议题")
	if issuePayload.Assigned.TotalCount > 0 {
		issueShared.PrintIssues(opts.IO, time.Now(), "  ", issuePayload.Assigned.TotalCount, issuePayload.Assigned.Issues)
	} else {
		message := "  没有分配给您的议题"
		prShared.PrintMessage(opts.IO, message)
	}
	fmt.Fprintln(out)

	prShared.PrintHeader(opts.IO, "提及您的议题")
	if issuePayload.Mentioned.TotalCount > 0 {
		issueShared.PrintIssues(opts.IO, time.Now(), "  ", issuePayload.Mentioned.TotalCount, issuePayload.Mentioned.Issues)
	} else {
		prShared.PrintMessage(opts.IO, "  没有提及您的议题")
	}
	fmt.Fprintln(out)

	prShared.PrintHeader(opts.IO, "由您打开的议题")
	if issuePayload.Authored.TotalCount > 0 {
		issueShared.PrintIssues(opts.IO, time.Now(), "  ", issuePayload.Authored.TotalCount, issuePayload.Authored.Issues)
	} else {
		prShared.PrintMessage(opts.IO, "  没有由您打开的议题")
	}
	fmt.Fprintln(out)

	return nil
}
