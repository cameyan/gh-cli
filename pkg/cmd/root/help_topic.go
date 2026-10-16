package root

import (
	"fmt"
	"io"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type helpTopic struct {
	name    string
	short   string
	long    string
	example string
}

var HelpTopics = []helpTopic{
	{
		name:  "mintty",
		short: "关于在 MinTTY 中使用 gh 的信息",
		long: heredoc.Docf(`
			MinTTY 是 Git for Windows 默认自带的终端模拟器。
			它在 gh 提示用户输入的能力上存在已知问题。

			有几种变通方法可以让 gh 在 MinTTY 中正常工作:

			- 重新安装 Git for Windows，勾选 "Enable experimental support for pseudo consoles"。

			- 在 Git for Windows 中使用其他终端模拟器，例如 Windows Terminal。
			  您可以在任意终端模拟器中运行 %[1]sC:\Program Files\Git\bin\bash.exe%[1]s，
			  以在不使用 MinTTY 的情况下继续使用 Git for Windows 中的全部工具。

			- 在 gh 调用前加上 %[1]swinpty%[1]s 前缀，例如: %[1]swinpty gh auth login%[1]s。
			  注意: 这可能导致一些界面(UI)上的问题。
		`, "`"),
	},
	{
		name:  "environment",
		short: "可与 gh 一起使用的环境变量",
		long: heredoc.Docf(`
			%[1]sGH_TOKEN%[1]s、%[1]sGITHUB_TOKEN%[1]s (按优先级排序): 当命令的目标是 %[1]sgithub.com%[1]s
			或 %[1]sghe.com%[1]s 的子域时使用的身份验证 token。设置它可以避免触发身份验证提示，
			并且优先于先前存储的凭证。

			%[1]sGH_ENTERPRISE_TOKEN%[1]s、%[1]sGITHUB_ENTERPRISE_TOKEN%[1]s (按优先级排序): 当命令的目标是
			GitHub Enterprise Server 主机时使用的身份验证 token。

			%[1]sGH_HOST%[1]s: 在未提供主机名、或无法从本地 Git 仓库的上下文中推断主机名时，
			为命令指定 GitHub 主机名。若该主机此前已通过身份验证，则使用存储的凭证；
			否则必须设置 %[1]sGH_TOKEN%[1]s 或 %[1]sGH_ENTERPRISE_TOKEN%[1]s，取决于目标主机。

			%[1]sGH_REPO%[1]s: 以 %[1]s[HOST/]OWNER/REPO%[1]s 格式指定 GitHub 仓库，
			供那些原本在本地仓库上操作的命令使用。

			%[1]sGH_EDITOR%[1]s、%[1]sGIT_EDITOR%[1]s、%[1]sVISUAL%[1]s、%[1]sEDITOR%[1]s (按优先级排序):
			用于撰写文本的编辑器工具。

			%[1]sGH_BROWSER%[1]s、%[1]sBROWSER%[1]s (按优先级排序): 用于打开链接的 web 浏览器。

			%[1]sGH_DEBUG%[1]s: 设置为真值(truthy)以在标准错误上启用详细输出。设置为 %[1]sapi%[1]s
			可额外记录 HTTP 流量的细节。

			%[1]sDEBUG%[1]s (已废弃): 设置为 %[1]s1%[1]s、%[1]strue%[1]s 或 %[1]syes%[1]s 以在标准错误上启用详细输出。

			%[1]sGH_PAGER%[1]s、%[1]sPAGER%[1]s (按优先级排序): 用于接收标准输出的终端分页程序，
			例如 %[1]sless%[1]s。

			%[1]sGLAMOUR_STYLE%[1]s: 用于渲染 Markdown 的样式。参见
			<https://github.com/charmbracelet/glamour#styles>

			%[1]sNO_COLOR%[1]s: 设置为任意值，以避免在输出中打印用于着色的 ANSI 转义序列。

			%[1]sCLICOLOR%[1]s: 设置为 %[1]s0%[1]s 以禁止在输出中打印 ANSI 颜色。

			%[1]sCLICOLOR_FORCE%[1]s: 设置为 %[1]s0%[1]s 以外的值，即使输出被管道转发
			也在输出中保留 ANSI 颜色。

			%[1]sGH_COLOR_LABELS%[1]s: 设置为任意值，在支持 truecolor 的终端中
			使用标签的 RGB 十六进制颜色码来显示标签。

			%[1]sGH_ACCESSIBLE_COLORS%[1]s (预览): 设置为真值以使用可自定义的 4 位无障碍颜色。

			%[1]sGH_FORCE_TTY%[1]s: 设置为任意值，即使输出被重定向也强制使用终端风格的输出。
			当值为数字时，它被解释为视口(viewport)中可用的列数。当值为百分比时，
			它将基于当前视口中可用的列数进行计算。

			%[1]sGH_NO_UPDATE_NOTIFIER%[1]s: 设置为任意值以禁用 GitHub CLI 的更新通知。
			每当执行任何命令时，gh 每 24 小时检查一次新版本。
			若发现更新的版本，将在标准错误上显示升级提示。

			%[1]sGH_NO_EXTENSION_UPDATE_NOTIFIER%[1]s: 设置为任意值以禁用 GitHub CLI 扩展的更新通知。
			每当执行扩展时，gh 每 24 小时为被执行的扩展检查一次新版本。
			若发现更新的版本，将在标准错误上显示升级提示。

			%[1]sGH_EXTENSION%[1]s: gh 在调用扩展时将其设置为 %[1]s1%[1]s，使扩展能够判断
			它是以 %[1]sgh <extension>%[1]s 的方式运行，还是作为独立程序直接运行。

			%[1]sGH_CONFIG_DIR%[1]s: gh 存储配置文件的目录。若未指定，
			默认值将为以下路径之一 (按优先级排序):
			  - %[1]s$XDG_CONFIG_HOME/gh%[1]s (若已设置 %[1]s$XDG_CONFIG_HOME%[1]s)，
			  - %[1]s$AppData/GitHub CLI%[1]s (Windows 上若已设置 %[1]s$AppData%[1]s)，或
			  - %[1]s$HOME/.config/gh%[1]s。

			%[1]sGH_PROMPT_DISABLED%[1]s: 设置为任意值以禁用终端中的交互式提示。

			%[1]sGH_PATH%[1]s: 设置 gh 可执行文件的路径，当 gh 无法正确确定其自身路径时很有用，
			例如在 cygwin 终端中。gh 在调用扩展时也会设置它，以便扩展可以回调同一个 gh 可执行文件。

			%[1]sGH_MDWIDTH%[1]s: Markdown 渲染换行的默认最大宽度。若未指定，终端上换行的
			最大行宽将取终端宽度、该值与 120 三者中的较小者。例如 %[1]spr view%[1]s 子命令会使用该值。

			%[1]sGH_ACCESSIBLE_PROMPTER%[1]s (预览): 设置为真值以启用与语音合成及盲文(braille)
			读屏软件更兼容的提示。

			%[1]sGH_TELEMETRY%[1]s: 设置为 %[1]slog%[1]s 可将遥测数据打印到标准错误而不是发送。
			设置为 %[1]sfalse%[1]s 或 %[1]s0%[1]s 可禁用遥测。其优先级高于 %[1]sDO_NOT_TRACK%[1]s。

			%[1]sDO_NOT_TRACK%[1]s: 设置为 %[1]strue%[1]s 或 %[1]s1%[1]s 可禁用遥测。
			当设置了优先级更高的 %[1]sGH_TELEMETRY%[1]s 时会被忽略。

			%[1]sGH_SPINNER_DISABLED%[1]s: 设置为真值可将加载动画(spinner)替换为
			文本形式的进度指示器。
		`, "`"),
	},
	{
		name:  "telemetry",
		short: "关于 gh 中遥测(telemetry)的信息",
		long: heredoc.Doc(`
			gh 收集遥测数据，以帮助我们了解 CLI 的使用情况并加以改进。

			要了解收集了哪些数据、如何使用这些数据以及如何选择退出(opt out)，请参见:
			<https://cli.github.com/telemetry>
		`),
	},
	{
		name:  "reference",
		short: "所有 gh 命令的综合参考",
	},
	{
		name:  "formatting",
		short: "从 gh 导出的 JSON 数据的格式化选项",
		long: heredoc.Docf(`
			默认情况下，%[1]sgh%[1]s 命令的结果以基于行的纯文本格式输出。
			一些命令支持传入 %[1]s--json%[1]s flag，它将输出转换为 JSON 格式。
			转换为 JSON 后，可以通过添加 %[1]s--jq%[1]s 或 %[1]s--template%[1]s flag，
			按照所需的格式化字符串进一步格式化输出。这对选择数据的子集、
			创建新的数据结构、以不同的格式显示数据，或作为另一个命令行脚本的输入非常有用。

			%[1]s--json%[1]s flag 需要一个以逗号分隔的要获取字段列表。要查看某个命令
			可能的 JSON 字段名，可在运行该命令时省略 %[1]s--json%[1]s flag 的字符串参数。
			注意: 若要使用 %[1]s--jq%[1]s 或 %[1]s--template%[1]s flag，必须先传入 %[1]s--json%[1]s flag 与字段名。

			%[1]s--jq%[1]s flag 需要一个 jq 查询语法的字符串参数，并且只会打印匹配该查询的
			JSON 值。jq 查询可用于从数组中选择元素、从对象中选择字段、创建新数组等。
			使用该格式化指令无需在系统上安装 %[1]sjq%[1]s 工具。连接到终端时，
			输出会自动进行美化打印(pretty-print)。要了解 jq 查询语法，请参见:
			<https://jqlang.github.io/jq/manual/>

			%[1]s--template%[1]s flag 需要一个 Go 模板语法的字符串参数，并且只会打印匹配该查询的
			JSON 值。

			除了标准库中的 Go 模板函数之外，该格式化指令还可以使用以下函数:
			- %[1]sautocolor%[1]s: 类似 %[1]scolor%[1]s，但只向终端输出颜色
			- %[1]scolor <style> <input>%[1]s: 使用 <https://github.com/mgutz/ansi> 为输入着色
			- %[1]sjoin <sep> <list>%[1]s: 使用分隔符连接列表中的值
			- %[1]spluck <field> <list>%[1]s: 从输入的所有项中收集某字段的值
			- %[1]stablerow <fields>...%[1]s: 将输出中的字段按表格形式垂直对齐
			- %[1]stablerender%[1]s: 原地渲染由 tablerow 添加的字段
			- %[1]stimeago <time>%[1]s: 将时间戳渲染为相对于现在的时间
			- %[1]stimefmt <format> <time>%[1]s: 使用 Go 的 %[1]sTime.Format%[1]s 函数格式化时间戳
			- %[1]struncate <length> <input>%[1]s: 确保输入不超过指定长度
			- %[1]shyperlink <url> <text>%[1]s: 渲染终端超链接

			以下 Sprig 模板库函数也可以与该格式化指令一起使用:
			- %[1]scontains <arg> <string>%[1]s: 检查 %[1]sstring%[1]s 是否包含 %[1]sarg%[1]s
			- %[1]shasPrefix <prefix> <string>%[1]s: 检查 %[1]sstring%[1]s 是否以 %[1]sprefix%[1]s 开头
			- %[1]shasSuffix <suffix> <string>%[1]s: 检查 %[1]sstring%[1]s 是否以 %[1]ssuffix%[1]s 结尾
			- %[1]sregexMatch <regex> <string>%[1]s: 检查 %[1]sstring%[1]s 是否存在任何匹配 %[1]sregex%[1]s 的结果

			有关 Sprig 库的更多信息，请参见 <https://masterminds.github.io/sprig/>。

			要了解有关 Go 模板的更多信息，请参见: <https://golang.org/pkg/text/template/>。
		`, "`"),
		example: heredoc.Doc(`
			# Default output format
			$ gh pr list
			Showing 23 of 23 open pull requests in cli/cli

			#123  A helpful contribution          contribution-branch              about 1 day ago
			#124  Improve the docs                docs-branch                      about 2 days ago
			#125  An exciting new feature         feature-branch                   about 2 days ago


			# Adding the --json flag with a list of field names
			$ gh pr list --json number,title,author
			[
			  {
			    "author": {
			      "login": "monalisa"
			    },
			    "number": 123,
			    "title": "A helpful contribution"
			  },
			  {
			    "author": {
			      "login": "codercat"
			    },
			    "number": 124,
			    "title": "Improve the docs"
			  },
			  {
			    "author": {
			      "login": "cli-maintainer"
			    },
			    "number": 125,
			    "title": "An exciting new feature"
			  }
			]


			# Adding the --jq flag and selecting fields from the array
			$ gh pr list --json author --jq '.[].author.login'
			monalisa
			codercat
			cli-maintainer


			# --jq can be used to implement more complex filtering and output changes
			$ gh issue list --json number,title,labels --jq \
			  'map(select((.labels | length) > 0))    # must have labels
			  | map(.labels = (.labels | map(.name))) # show only the label names
			  | .[:3]                                 # select the first 3 results'
			  [
			    {
			      "labels": [
			        "enhancement",
			        "needs triage"
			      ],
			      "number": 123,
			      "title": "A helpful contribution"
			    },
			    {
			      "labels": [
			        "help wanted",
			        "docs",
			        "good first issue"
			      ],
			      "number": 125,
			      "title": "Improve the docs"
			    },
			    {
			      "labels": [
			        "enhancement",
			      ],
			      "number": 7221,
			      "title": "An exciting new feature"
			    }
			  ]


			# Using the --template flag with the hyperlink helper
			$ gh issue list --json title,url --template '{{range .}}{{hyperlink .url .title}}{{"\n"}}{{end}}'


			# Adding the --template flag and modifying the display format
			$ gh pr list --json number,title,headRefName,updatedAt --template \
				'{{range .}}{{tablerow (printf "#%v" .number | autocolor "green") .title .headRefName (timeago .updatedAt)}}{{end}}'

			#123  A helpful contribution      contribution-branch       about 1 day ago
			#124  Improve the docs            docs-branch               about 2 days ago
			#125  An exciting new feature     feature-branch            about 2 days ago


			# A more complex example with the --template flag which formats a pull request using multiple tables with headers
			$ gh pr view 3519 --json number,title,body,reviews,assignees --template \
			'{{printf "#%v" .number}} {{.title}}

			{{.body}}

			{{tablerow "ASSIGNEE" "NAME"}}{{range .assignees}}{{tablerow .login .name}}{{end}}{{tablerender}}
			{{tablerow "REVIEWER" "STATE" "COMMENT"}}{{range .reviews}}{{tablerow .author.login .state .body}}{{end}}
			'

			#3519 Add table and helper template functions

			Resolves #3488

			ASSIGNEE  NAME
			mislav    Mislav Marohnić


			REVIEWER  STATE              COMMENT
			mislav    COMMENTED          This is going along great! Thanks for working on this ❤️
		`),
	},
	{
		name:  "exit-codes",
		short: "gh 使用的退出码(exit code)",
		long: heredoc.Doc(`
			gh 遵循关于退出码的常规惯例。

			- 若命令成功完成，退出码为 0

			- 若命令因任何原因失败，退出码为 1

			- 若命令正在运行但被取消，退出码为 2

			- 若命令需要身份验证，退出码为 4

			注意: 某个特定命令可能拥有更多的退出码，因此若您依赖退出码来
			控制某些行为，最好查阅该命令的文档。
		`),
	},
}

func NewCmdHelpTopic(ios *iostreams.IOStreams, ht helpTopic) *cobra.Command {
	cmd := &cobra.Command{
		Use:     ht.name,
		Short:   ht.short,
		Long:    ht.long,
		Example: ht.example,
		Hidden:  true,
		Annotations: map[string]string{
			"markdown:generate": "true",
			"markdown:basename": "gh_help_" + ht.name,
		},
	}

	cmd.SetUsageFunc(func(c *cobra.Command) error {
		return helpTopicUsageFunc(ios.ErrOut, c)
	})

	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		helpTopicHelpFunc(ios.Out, c)
	})

	return cmd
}

func helpTopicHelpFunc(w io.Writer, command *cobra.Command) {
	fmt.Fprint(w, command.Long)
	if command.Example != "" {
		fmt.Fprintf(w, "\n\n示例(EXAMPLES)\n")
		fmt.Fprint(w, text.Indent(command.Example, "  "))
	}
}

func helpTopicUsageFunc(w io.Writer, command *cobra.Command) error {
	fmt.Fprintf(w, "用法(Usage): gh help %s", command.Use)
	return nil
}
