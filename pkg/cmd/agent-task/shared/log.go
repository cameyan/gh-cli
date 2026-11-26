package shared

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
)

//go:generate moq -rm -out log_mock.go . LogRenderer

type LogRenderer interface {
	Follow(fetcher func() ([]byte, error), w io.Writer, io *iostreams.IOStreams) error
	Render(logs []byte, w io.Writer, io *iostreams.IOStreams) (stop bool, err error)
}

type logRenderer struct{}

func NewLogRenderer() LogRenderer {
	return &logRenderer{}
}

// Follow 使用提供的 fetcher 函数持续获取日志，
// 并将其渲染到指定的 writer。当 Render 指示停止时结束。
func (r *logRenderer) Follow(fetcher func() ([]byte, error), w io.Writer, io *iostreams.IOStreams) error {
	var last string
	for {
		raw, err := fetcher()
		if err != nil {
			return err
		}

		logs := string(raw)
		if logs == last {
			continue
		}

		diff := strings.TrimSpace(logs[len(last):])

		if stop, err := r.Render([]byte(diff), w, io); err != nil {
			return err
		} else if stop {
			return nil
		}

		last = logs
	}
}

// Render 处理给定的日志并将渲染后的输出写入 w。
// 当遇到意外的日志条目时返回错误。
func (r *logRenderer) Render(logs []byte, w io.Writer, io *iostreams.IOStreams) (bool, error) {
	lines := slices.DeleteFunc(strings.Split(string(logs), "\n"), func(line string) bool {
		return line == ""
	})

	for _, line := range lines {
		raw, found := strings.CutPrefix(line, "data: ")
		if !found {
			return false, errors.New("意外的日志格式")
		}

		// 我们唯一关心的日志条目类型是 chat completion chunk，
		// 可以通过成功反序列化为对应类型，且 Object 字段等于 "chat.completion.chunk"
		// 来验证。后者是为了避免接受空的 JSON 对象（即 "{}"）。此外，
		// 如果条目不是我们期望的内容，应直接跳过，避免返回错误。
		var entry chatCompletionChunkEntry
		err := json.Unmarshal([]byte(raw), &entry)
		if err != nil || entry.Object != "chat.completion.chunk" {
			continue
		}

		if stop, err := renderLogEntry(entry, w, io); err != nil {
			return false, fmt.Errorf("处理日志条目失败: %w", err)
		} else if stop {
			return true, nil
		}
	}

	return false, nil
}

