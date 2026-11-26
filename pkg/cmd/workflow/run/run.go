package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type RunOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Detector   fd.Detector
	Prompter   iprompter

	Selector  string
	Ref       string
	JSONInput string
	JSON      bool

	MagicFields []string
	RawFields   []string

	Prompt bool
}

type iprompter interface {
	Input(string, string) (string, error)
	Select(string, string, []string) (int, error)
}

func NewCmdRun(f *cmdutil.Factory, runF func(*RunOptions) error) *cobra.Command {
	opts := &RunOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "run [<workflow-id> | <workflow-name>]",
		Short: "通过创建工作流调度事件来运行工作流",
		Long: heredoc.Docf(`
			为给定的工作流创建一个 %[1]sworkflow_dispatch%[1]s 事件。

			此命令将触发 GitHub Actions 运行指定的工作流文件。指定的工作流文件必须支持
			%[1]son.workflow_dispatch%[1]s 触发器，才能以这种方式运行。

			如果工作流文件支持输入，可以通过以下几种方式指定:

			- 人机交互方式
			- 通过 %[1]s-f/--raw-field%[1]s 或 %[1]s-F/--field%[1]s 参数选项
			- 作为 JSON，通过标准输入传入

			如果可用，将返回所创建工作流运行的链接。
		`, "`"),
		Example: heredoc.Doc(`
			# 让 gh 提示您选择想要运行的工作流，并交互式地收集输入
			$ gh workflow run

			# 在远程仓库的默认分支上运行工作流文件 'triage.yml'
			$ gh workflow run triage.yml

			# 在指定的分支上运行工作流文件 'triage.yml'
			$ gh workflow run triage.yml --ref my-branch

			# 通过命令行输入运行工作流文件 'triage.yml'
			$ gh workflow run triage.yml -f name=scully -f greeting=hello

			# 通过标准输入以 JSON 格式运行工作流文件 'triage.yml'
			$ echo '{"name":"scully", "greeting":"hello"}' | gh workflow run triage.yml --json
		`),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(opts.MagicFields)+len(opts.RawFields) > 0 && len(args) == 0 {
				return cmdutil.FlagErrorf("在使用 -f 或 -F 参数时，需要指定工作流参数")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			inputFieldsPassed := len(opts.MagicFields)+len(opts.RawFields) > 0

			if len(args) > 0 {
				opts.Selector = args[0]
			} else if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("在非交互式运行时，需要指定工作流 ID、名称或文件名")
			} else {
				opts.Prompt = true
			}

			if opts.JSON && !opts.IO.IsStdinTTY() {
				jsonIn, err := io.ReadAll(opts.IO.In)
				if err != nil {
					return errors.New("无法从标准输入读取数据")
				}
				opts.JSONInput = string(jsonIn)
			} else if opts.JSON {
				return cmdutil.FlagErrorf("--json 已指定，但标准输入中没有内容")
			}

			if opts.Selector == "" {
				if opts.JSONInput != "" {
					return cmdutil.FlagErrorf("传递 JSON 时需要工作流参数")
				}
			} else {
				if opts.JSON && inputFieldsPassed {
					return cmdutil.FlagErrorf("只能传递 STDIN 或 -f/-F 中的一个参数")
				}
			}

			if runF != nil {
				return runF(opts)
			}

			return runRun(opts)
		},
	}
	cmd.Flags().StringVarP(&opts.Ref, "ref", "r", "", "包含您希望运行的工作流文件版本的分支或标签名称")
	cmd.Flags().StringArrayVarP(&opts.MagicFields, "field", "F", nil, "添加一个字符串参数，格式为 `key=value`，并遵循 @ 语法(参见 \"gh help api\")")
	cmd.Flags().StringArrayVarP(&opts.RawFields, "raw-field", "f", nil, "添加一个格式为 `key=value` 的字符串参数")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "通过标准输入读取工作流输入为 JSON 格式")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "ref")

	return cmd
}

// TODO copypasta from gh api
func parseField(f string) (string, string, error) {
	idx := strings.IndexRune(f, '=')
	if idx == -1 {
		return f, "", fmt.Errorf("字段 %q 需要一个以 '=' 符号分隔的值", f)
	}
	return f[0:idx], f[idx+1:], nil
}

// TODO MODIFIED copypasta from gh api
func magicFieldValue(v string, opts RunOptions) (string, error) {
	if strings.HasPrefix(v, "@") {
		fileBytes, err := opts.IO.ReadUserFile(v[1:])
		if err != nil {
			return "", err
		}
		return string(fileBytes), nil
	}

	return v, nil
}

