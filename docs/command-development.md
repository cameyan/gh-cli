# 命令开发

[开发指南](README.md)

关于实现和审查 `gh` 命令的约定，包括命令装配、标志、提示、帮助与输出。

## 结构与生命周期

命令 `gh foo bar` 应位于 `pkg/cmd/foo/bar/`，通常包含 `bar.go`、`bar_test.go`，
以及可选的 `http.go`/`http_test.go`。命令内部逻辑就放在那里；在新增全局 API
之前，先看看命令族中是否已有 `shared` 辅助实现。

对于普通的 Cobra 命令，遵循既有的 Options/Factory/run 函数模式。只包含命令
所需的依赖，而不是一套固定的样板。以下具名示例各有其特定用途：

| 源码符号                                                                      | 模式                                                     |
|---------------------------------------------------------------------------|--------------------------------------------------------|
| [issue/list](../pkg/cmd/issue/list/list.go) 中的 `ListOptions`、`NewCmdList` | Options 持有标志与依赖；构造函数接收 `*cmdutil.Factory` 和可注入的 `runF` |
| [issue/list](../pkg/cmd/issue/list/list.go) 中的 `listRun`                  | run 函数承载业务逻辑；提供 `runF` 时 `RunE` 会分派给它                  |
| [factory/default.go](../pkg/cmd/factory/default.go) 中的 `New`              | 共享依赖装配                                                 |
| [root/root.go](../pkg/cmd/root/root.go) 中的 `NewCmdRoot`                   | 顶层注册                                                   |
| [issue/issue.go](../pkg/cmd/issue/issue.go) 中的 `NewCmdIssue`              | 子命令注册；`cmdutil.AddGroup` 对帮助输出分组                       |

可执行程序从 [main](../cmd/gh/main.go) 启动，委托给
[`ghcmd.Main`](../internal/ghcmd/cmd.go)，并通过 `root.NewCmdRoot` 构建命令树。

将仓库/Git 的发现（`BaseRepo`、`Remotes`、`Branch`）推迟到 `RunE` 或 run 函数
中进行，而不是在构造时进行。特别地，**要在 `RunE` 内部绑定
`opts.BaseRepo = f.BaseRepo`**。
[`EnableRepoOverride`](../pkg/cmdutil/repo_override.go) 会在 pre-run 钩子中替换
`f.BaseRepo`；若捕获了更早的函数，就可能忽略 `-R` 和 `GH_REPO`。更改此装配时，
应通过相关的父级钩子来验证执行，而不只是孤立地调用构造函数。按
[API 与主机](api-and-hosts.md) 中所述使用已解析仓库的主机。

## 标志、帮助与错误

复用 [cmdutil 标志辅助函数](../pkg/cmdutil/flags.go)：`NilStringFlag` 和
`NilBoolFlag` 用于区分未提供值与显式的空值/假值；`StringEnumFlag` 提供枚举
校验与补全。保留可接受的值、默认值、标志名称以及非交互路径。

命令示例使用 `heredoc.Doc`，以 `#` 作为说明性注释行，以 `$ ` 作为命令前缀。
在命令的 Go 源码中编辑帮助（通用主题则编辑
[help_topic.go](../pkg/cmd/root/help_topic.go)），而不要编辑生成的手册页；
[CONTRIBUTING](../.github/CONTRIBUTING.md) 描述了它们在发布时的生成过程。

使用 [cmdutil 错误](../pkg/cmdutil/errors.go)，而不是自行复刻其打印或退出处理：

| 辅助函数/类型                              | 含义                 |
|--------------------------------------|--------------------|
| `FlagErrorf`                         | 无效的标志/参数；显示用法      |
| `MutuallyExclusive`                  | 以标志错误拒绝相互冲突的标志条件   |
| `SilentError`                        | 以退出码 1 退出，不再输出其他消息 |
| `CancelError`                        | 用户取消               |
| `PendingError`                       | 结果待定，并非失败          |
| `NoResultsError`、`NewNoResultsError` | 空结果                |

## 输出与可脚本化

除非已商定的变更明确授权破坏它们，否则应保留面向脚本的契约：标志、参数、默认值、
退出行为、错误消息、JSON 字段、非 TTY 输出以及 stdout/stderr 的路由。同时也要
保留预期的 TTY 行为。

在更改终端交互之前，请查阅 [Primer](primer/README.md) 及其
[可脚本化指南](primer/foundations/README.md#scriptability)。输入/输出、TTY 检测、
颜色与分页处理使用 [IOStreams](../pkg/iostreams/iostreams.go)，表格使用
[tableprinter](../internal/tableprinter)。将数据放在命令既定的 stdout 路径上，
将诊断信息放在其 stderr 路径上；不要合并流，也不要让交互式装饰泄漏到管道中。
非 TTY 表格使用便于脚本处理的输出，而不是终端截断、颜色或表头。提示需要提供
非交互的标志路径。

对于结构化输出，使用 [`cmdutil.AddJSONFlags`](../pkg/cmdutil/json_flags.go) 并
传入命令的可导出字段，以添加 `--json`、`--jq` 和 `--template`。在 run 函数中，
当设置了 exporter 时，在人类可读输出之前返回
`opts.Exporter.Write(opts.IO, data)`。字段选择与 exporter 分派参见上文的
`NewCmdList` 和 `listRun`。JSON 字段名、类型与空结果行为应独立于终端渲染而
予以保留。

在修改这些契约相关的测试之前，请阅读 [测试](testing.md)。