func renderLogEntry(entry chatCompletionChunkEntry, w io.Writer, io *iostreams.IOStreams) (bool, error) {
	cs := io.ColorScheme()
	var stop bool
	for _, choice := range entry.Choices {
		if choice.FinishReason == "stop" {
			stop = true
		}

		if len(choice.Delta.ToolCalls) == 0 {
			if !choice.Delta.Content.Empty() && choice.Delta.Role == "assistant" {
				// Copilot 消息，应当显示。
				renderRawMarkdown(choice.Delta.Content.String(), w, io)
			}
			continue
		}

		// 由于我们不希望清除并重新打印事件的实时进度，
		// 只需处理对应已完成的 tool call 的条目。
		// 这类条目具有非空的 Content 字段。
		if choice.Delta.Content.Empty() {
			continue
		}

		if !choice.Delta.ReasoningText.Empty() {
			// 注意：这里应格式化为普通的 "thought" 消息，
			// without the heading.
			renderRawMarkdown(choice.Delta.ReasoningText.String(), w, io)
		}

		for _, tc := range choice.Delta.ToolCalls {
			name := tc.Function.Name.Raw()
			if name == "" {
				continue
			}

			args := tc.Function.Arguments

			switch name {
			case "run_setup":
				if v := unmarshal[runSetupToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, v.Name.String(), "")
					continue
				}
			case "view":
				args := viewToolArgs{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					fmt.Fprintf(io.ErrOut, "\n解析 'view' 工具调用参数失败: %v\n", err)
					continue
				}
				renderToolCallTitle(w, cs, fmt.Sprintf("查看 %s", cs.Bold(relativeFilePath(args.Path.String()))), "")

				content := stripDiffFormat(choice.Delta.Content.String())

				if err := renderFileContentAsMarkdown(args.Path.String(), content, w, io); err != nil {
					fmt.Fprintf(io.ErrOut, "\n渲染已查看文件的内容失败: %v\n\n", err)
					fmt.Fprintln(io.ErrOut, content) // raw fallback
				}
			case "bash":
				if v := unmarshal[bashToolArgs](args); v != nil {
					if !v.Description.Empty() {
						renderToolCallTitle(w, cs, "Bash", v.Description.String())
					} else {
						renderToolCallTitle(w, cs, "运行 Bash 命令", "")
					}

					contentWithCommand := choice.Delta.Content.String()
					if !v.Command.Empty() {
						contentWithCommand = fmt.Sprintf("$ %s\n%s", v.Command, choice.Delta.Content.String())
					}
					if err := renderFileContentAsMarkdown("commands.sh", contentWithCommand, w, io); err != nil {
						fmt.Fprintf(io.ErrOut, "\n渲染 Bash 命令输出失败: %v\n\n", err)
						fmt.Fprintln(io.ErrOut, contentWithCommand)
					}
				}
			// TODO: 考虑为这些与 Bash 相关的 tool call 包含更多细节。
			case "write_bash":
				if v := unmarshal[writeBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "向 Bash 会话发送输入", "")
					continue
				}
			case "read_bash":
				if v := unmarshal[readBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "读取 Bash 会话的日志", "")
					continue
				}
			case "stop_bash":
				if v := unmarshal[stopBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "停止 Bash 会话", "")
					continue
				}
			case "async_bash":
				if v := unmarshal[asyncBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "启动长时间运行的 Bash 会话或向其发送输入", "")
					continue
				}
			case "read_async_bash":
				if v := unmarshal[readAsyncBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "查看长时间运行的 Bash 会话的日志", "")
					continue
				}
			case "stop_async_bash":
				if v := unmarshal[stopAsyncBashToolArgs](args); v != nil {
					renderToolCallTitle(w, cs, "停止长时间运行的 Bash 会话", "")
					continue
				}
			case "think":
				args := thinkToolArgs{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					fmt.Fprintf(io.ErrOut, "\n解析 'think' 工具调用参数失败: %v\n", err)
					continue
				}

				// NOTE: omit the delta.content since it's the same as thought
				renderToolCallTitle(w, cs, "思考", "")
				if err := renderRawMarkdown(args.Thought.String(), w, io); err != nil {
					fmt.Fprintf(io.ErrOut, "\n渲染思考失败: %v\n", err)
				}
			case "report_progress":
				args := reportProgressToolArgs{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					fmt.Fprintf(io.ErrOut, "\n解析 'report_progress' 工具调用参数失败: %v\n", err)
					continue
				}

				renderToolCallTitle(w, cs, "进度更新", cs.Bold(args.CommitMessage.String()))
				if !args.PrDescription.Empty() {
					if err := renderRawMarkdown(args.PrDescription.String(), w, io); err != nil {
						fmt.Fprintf(io.ErrOut, "\n渲染 PR 描述失败: %v\n", err)
					}
				}

				// TODO: KW 我始终未能让这个分支被触发。
				if !choice.Delta.Content.Empty() {
					// 尝试将其视为 JSON
					if err := renderContentAsJSONMarkdown("", choice.Delta.Content.String(), w, io); err != nil {
						fmt.Fprintf(io.ErrOut, "\n渲染进度更新内容失败: %v\n", err)
					}
				}

			case "create":
				args := createToolArgs{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					fmt.Fprintf(io.ErrOut, "\n解析 'create' 工具调用参数失败: %v\n", err)
					continue
				}
				renderToolCallTitle(w, cs, "创建", cs.Bold(relativeFilePath(args.Path.String())))

				if err := renderFileContentAsMarkdown(args.Path.String(), args.FileText.String(), w, io); err != nil {
					fmt.Fprintf(io.ErrOut, "\n渲染已创建文件的内容失败: %v\n\n", err)
					fmt.Fprintln(io.ErrOut, args.FileText)
				}
			case "str_replace":
				args := strReplaceToolArgs{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					fmt.Fprintf(io.ErrOut, "\n解析 'str_replace' 工具调用参数失败: %v\n", err)
					continue
				}

				renderToolCallTitle(w, cs, "编辑", cs.Bold(relativeFilePath(args.Path.String())))
				if err := renderFileContentAsMarkdown("output.diff", choice.Delta.Content.String(), w, io); err != nil {
					fmt.Fprintf(io.ErrOut, "\n渲染 str_replace 差异失败: %v\n\n", err)
					fmt.Fprintln(io.ErrOut, choice.Delta.Content.String())
				}
			default:
				// Unknown tool call. For example for "codeql_checker":
				// NOTE: omit the delta.content since we don't know how large could that be
				renderGenericToolCall(w, cs, tc.Function.Name)

				// 如果是 JSON，则按 JSON 处理，否则跳过内容。
				_ = renderContentAsJSONMarkdown("输出: ", choice.Delta.Content.String(), w, io)

				// The entirety of the args can be treated as "input" to the tool call.
				// We try to render it as JSON, but if that fails, just skip it.
				_ = renderContentAsJSONMarkdown("输入: ", args, w, io)
			}
		}
	}
	return stop, nil
}

