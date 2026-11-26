package list

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const defaultLimit = 50

type ListOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Exporter   cmdutil.Exporter

	All   bool
	Limit int
}

var workflowFields = []string{
	"id",
	"name",
	"path",
	"state",
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "列出工作流",
		Long:    "列出工作流文件，默认隐藏已禁用的工作流",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效限制值: %v", opts.Limit)
			}

			if runF != nil {
				return runF(opts)
			}

			return listRun(opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", defaultLimit, "可获取的工作流数量上限")
	cmd.Flags().BoolVarP(&opts.All, "all", "a", false, "包含已禁用的工作流")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, workflowFields)
	return cmd
}

func listRun(opts *ListOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法创建 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(httpClient)

	opts.IO.StartProgressIndicator()
	workflows, err := shared.GetWorkflows(client, repo, opts.Limit)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return fmt.Errorf("获取工作流失败: %w", err)
	}

	var filteredWorkflows []shared.Workflow
	if opts.All {
		filteredWorkflows = workflows
	} else {
		for _, workflow := range workflows {
			if !workflow.Disabled() {
				filteredWorkflows = append(filteredWorkflows, workflow)
			}
		}
	}

	if len(filteredWorkflows) == 0 {
		return cmdutil.NewNoResultsError("未找到工作流")
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, filteredWorkflows)
	}

	cs := opts.IO.ColorScheme()
	tp := tableprinter.New(opts.IO, tableprinter.WithHeader("名称", "状态", "ID"))

	for _, workflow := range filteredWorkflows {
		tp.AddField(workflow.Name)
		tp.AddField(string(workflow.State))
		tp.AddField(fmt.Sprintf("%d", workflow.ID), tableprinter.WithColor(cs.Cyan))
		tp.EndRow()
	}

	return tp.Render()
}
