package disable

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DisableOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter

	Selector string
	Prompt   bool
}

type iprompter interface {
	Select(string, string, []string) (int, error)
}

func NewCmdDisable(f *cmdutil.Factory, runF func(*DisableOptions) error) *cobra.Command {
	opts := &DisableOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "disable [<workflow-id> | <workflow-name>]",
		Short: "禁用一个工作流",
		Long:  "禁用一个工作流，防止其在运行或在工作流列表中显示",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.Selector = args[0]
			} else if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时需要工作流 ID 或名称")
			} else {
				opts.Prompt = true
			}

			if runF != nil {
				return runF(opts)
			}
			return runDisable(opts)
		},
	}

	return cmd
}

func runDisable(opts *DisableOptions) error {
	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法构建 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	states := []shared.WorkflowState{shared.Active}
	workflow, err := shared.ResolveWorkflow(
		opts.Prompter, opts.IO, client, repo, opts.Prompt, opts.Selector, states)
	if err != nil {
		if _, ok := errors.AsType[shared.FilteredAllError](err); ok {
			return errors.New("没有已启用的工作流可供禁用")
		}
		return err
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows", strconv.FormatInt(workflow.ID, 10), "disable")
	if err != nil {
		return err
	}
	err = client.REST(repo.RepoHost(), "PUT", path.String(), nil, nil)
	if err != nil {
		return fmt.Errorf("禁用工作流失败: %w", err)
	}

	if opts.IO.CanPrompt() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "%s 已禁用 %s\n", cs.SuccessIconWithColor(cs.Red), cs.Bold(workflow.Name))
	}

	return nil
}