// renderContentAsJSONMarkdown 尝试将给定内容反序列化为 JSON，
// 将其包装在 markdown JSON 代码块中，并渲染为 markdown。
// 如果 label 非空，则将其渲染为 JSON 块之前（块外）的前导文本。
func renderContentAsJSONMarkdown(label, content string, w io.Writer, io *iostreams.IOStreams) error {
	var contentAsJSON any
	if err := json.Unmarshal([]byte(content), &contentAsJSON); err == nil {
		marshaled, err := json.MarshalIndent(contentAsJSON, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化 JSON 失败: %w", err)
		}

		if label != "" {
			if err := renderRawMarkdown(label, w, io); err != nil {
				return fmt.Errorf("渲染标签失败: %w", err)
			}
		}

		if err := renderFileContentAsMarkdown("output.json", string(marshaled), w, io); err != nil {
			return fmt.Errorf("渲染 JSON 失败: %w", err)
		}
	}
	return nil
}

// renderRawMarkdown 将给定的原始 markdown 字符串渲染到指定的 writer。
// 用于无需转换的完整 markdown 内容的 tool call。
func renderRawMarkdown(md string, w io.Writer, io *iostreams.IOStreams) error {
	// 当内容是完整的 markdown 文档时，Glamour 不会添加前导换行。
	// 因此我们必须自行添加前导换行。
	formatFunc := func(s string) string {
		return fmt.Sprintf("\n%s\n\n", s)
	}

	return renderMarkdownWithFormat(md, w, io, formatFunc)
}

// renderMarkdownWithFormat 将给定的 markdown 字符串渲染到指定的 writer。
// 如果提供了 formatFunc，则 md 字符串会在渲染前经过它处理。
// 这可用于在内容前后添加换行。
func renderMarkdownWithFormat(md string, w io.Writer, io *iostreams.IOStreams, formatFunc func(string) string) error {
	rendered, err := markdown.Render(md,
		markdown.WithTheme(io.TerminalTheme()),
		markdown.WithWrap(io.TerminalWidth()),
	)

	if err != nil {
		return fmt.Errorf("渲染 markdown 失败: %w", err)
	}

	rendered = strings.TrimSpace(rendered)
	if formatFunc != nil {
		rendered = formatFunc(rendered)
	}

	fmt.Fprint(w, rendered)

	return nil
}

// stripDiffFormat 通过移除 diff 特有的格式，
// 实现从 diff 字符串到纯文本表示的简单转换。
func stripDiffFormat(diff string) string {
	lines := strings.Split(diff, "\n")

	// 查找 hunk 头的结束位置。
	hunkEndIndex := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			hunkEndIndex = i
			break
		}
	}

	// 这不是 diff。
	if hunkEndIndex == -1 {
		return diff
	}

	// 移除 hunk 头。
	lines = lines[hunkEndIndex+1:]

	// 如果行首存在 + 和 -，则将其移除。
	var stripped []string
	for _, line := range lines {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			stripped = append(stripped, line[1:])
		} else {
			stripped = append(stripped, line)
		}
	}
	return strings.Join(stripped, "\n")
}

// renderFileContentAsMarkdown 根据路径的文件扩展名，
// 将给定内容渲染为 markdown。
func renderFileContentAsMarkdown(path, content string, w io.Writer, io *iostreams.IOStreams) error {
	lang := filepath.Ext(filepath.ToSlash(path))

	if lang == ".md" {
		return renderRawMarkdown(content, w, io)
	}

	md := fmt.Sprintf("```%s\n%s\n```", lang, content)
	// 当内容只是代码块时，Glamour 会添加前导换行，
	// 因此我们只需添加末尾换行。
	formatFunc := func(s string) string {
		return fmt.Sprintf("%s\n\n", s)
	}

	return renderMarkdownWithFormat(md, w, io, formatFunc)
}

