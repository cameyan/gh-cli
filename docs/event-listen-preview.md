# 事件监听预览

`gh event listen` 和 `gh event subscription` 处于预览阶段。本文描述它们当前的行为，
这些行为可能会发生变化。

## `gh event listen`

```sh
gh event listen --resource https://github.com/OWNER/REPO/pull/123 --event com.github.pull_request.review.submitted.v1,com.github.pull_request.state.changed.v1
gh event listen --resource https://github.com/OWNER/REPO/pull/123 --event com.github.pull_request.review.submitted.v1 --timeout 30m
gh event listen --resource https://github.com/OWNER/REPO/pull/123 --event com.github.pull_request.review.submitted.v1,com.github.pull_request.state.changed.v1 --follow
gh event listen --subscription SUBSCRIPTION_ID
```

`gh event listen` 是一个单一的通用命令，用于等待 GitHub 事件。它会为资源与事件
类型创建订阅，或复用针对同一事件集合的现有订阅，然后接入其事件流，并将每个事件
以每行一个紧凑 CloudEvent JSON 对象的形式打印到 stdout。保活消息不会被打印。

`--event` 接受完整的事件类型名称，例如
`com.github.pull_request.review.submitted.v1`，可以重复指定，也可以用逗号分隔
列出多个类型；没有短别名。gh 不会校验它们：API 会以
`422 Unsupported event types: ...` 拒绝不支持的类型。`--resource` 同样不会被
校验或重写：除拉取请求 URL 之外的任何内容都会被 API 拒绝，包括带有 `/files`
之类后缀的 URL。其主机仅决定请求发送到哪里；若未提供，则使用默认主机。

`--subscription` 通过 ID 接入现有订阅，而不是创建新订阅。它不能与 `--resource`
或 `--event` 组合使用。

## 输出与就绪状态

事件是精简的：`type` 说明发生了什么变化，`data.api_url` 指向发生变化的对象，
例如评审、拉取请求或检查套件。消费者在需要详情时再去获取它。检查套件事件还
携带 `data.conclusion`；一个拉取请求通常有多个检查套件，每个都会发送自己的
事件，因此等待所有检查的消费者会在每个事件之后读取它们。

`--jq` 和 `--template` 像 `gh api` 那样分别格式化每个事件。二者互斥，并在
订阅前完成校验。jq 结果每行打印一个，字符串不加引号。若某个表达式或模板在
某个事件上失败，命令会结束，而不是重新连接。无论 stdout 是否为终端，输出都是
JSON。

每次接入事件流时，都会向 stderr 写入 `Listening for events.`，包括重新连接之后
（若订阅被重新创建，则为 `Listening for events on a new subscription.`），随后
是一条提醒：更早的变更不会被报告。API 仅提供实时数据，因此需要当前状态的调用方
应先启动监听器，等待这一行出现，然后再拍摄快照。若把启动监听器与拍摄快照放在
同一个并行批次中，可能会漏掉某个事件。

## 生命周期

默认情况下，命令在收到第一个事件后以状态 0 退出，然后在 stderr 上提示可以再次
运行以等待下一个事件。`--count N` 在收到 N 个事件后以状态 0 退出。`--follow`
会一直监听，直到命令被中断，且不能与 `--count` 组合使用。`--timeout` 若先到达
超时，则以状态 8 退出。默认在第一个事件后退出适合智能体框架，因为这类框架是在
后台命令退出时恢复智能体，而不是在其打印输出时恢复。

网络错误、`5xx` 响应、未收到结束帧就关闭的流，以及 `reconnect` 结束帧，都会
触发带抖动的指数退避重连，上限为 30 秒。退避只有在某个流送达心跳或事件之后才会
重置，而不仅仅是在接入时重置，因此对于一接入就关闭流的服务器，不会每秒都去重连。
`503` 上的 `Retry-After` 请求头会将等待时间提高到至少该秒数。创建订阅也会对
同样的错误进行重试。若某个流在 45 秒内没有送达任何数据（连心跳都没有），则视为
已死并重连；服务器每 9 秒发送一次心跳。

当订阅过期，或流报告其不存在时，若命令是以 `--resource` 启动的，则会重新创建
订阅；连续第二次未命中即为最终结果。若以 `--subscription` 启动，订阅缺失或过期
即为最终结果。无论哪种模式，订阅被删除都是最终结果，因此删除订阅会停止其监听器。
其他 HTTP 错误（包括 `401` 和 `403`）以及其他 `subscription_ended` 结果都是最终
结果。`401` 会给出通常的 `gh auth login` 提示。