// TODO copypasta from gh api
func parseFields(opts RunOptions) (map[string]string, error) {
	params := make(map[string]string)
	for _, f := range opts.RawFields {
		key, value, err := parseField(f)
		if err != nil {
			return params, err
		}
		params[key] = value
	}
	for _, f := range opts.MagicFields {
		key, strValue, err := parseField(f)
		if err != nil {
			return params, err
		}
		value, err := magicFieldValue(strValue, opts)
		if err != nil {
			return params, fmt.Errorf("解析 %q 值时出错: %w", key, err)
		}
		params[key] = value
	}
	return params, nil
}

type InputAnswer struct {
	providedInputs map[string]string
}

func (ia *InputAnswer) WriteAnswer(name string, value any) error {
	if s, ok := value.(string); ok {
		ia.providedInputs[name] = s
		return nil
	}

	// TODO i hate this; this is to make tests work:
	if rv, ok := value.(reflect.Value); ok {
		ia.providedInputs[name] = rv.String()
		return nil
	}

	return fmt.Errorf("意外的值类型: %v", value)
}

func collectInputs(p iprompter, yamlContent []byte) (map[string]string, error) {
	inputs, err := findInputs(yamlContent)
	if err != nil {
		return nil, err
	}

	providedInputs := map[string]string{}

	if len(inputs) == 0 {
		return providedInputs, nil
	}

	for _, input := range inputs {
		var answer string

		if input.Type == "choice" {
			name := input.Name
			if input.Required {
				name += " (required)"
			}
			selected, err := p.Select(name, input.Default, input.Options)
			if err != nil {
				return nil, err
			}
			answer = input.Options[selected]
		} else if input.Required {
			for answer == "" {
				answer, err = p.Input(input.Name+" (required)", input.Default)
				if err != nil {
					break
				}
			}
		} else {
			answer, err = p.Input(input.Name, input.Default)
		}

		if err != nil {
			return nil, err
		}

		providedInputs[input.Name] = answer
	}
	return providedInputs, nil
}

func runRun(opts *RunOptions) error {
	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法构建 HTTP 客户端: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(c, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, repo.RepoHost())
	}

	ref := opts.Ref

	if ref == "" {
		ref, err = api.RepoDefaultBranch(client, repo)
		if err != nil {
			return fmt.Errorf("无法确定 %s 的默认分支: %w", ghrepo.FullName(repo), err)
		}
	}

	states := []shared.WorkflowState{shared.Active}
	workflow, err := shared.ResolveWorkflow(opts.Prompter,
		opts.IO, client, repo, opts.Prompt, opts.Selector, states)
	if err != nil {
		if _, ok := errors.AsType[shared.FilteredAllError](err); ok {
			return errors.New("该仓库未启用任何工作流")
		}
		return err
	}

	providedInputs := map[string]string{}

	if len(opts.MagicFields)+len(opts.RawFields) > 0 {
		providedInputs, err = parseFields(*opts)
		if err != nil {
			return err
		}
	} else if opts.JSONInput != "" {
		err := json.Unmarshal([]byte(opts.JSONInput), &providedInputs)
		if err != nil {
			return fmt.Errorf("无法解析提供的 JSON: %w", err)
		}
	} else if opts.Prompt {
		yamlContent, err := shared.GetWorkflowContent(client, repo, *workflow, ref)
		if err != nil {
			return fmt.Errorf("无法获取工作流文件内容: %w", err)
		}
		providedInputs, err = collectInputs(opts.Prompter, yamlContent)
		if err != nil {
			return err
		}
	}

	features, err := opts.Detector.ActionsFeatures()
	if err != nil {
		return err
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows", strconv.FormatInt(workflow.ID, 10), "dispatches")
	if err != nil {
		return err
	}

	requestBody := map[string]any{
		"ref":    ref,
		"inputs": providedInputs,
	}

	// TODO workflowDispatchRunDetailsCleanup
	// We will have to always set the `return_run_details` field to true, unless
	// we opt into the new REST API version, which will probably return the
	// details by default.
	if features.DispatchRunDetails {
		requestBody["return_run_details"] = true
	}

	requestByte, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("无法序列化工作流输入: %w", err)
	}

	body := bytes.NewReader(requestByte)

	var response struct {
		WorkflowRunID int64  `json:"workflow_run_id"`
		RunURL        string `json:"run_url"`
		HtmlURL       string `json:"html_url"`
	}

	// Note that the workflow dispatch endpoint used to return 204 No Content
	// (with no body, obviously). Now it's possible for the endpoint to also
	// return 200 OK with created run details. So, we have to handle both cases
	// because old GHE versions still return 204. Even on github.com, we
	// may still get 204 for any reason.
	//
	// Our REST client library is smart enough to ignore JSON unmarshal when it
	// receives 204, so we're safe here anyway.
	//
	// As a related note, the new REST API version (which will come with breaking
	// changes) will probably default to return 200 + run details.
	err = client.REST(repo.RepoHost(), "POST", path.String(), body, &response)
	if err != nil {
		return fmt.Errorf("无法创建工作流调度事件: %w", err)
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		fmt.Fprintf(opts.IO.Out, "%[1]s 在 %[3]s 中为 %[2]s 创建了 workflow_dispatch 事件\n",
			cs.SuccessIcon(), cs.Cyan(workflow.Base()), cs.Bold(ref))

		if response.HtmlURL != "" {
			fmt.Fprintln(opts.IO.Out, response.HtmlURL)
		}

		fmt.Fprintln(opts.IO.Out)

		if response.WorkflowRunID != 0 {
			fmt.Fprintf(opts.IO.Out, "要查看已创建的工作流运行情况，请尝试: %s\n",
				cs.Boldf("gh run view %d", response.WorkflowRunID))
		}

		fmt.Fprintf(opts.IO.Out, "要查看此工作流的运行情况，请尝试: %s\n",
			cs.Boldf("gh run list --workflow=%q", workflow.Base()))
	} else {
		if response.HtmlURL != "" {
			fmt.Fprintln(opts.IO.Out, response.HtmlURL)
		}
	}

	return nil
}