// relativeFilePath 将绝对文件路径转换为相对路径。
// 我们期望路径的形如: /home/runner/work/<repo-owner>/<repo-name>/path/to/file
// 上述示例的期望输出为: path/to/file
func relativeFilePath(absPath string) string {
	relPath := strings.TrimPrefix(absPath, "/home/runner/work/")

	parts := strings.Split(relPath, "/")

	// 路径的最后两部分是
	// 仓库名和仓库所有者。
	// 如果我们只有这些（或更少），
	// 则返回友好名称 "repository"。
	if len(parts) > 2 {
		// 移除仓库所有者和名称，返回剩余路径。
		return strings.Join(parts[2:], "/")
	}
	return "repository"
}

func unmarshal[T any](raw string) *T {
	var t T
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil
	}
	return &t
}

// renderToolCallTitle renders a title for a tool call. Should be followed by a
// call to render a markdown representation of the tool call's content.
func renderToolCallTitle(w io.Writer, cs *iostreams.ColorScheme, toolName, title string) {
	// Should not happen, but if it does we still want to print a heading
	// with the information we do have.
	if toolName == "" {
		toolName = "通用工具调用"
	}

	if title != "" {
		title = cs.Bold(title)
	}

	if title != "" {
		fmt.Fprintf(w, "%s: %s\n", toolName, title)
	} else {
		fmt.Fprintf(w, "%s\n", toolName)
	}
}

// genericToolCallNamesToTitles maps known generic tool call identifiers to human-friendly titles.
var genericToolCallNamesToTitles = map[string]string{
	// Custom tools, the GitHub UI doesn't currently have these.
	"codeql_checker": "运行 CodeQL 分析",

	// Playwright tools.
	"playwright-browser_navigate":         "在 Playwright 浏览器中导航至某个链接",
	"playwright-browser_navigate_back":    "在 Playwright 浏览器中返回上一页",
	"playwright-browser_navigate_forward": "在 Playwright 浏览器中前进到下一页",
	"playwright-browser_click":            "在 Playwright 浏览器中点击元素",
	"playwright-browser_take_screenshot":  "截取 Playwright 浏览器的截图",
	"playwright-browser_type":             "在 Playwright 浏览器中输入文本",
	"playwright-browser_wait_for":         "等待 Playwright 浏览器中的文本出现/消失",
	"playwright-browser_evaluate":         "在 Playwright 浏览器中运行 JavaScript",
	"playwright-browser_snapshot":         "截取 Playwright 浏览器页面的快照",
	"playwright-browser_resize":           "调整 Playwright 浏览器窗口大小",
	"playwright-browser_close":            "关闭 Playwright 浏览器",
	"playwright-browser_press_key":        "在 Playwright 浏览器中按下按键",
	"playwright-browser_select_option":    "在 Playwright 浏览器中选择选项",
	"playwright-browser_handle_dialog":    "与 Playwright 浏览器中的对话框进行交互",
	"playwright-browser_console_messages": "从 Playwright 浏览器获取控制台消息",
	"playwright-browser_drag":             "在 Playwright 浏览器中在元素之间拖动鼠标",
	"playwright-browser_file_upload":      "在 Playwright 浏览器中上传文件",
	"playwright-browser_hover":            "在 Playwright 浏览器中将鼠标悬停在元素上",
	"playwright-browser_network_requests": "从 Playwright 浏览器获取网络请求数据",

	// GitHub MCP server common tools
	"github-mcp-server-get_file_contents":              "从 GitHub 获取文件内容",
	"github-mcp-server-get_pull_request":               "从 GitHub 获取拉取请求",
	"github-mcp-server-get_issue":                      "从 GitHub 获取问题",
	"github-mcp-server-get_pull_request_files":         "从 GitHub 获取拉取请求变更的文件",
	"github-mcp-server-list_pull_requests":             "列出 GitHub 上的拉取请求",
	"github-mcp-server-list_branches":                  "列出 GitHub 上的分支",
	"github-mcp-server-get_pull_request_diff":          "从 GitHub 获取拉取请求差异",
	"github-mcp-server-get_pull_request_comments":      "从 GitHub 获取拉取请求评论",
	"github-mcp-server-get_commit":                     "从 GitHub 获取提交",
	"github-mcp-server-search_repositories":            "在 GitHub 上搜索仓库",
	"github-mcp-server-search_code":                    "在 GitHub 上搜索代码",
	"github-mcp-server-get_issue_comments":             "从 GitHub 获取问题评论",
	"github-mcp-server-list_issues":                    "列出 GitHub 上的问题",
	"github-mcp-server-search_pull_requests":           "在 GitHub 上搜索拉取请求",
	"github-mcp-server-list_commits":                   "列出 GitHub 上的提交",
	"github-mcp-server-get_pull_request_status":        "从 GitHub 获取拉取请求状态",
	"github-mcp-server-search_issues":                  "在 GitHub 上搜索问题",
	"github-mcp-server-get_pull_request_reviews":       "从 GitHub 获取拉取请求评审",
	"github-mcp-server-download_workflow_run_artifact": "下载 GitHub Actions 工作流运行工件",
	"github-mcp-server-get_job_logs":                   "获取 GitHub Actions 作业日志",
	"github-mcp-server-get_workflow_run":               "获取 GitHub Actions 工作流运行",
	"github-mcp-server-get_workflow_run_logs":          "获取 GitHub Actions 工作流运行日志",
	"github-mcp-server-get_workflow_run_usage":         "获取 GitHub Actions 工作流使用情况",
	"github-mcp-server-list_workflow_jobs":             "列出 GitHub Actions 工作流作业",
	"github-mcp-server-list_workflow_run_artifacts":    "列出 GitHub Actions 工作流运行工件",
	"github-mcp-server-list_workflow_runs":             "列出 GitHub Actions 工作流运行",
	"github-mcp-server-list_workflows":                 "列出 GitHub Actions 工作流",
}

