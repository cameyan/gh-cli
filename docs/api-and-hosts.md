# API 与主机

[开发指南](README.md)

关于修改和审查 HTTP 请求、主机选择、认证以及 GitHub 企业版服务器(GHES)能力检查的约定。

## 选择操作实际指向的主机

| 操作           | 主机来源                                     |
|--------------|------------------------------------------|
| 仓库范围内的操作     | 通过命令既有的仓库选择路径解析仓库，然后使用 `repo.RepoHost()` |
| 显式的主机标志或 URL | 遵循命令既有的校验与优先级规则；不要用默认值替换它                |
| 需要已配置默认值的操作  | 使用 `cfg.Authentication().DefaultHost()`  |

`ghinstance.Default()` 始终返回 `github.com`；它不是用户选择的主机解析。
也不要机械地将其替换为 `DefaultHost()`：首先要确定操作是否已有显式主机或
仓库主机。默认值不得覆盖通过 `-R` 或 `GH_REPO` 选择的仓库。

关于已解析仓库的主机用法，参见 [issue/list](../pkg/cmd/issue/list/list.go) 中的
`listRun`、`issueList` 和 `milestoneByNumber`；

关于已配置的默认行为，参见 [config.go](../internal/config/config.go) 中的
`AuthConfig.DefaultHost`。

保留 [命令开发](command-development.md#structure-and-lifecycle) 中描述的延迟 Factory 绑定。

## 复用客户端、认证与请求处理

从命令既有的依赖中获取其已配置的 HTTP 客户端。

[`factory.New` 与 `HttpClientFunc`](../pkg/cmd/factory/default.go) 负责装配配置、认证、I/O
以及传输行为。对于常规的 GitHub API 操作，不要替换为裸客户端、复制令牌查找逻辑，或手工构造 API 端点。

[`api.NewClientFromHTTP`](../api/client.go) 封装该客户端。

复用其 `GraphQL`/`Query`/`Mutate` 与 `REST` 方法，或复用与响应相匹配的既有请求接口。传入所选主机和
相对 API 路径。通过共享抽象保留端点特定的请求头、作用域、重定向策略、错误处理与分页。避免多余的往返请求，
只请求命令所需的字段，包括所请求的 JSON 导出字段。

在更改 API 路由或构造客户端/请求时，还应阅读 [`api_host`](api-host.md)；在验证路由变更时，查阅其
[测试框架](api-host-test-harness.md)。逻辑上的 GitHub 主机仍然负责认证、Git 远程仓库以及 Web URL；
API 网关并不是仓库主机的替代品。不要假设每个命令或服务都已迁移，或都应当使用该网关。

## 功能检测与清理

复用 [`featuredetection.Detector`](../internal/featuredetection/feature_detection.go)
以及既有的能力定义，而不是分散的版本判断。使用既有的客户端/缓存模式为目标主机构造检测器。传播检测错误，而
不是将其视为不支持。

当某个 API 尚未在所有受支持的 GitHub API 服务器(包括 GHES)上普遍可用时，使用临时功能检测。对于在所有
受支持环境中早已可用的成熟 API，跳过新增门控。查阅当前的能力文档与支持要求；不要仅凭 GitHub.com 的行为
推断其就绪状态。

对于最终会消失的临时门控，将 `// TODO <cleanupIdentifier>` 直接放在 `if` 语句上方。在相关调用点和
能力定义中使用相同的标识符，以便一致地移除回退逻辑。检测器中的 `IssueFeatures.ApiActorsSupported`
就是一个带有文档化清理条件的能力示例。这是仓库约定，并非对某个特定 linter 的声明。

对于 GHES 上不会支持的永久性门控，**不需要**清理注释。不要因为某个主机支持就移除门控；应验证其清理条件在
当前受支持服务器范围内是否成立。

在修改测试之前，请阅读 [测试](testing.md)。覆盖相关的已选择主机路径与默认主机路径(包括非 `github.com`
的主机)，以及支持与不支持能力的行为。断言实际目标，而不仅仅是断言某个模拟响应被消费。