退出时不会删除订阅。订阅按用户、资源和确切的事件集合幂等，因此相互独立的监听器
可以安全地共享它们，并且它们会在 24 小时后过期。每个账户可持有的活动订阅数量
有限；超出限制的创建会返回 `422 Subscription limit exceeded`，命令会将其报告为
可能已达到限制的请求被拒。其他 `422` 响应会列出无效字段，例如 `resource_url`，
并被报告为无效的订阅请求。网关路由失败会返回与拉取请求缺失或不可访问时相同的
`404 Not Found` 响应体，因此二者无法区分。

预览请求使用已配置的 HTTP 客户端。仅支持 github.com；GitHub Enterprise Server
和 GHE.com 主机会在发出任何请求之前失败，并提示 “event subscriptions are only
available on github.com”。凭据对应的账户必须已启用该预览：用户令牌对应的是该
用户，GitHub App 安装令牌对应的是该应用所安装到的用户或组织。启用某个组织会
允许该组织上所安装应用的安装令牌，但不允许其成员的用户令牌。

## 发现提示

当 `internal/agents` 检测到智能体调用方时，以下情况会向 stderr 打印一条提示，
建议改为等待其事件而不是轮询，面向的是需要在继续之前等待拉取请求被评审、合并或
关闭的智能体：成功创建 github.com 拉取请求的 `gh pr create`；成功以非浏览器方式
查看未合并、未关闭的 github.com 拉取请求的 `gh pr view`；以及对 github.com 的
`repos/OWNER/REPO/pulls/NUMBER` 端点或其任意子路径（以路径或绝对
`https://api.github.com` URL 形式给出）成功执行的 `gh api` GET。该提示会将一条
可复制的 `gh event listen` 命令单独放在一行，并带上完整的事件类型名称。它说明该
命令会以一行 JSON 打印第一个事件后退出，并且可以再次运行以等待下一个事件。它
有意不提及 `--follow`：被提供该选项的智能体会选择它，即使只是一次性等待，也会
在后台运行它，然后永远不再被恢复。它会以示例形式建议使用 `--timeout`。它还说明
事件不携带详情，监听器启动之前发生的变更会被遗漏，因此应在监听器运行后再读取
一次拉取请求，并指向 `gh event listen --help` 以了解其他事件类型。`gh api`
不知道拉取请求的状态，因此对于已合并或已关闭的拉取请求也会显示其提示。人类调用、
其他主机、stdout、JSON 输出与退出状态均不变。`gh pr` 和 `gh pr view` 的帮助文本
在 github.com 情况下也会指向 `gh event listen`，并且与 `gh event` 的帮助一样，
说明监听器启动之前发生的变更不会被报告。

对于智能体调用方，若 `gh run watch` 和 `gh run view` 针对的是尚未完成且属于
github.com 拉取请求的工作流运行，则会在 stderr 上打印一条类似提示，面向需要
等待该拉取请求的检查完成的智能体。它建议监听该拉取请求的
`com.github.pull_request.check_suite.status.changed.v1`，并附有相同的说明，只是
额外解释了每个检查套件都会发送带有其结论的独立事件，且应在每个事件之后用
`gh pr checks` 读取检查结果，并在仍有待处理项时再次运行监听器。`gh run watch`
会在开始监视之前打印它，因此即使监视被停止也能看到。`gh run view` 在使用
`--json` 时也会打印它，但在使用 `--web` 或 `--log` 时不打印。`gh run watch` 和
`gh run view` 的帮助文本会指向 `gh event listen`，用于等待 github.com 拉取请求
的工作流运行。

## 评估用法

随附的 `gh` 技能位于 `skills/gh`，其中记录了 `gh event listen`。可以从工作区
对其进行评估，或与主干上的版本进行比较——在本次变更合并之前，主干版本没有事件
相关指引：

```sh
# The skill on this branch
evals/run --live --gh ./bin/gh --gh-skill ./skills/gh --model MODEL_ID --skill on notify-review

# The skill on trunk
evals/run --live --gh ./bin/gh --gh-skill-ref trunk:skills/gh --model MODEL_ID --skill on notify-review
```

路由评分器会接受针对每个脚本化变更的 `gh event listen` 输出，包括来自在送达
事件后即被停止的监听器的输出。经 `--jq` 或 `--template` 重塑的输出无法与特定
事件匹配，因此只有当监听器以 `--resource` 绑定到测试夹具拉取请求，且 `--event`
集合覆盖该变更时，才会被计入。在当前评估契约下，直接的 `gh api` 流仍是一条独立
的、不通过的路径。