func renderGenericToolCall(w io.Writer, cs *iostreams.ColorScheme, name iostreams.Untrusted) {
	toolName, ok := genericToolCallNamesToTitles[name.Raw()]
	if !ok {
		toolName = fmt.Sprintf("调用 %s", name)
	}

	renderToolCallTitle(w, cs, toolName, "")
}

type chatCompletionChunkEntry struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			ReasoningText iostreams.Untrusted `json:"reasoning_text"`
			Content       iostreams.Untrusted `json:"content"`
			Role          string              `json:"role"`
			ToolCalls     []struct {
				Function struct {
					Name      iostreams.Untrusted `json:"name"`
					Arguments string              `json:"arguments"`
				} `json:"function"`
				Index int    `json:"index"`
				ID    string `json:"id"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
		Index        int    `json:"index"`
	} `json:"choices"`
}

// Tool call arguments are JSON encoded inside each log entry, so decoding them
// can produce control characters. Fields that get printed are Untrusted.

type runSetupToolArgs struct {
	Name iostreams.Untrusted `json:"name"`
}

type bashToolArgs struct {
	Command     iostreams.Untrusted `json:"command"`
	Description iostreams.Untrusted `json:"description"`
}

type readBashToolArgs struct {
	SessionID string `json:"sessionId"`
}

type writeBashToolArgs struct {
	SessionID string `json:"sessionId"`
	Input     string `json:"input"`
}

type stopBashToolArgs struct {
	SessionID string `json:"sessionId"`
}

type asyncBashToolArgs struct {
	Command   string `json:"command"`
	SessionID string `json:"sessionId"`
}

type readAsyncBashToolArgs struct {
	SessionID string `json:"sessionId"`
}

type stopAsyncBashToolArgs struct {
	SessionID string `json:"sessionId"`
}

type viewToolArgs struct {
	Path iostreams.Untrusted `json:"path"`
}
type thinkToolArgs struct {
	SessionID string              `json:"sessionId"`
	Thought   iostreams.Untrusted `json:"thought"`
}

type reportProgressToolArgs struct {
	CommitMessage iostreams.Untrusted `json:"commitMessage"`
	PrDescription iostreams.Untrusted `json:"prDescription"`
}

type createToolArgs struct {
	FileText iostreams.Untrusted `json:"file_text"`
	Path     iostreams.Untrusted `json:"path"`
}

type strReplaceToolArgs struct {
	NewStr string              `json:"new_str"`
	OldStr string              `json:"old_str"`
	Path   iostreams.Untrusted `json:"path"`
}
