package view

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	runShared "github.com/cli/cli/v2/pkg/cmd/run/shared"
	"github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	Prompter   iprompter

	Selector string
	Ref      string
	Web      bool
	Prompt   bool
	Raw      bool
	YAML     bool

	now time.Time
}

type iprompter interface {
	Select(string, string, []string) (int, error)
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Prompter:   f.Prompter,
		now:        time.Now(),
	}

	cmd := &cobra.Command{
		Use:   "view [<workflow-id> | <workflow-name> | <filename>]",
		Short: "查看工作流摘要",
		Args:  cobra.MaximumNArgs(1),
		Example: heredoc.Doc(`
			# 交互式选择工作流以查看
			$ gh workflow view

			# 查看特定工作流
			$ gh workflow view 0451
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			opts.Raw = !opts.IO.IsStdoutTTY()

			if len(args) > 0 {
				opts.Selector = args[0]
			} else if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("在非交互式运行时，需要提供工作流参数")
			} else {
				opts.Prompt = true
			}

			if !opts.YAML && opts.Ref != "" {
				return cmdutil.FlagErrorf("`--yaml` 在指定 `--ref` 时是必需的")
			}

			if runF != nil {
				return runF(opts)
			}
			return runView(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开工作流")
	cmd.Flags().BoolVarP(&opts.YAML, "yaml", "y", false, "查看工作流 YAML 文件")
	cmd.Flags().StringVarP(&opts.Ref, "ref", "r", "", "包含您要查看的工作流文件版本的分支或标签名称")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "ref")

	return cmd
}

func runView(opts *ViewOptions) error {
	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法构建 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	var workflow *shared.Workflow
	states := []shared.WorkflowState{shared.Active}
	workflow, err = shared.ResolveWorkflow(opts.Prompter, opts.IO, client, repo, opts.Prompt, opts.Selector, states)
	if err != nil {
		return err
	}

	if opts.Web {
		var address string
		if opts.YAML {
			ref := opts.Ref
			if ref == "" {
				opts.IO.StartProgressIndicator()
				ref, err = api.RepoDefaultBranch(client, repo)
				opts.IO.StopProgressIndicator()
				if err != nil {
					return err
				}
			}
			address = ghrepo.GenerateRepoURL(repo, "blob/%s/%s", url.QueryEscape(ref), url.QueryEscape(workflow.Path))
		} else {
			address = ghrepo.GenerateRepoURL(repo, "actions/workflows/%s", url.QueryEscape(workflow.Base()))
		}
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s\n", text.DisplayURL(address))
		}
		return opts.Browser.Browse(address)
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "无法启动分页器: %v\n", err)
	}

	if opts.YAML {
		err = viewWorkflowContent(opts, client, repo, workflow, opts.Ref)
	} else {
		err = viewWorkflowInfo(opts, client, repo, workflow)
	}
	if err != nil {
		return err
	}

	return nil
}

func viewWorkflowContent(opts *ViewOptions, client *api.Client, repo ghrepo.Interface, workflow *shared.Workflow, ref string) error {
	yamlBytes, err := shared.GetWorkflowContent(client, repo, *workflow, ref)
	if err != nil {
		if s, ok := err.(api.HTTPError); ok && s.StatusCode == 404 {
			if ref != "" {
				return fmt.Errorf("在 %[2]s 上找不到工作流文件 %[1]s，尝试指定其他引用", workflow.Base(), ref)
			}
			return fmt.Errorf("找不到工作流文件 %s，请尝试使用 `--ref` 指定分支或标签", workflow.Base())
		}
		return fmt.Errorf("无法获取工作流文件内容: %w", err)
	}

	yaml := string(yamlBytes)

	if !opts.Raw {
		cs := opts.IO.ColorScheme()
		out := opts.IO.Out

		fileName := workflow.Base()
		fmt.Fprintf(out, "%s - %s\n", cs.Bold(workflow.Name), cs.Muted(fileName))
		fmt.Fprintf(out, "ID: %s", cs.Cyanf("%d", workflow.ID))

		codeBlock := fmt.Sprintf("```yaml\n%s\n```", yaml)
		rendered, err := markdown.Render(codeBlock,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithoutIndentation(),
			markdown.WithWrap(0))
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(opts.IO.Out, rendered)
		return err
	}

	if _, err := fmt.Fprint(opts.IO.Out, yaml); err != nil {
		return err
	}

	if !strings.HasSuffix(yaml, "\n") {
		_, err := fmt.Fprint(opts.IO.Out, "\n")
		return err
	}

	return nil
}

func viewWorkflowInfo(opts *ViewOptions, client *api.Client, repo ghrepo.Interface, workflow *shared.Workflow) error {
	wr, err := runShared.GetRuns(client, repo, &runShared.FilterOptions{
		WorkflowID:   workflow.ID,
		WorkflowName: workflow.Name,
	}, 5)
	if err != nil {
		return fmt.Errorf("获取运行次数失败: %w", err)
	}

	out := opts.IO.Out
	cs := opts.IO.ColorScheme()

	// Header
	filename := workflow.Base()
	fmt.Fprintf(out, "%s - %s\n", cs.Bold(workflow.Name), cs.Cyan(filename))
	fmt.Fprintf(out, "ID: %s\n\n", cs.Cyanf("%d", workflow.ID))

	// Runs
	fmt.Fprintf(out, "执行次数 %d\n", wr.TotalCount)

	if wr.TotalCount != 0 {
		fmt.Fprintln(out, "最近执行记录")
	}

	headers := make([]string, 0, 8)
	if opts.Raw {
		headers = append(headers, "状态", "CONCLUSION")
	} else {
		headers = append(headers, "")
	}
	headers = append(headers, "标题", "工作流", "分支", "触发事件")
	if opts.Raw {
		headers = append(headers, "ELAPSED")
	}
	headers = append(headers, "ID")

	tp := tableprinter.New(opts.IO, tableprinter.WithHeader(headers...))
	for _, run := range wr.WorkflowRuns {
		if opts.Raw {
			tp.AddField(string(run.Status))
			tp.AddField(string(run.Conclusion))
		} else {
			symbol, symbolColor := runShared.Symbol(cs, run.Status, run.Conclusion)
			tp.AddField(symbol, tableprinter.WithColor(symbolColor))
		}

		tp.AddField(run.Title(), tableprinter.WithColor(cs.Bold))

		tp.AddField(run.WorkflowName())
		tp.AddField(run.HeadBranch, tableprinter.WithColor(cs.Bold))
		tp.AddField(string(run.Event))

		if opts.Raw {
			tp.AddField(run.Duration(opts.now).String())
		}

		tp.AddField(fmt.Sprintf("%d", run.ID), tableprinter.WithColor(cs.Cyan))

		tp.EndRow()
	}

	err = tp.Render()
	if err != nil {
		return err
	}

	fmt.Fprintln(out)

	// Footer
	if wr.TotalCount != 0 {
		fmt.Fprintf(out, "要查看此工作流的更多运行情况，请尝试: gh run list --workflow %s\n", filename)
	}
	fmt.Fprintf(out, "要查看此工作流的 YAML，请尝试: gh workflow view %s --yaml\n", filename)
	return nil
}