type WorkflowInput struct {
	Name        string
	Required    bool
	Default     string
	Description string
	Type        string
	Options     []string
}

func findInputs(yamlContent []byte) ([]WorkflowInput, error) {
	var rootNode yaml.Node
	err := yaml.Unmarshal(yamlContent, &rootNode)
	if err != nil {
		return nil, fmt.Errorf("无法解析工作流 YAML: %w", err)
	}

	if len(rootNode.Content) != 1 {
		return nil, errors.New("无效的 YAML 文件")
	}

	var onKeyNode *yaml.Node
	var dispatchKeyNode *yaml.Node
	var inputsKeyNode *yaml.Node
	var inputsMapNode *yaml.Node

	// TODO this is pretty hideous
	for _, node := range rootNode.Content[0].Content {
		if onKeyNode != nil {
			if node.Kind == yaml.MappingNode {
				for _, node := range node.Content {
					if dispatchKeyNode != nil {
						for _, node := range node.Content {
							if inputsKeyNode != nil {
								inputsMapNode = node
								break
							}
							if node.Value == "inputs" {
								inputsKeyNode = node
							}
						}
						break
					}
					if node.Value == "workflow_dispatch" {
						dispatchKeyNode = node
					}
				}
			} else if node.Kind == yaml.SequenceNode {
				for _, node := range node.Content {
					if node.Value == "workflow_dispatch" {
						dispatchKeyNode = node
						break
					}
				}
			} else if node.Kind == yaml.ScalarNode {
				if node.Value == "workflow_dispatch" {
					dispatchKeyNode = node
					break
				}
			}
			break
		}
		if strings.EqualFold(node.Value, "on") {
			onKeyNode = node
		}
	}

	if onKeyNode == nil {
		return nil, errors.New("无效的工作流: 缺少 'on' 字段")
	}

	if dispatchKeyNode == nil {
		return nil, errors.New("无法在没有 workflow_dispatch 事件的情况下手动运行工作流")
	}

	out := []WorkflowInput{}

	m := map[string]WorkflowInput{}

	if inputsKeyNode == nil || inputsMapNode == nil {
		return out, nil
	}

	err = inputsMapNode.Decode(&m)
	if err != nil {
		return nil, fmt.Errorf("无法解码工作流输入内容: %w", err)
	}

	for name, input := range m {
		if input.Type == "choice" && len(input.Options) == 0 {
			return nil, fmt.Errorf("工作流输入 %q 的类型为选择，但没有选项", name)
		}
		out = append(out, WorkflowInput{
			Name:        name,
			Default:     input.Default,
			Description: input.Description,
			Required:    input.Required,
			Options:     input.Options,
			Type:        input.Type,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return out, nil
}
