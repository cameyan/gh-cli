# `api_host` 测试框架

一个黑盒测试，用于验证将 `gh` 的 API 流量经由企业网关路由，正如
[cli/cli#13717](https://github.com/cli/cli/issues/13717) 中所提议的那样。

该测试框架位于 `script/api-host-gateway/`。

## 存在的意义

只有当每个请求都经过同一个咽喉点时，`api_host` 才能在中心位置统一落实。一个
构造绝对 `https://api.github.com/...` URL 并调用 `httpClient.Do` 的调用点会
绕过任何中心化的解析，而现有测试都察觉不到，因为 `api.github.com` 从 CI 中
可以访问。如果没有某种能让绕过行为大声失败的东西，这次迁移就是不可证伪的。

这个测试框架就是那个“某种东西”。它让真实的 `gh` 二进制针对一个记录式的 TLS
反向代理运行，该代理转发到真实的 `api.github.com`，同时把 `api.github.com`
黑洞化，使 `gh` 除了经由网关之外无法到达 GitHub。随后它对该主张的两半都进行
断言：网关看到了请求，并且 `gh` 拿到了真实的响应。

由此产生的后果正是关键所在。遵循 `api_host` 的请求会到达网关并被记录；忽略它
的请求则以 `dial tcp 127.0.0.1:443: connection refused` 失败。不存在悄无声息
的通过。

## 运行方式

```console
$ script/api-host-gateway/run.sh
```

需要 Docker 和一个令牌：若已设置则用 `$GH_TOKEN`，否则用
`gh auth token --hostname github.com`。测试所断言的登录名由该令牌推导而来，
也可以通过 `GH_APIHOST_EXPECTED_LOGIN` 显式设置以跳过查找。

### 它测试的是你的工作区，而不是 HEAD

容器会从包含 `run.sh` 的工作区构建 `gh`。如果该工作区是脏的，结果既不能描述
`HEAD`，也不能描述任何可命名的事物，而对于一个全部职责就是告诉你哪些调用点
有问题的测试来说，这毫无价值。因此 `run.sh` 在脏工作区上拒绝启动，并打印它
即将测试的版本：

```console
$ script/api-host-gateway/run.sh
Testing gh at 5fd4e7f27
```

若想在不打扰当前工作的情况下测试某个版本，可从另一个检出中运行：

```console
$ git worktree add /tmp/gh-harness <revision>
$ /tmp/gh-harness/script/api-host-gateway/run.sh
```

`GH_APIHOST_ALLOW_DIRTY=yes` 会照常运行，并将版本标记为 `-dirty`。这在迭代
修复时很有用，但 `-dirty` 的运行结果绝不应被引用为某个调用点已修复的证据。

### 使用验收子集

设置 `GH_APIHOST_ACCEPTANCE=yes`，并将 `GH_APIHOST_ORG` 设为一个该令牌可在其中
创建仓库的 GitHub 组织：

```console
$ GH_APIHOST_ACCEPTANCE=yes GH_APIHOST_ORG=my-org script/api-host-gateway/run.sh
```

这会创建真实仓库，耗时几分钟。它会提示输入一个由该组织拥有的细粒度 PAT。该
PAT 必须作用于组织内的所有仓库，因为脚本会创建名称随机的仓库，无法事先列出。
设置 `GH_APIHOST_ORG_TOKEN` 可跳过提示。

阶段 4 和阶段 5 有意设为非阻塞：红色结果会被打印，但不会停止脚本，也不影响
退出码——退出码仅由阶段 1–3 决定。这两个阶段的存在是为了产出一种在一系列变更
中由红转绿的统计记录，而不是为了门禁构建。

## 为什么用容器

有两个约束使得直接在开发机上运行颇为别扭，而在 Linux 容器内则轻而易举：

- `api_host` 是裸主机名，因此不能携带端口。网关必须监听 443，而这需要 root。
- 网关的证书必须被 `gh` 信任。Go 在 Linux 上遵循 `SSL_CERT_FILE`，但在 macOS
  上不遵循（那里使用平台验证器），因此在 macOS 上唯一的替代方案是向钥匙串
  安装 CA。

容器还为我们提供了可写的 `/etc/hosts`，`api.github.com` 正是借此被黑洞化的。

## 它做了什么

`run.sh` 启动 `golang:1.26`，将仓库挂载到 `/src`，并在其中运行 `test.sh`。
`test.sh` 会：

1. 构建 `gh` 和网关。
2. 将 `api.github.com` 解析为一个 IP，并在 `127.0.0.2:443` 上启动网关，将其
   固定到该 IP，使其在黑洞生效后仍能工作。网关为
   `gh-gateway.internal` 生成自己的 CA 与叶子证书。
3. 通过 `SSL_CERT_FILE` 信任该 CA，并在 `/etc/hosts` 中将
   `gh-gateway.internal` 指向 `127.0.0.2`。
4. 写入一个隔离的 `GH_CONFIG_DIR`，其 `hosts.yml` 中 `github.com` 带有
   `user`、`oauth_token` 以及 `api_host: gh-gateway.internal`。
5. 运行下面的各个阶段，并按名称记录每一条断言。

### 阶段

**阶段 1，已路由。** 设置 `api_host` 并黑洞化 `api.github.com`。`gh api user`、
`gh api repos/cli/cli`、`gh api graphql`、`gh repo view` 和 `gh api --paginate`
中的每一个都必须返回真实的 GitHub 数据，并且网关必须记录到对应的请求，带有
`Host: gh-gateway.internal` 和 `Authorization` 请求头。

分页用例还额外证明网关的 `Link` 请求头重写有效，因为只有当 `gh` 被送回网关
而不是 `api.github.com` 时才能取到第二页，并且证明后续请求仍然携带令牌。最后
这条断言很容易失败：`gh` 按请求主机附加令牌，而网关主机自身没有令牌，因此
朴素的实现会以匿名方式分页，只在针对公开资源时才显得可用。

**阶段 2，对照。** 不设 `api_host`，也不设黑洞。同样的命令仍须正常工作，且
网关不得记录任何内容，从而证明正是该覆盖设置导致了路由。

**阶段 3，黑洞合理性检查。** 不设 `api_host`，重新开启黑洞。`gh api user` 必须
失败。若没有这一阶段，阶段 1 可能是在走一条黑洞本应阻止却悄然未能阻止的直接
连接。

阶段 2 和阶段 3 是对照组。即使阶段 1 完全红色，它们也应通过；而一旦它们失败，
说明测试框架本身坏了，而不是产品坏了。

**阶段 4 和阶段 5，验收子集。** 在 `GH_APIHOST_ACCEPTANCE=yes` 时运行。设置
`api_host` 并黑洞化 `api.github.com`，与阶段 1 一致。真实验收套件中选定的
子集会经由网关运行，每次 `go test` 调用运行一个脚本。

脚本逐个运行，而不是按测试函数批量运行，因为一个测试函数会捆绑因互不相关原因
而失败的脚本。批量运行会掩盖单个脚本转绿的事实。

这两个阶段之所以拆分，是因为细粒度 PAT 恰好只有一个资源所有者，而 Gists 属于
**账户**权限，其余则需要**组织**权限。没有任何单个 PAT 能同时覆盖两者。阶段 4
在由测试组织拥有的 PAT 下运行组织范围的脚本；阶段 5 在开发者自己的 OAuth 令牌
下运行 gist 脚本。

`run_subset` 区分三种结果而非两种：通过、失败，以及**未匹配到任何测试**。第三
种很重要，因为在被测版本上不存在的脚本否则会显得是绿的。它通过搜索
`[no tests to run]` 来检测。

### 解读失败

两种失败特征意味着相反的事情，区分它们是任何调试会话的第一步：

| 特征                                                                         | 含义                                                              |
|----------------------------------------------------------------------------|-----------------------------------------------------------------|
| 针对 `api.github.com` 的 `dial tcp 127.0.0.1:443: connection refused`         | 被测代码忽略了 `api_host`，去了规范主机。真实的产品故障。                              |
| 针对 `gh-gateway.internal` 的 `x509: certificate signed by unknown authority` | 请求**到达了**网关，但调用方不信任测试框架的 CA。测试框架缺陷，通常是 `SSL_CERT_FILE` 未传播到子进程。 |

注意，`gh auth status` 会把任何传输失败都报告为 “The token in hosts.yml is
invalid”。调试时不要照字面理解这条消息。

有两个偶发失败是既有的，与 `api_host` 无关：`repo-archive-unarchive`
（服务端问题），以及 `search-issues`，它依赖于问题创建后搜索索引的追赶。

## 网关

`gateway/main.go` 是一个无依赖的单一程序。除了记录请求之外，它会缓冲每个响应，
并将每一处 `api.github.com` 重写为 `gh-gateway.internal`，包括 `Link` 等请求头
以及 JSON 正文中的出现。这反映了实践中的处理方式：GitHub 会返回规范主机上的
绝对 URL，因此不重写它们的网关会把客户端直接送回其路由之外。它向上游请求
identity 内容编码以便正文可重写，并在之后修正 `Content-Length`。

内容主机被有意保留为可达。`api_host` 代理的是 API，而不是每个 GitHub 端点：
gist 文件正文来自 `gist.githubusercontent.com`，`codeload` 和发布二进制存储
也是如此。测试框架只黑洞化 `api.github.com`，这正确地模拟了客户的网络。若把
内容主机也黑洞化，会产生任何用户都不会遇到的失败。

## 单独调试网关

如果给它一个非特权端口，网关就不需要 root 或容器，这使得用 `curl` 试探它变得
很容易：

```console
$ go build -o /tmp/gateway ./script/api-host-gateway/gateway
$ /tmp/gateway -listen 127.0.0.1:8443 \
    -upstream-addr "$(dig +short api.github.com | head -1):443" \
    -ca-out /tmp/ca.pem -log /tmp/gateway.jsonl &
$ curl --cacert /tmp/ca.pem --resolve gh-gateway.internal:8443:127.0.0.1 \
    -H "Authorization: token $(gh auth token)" \
    https://gh-gateway.internal:8443/user
```

`gh` 本身无法指向它，因为 `api_host` 不能携带端口。

## 当前状态

除了一处与路由无关的失败之外，每个阶段都通过，每个脚本都是绿的：

```
HTTP 422: Validation Failed (https://gh-gateway.internal/repos/gh-acceptance-testing/repo_list_rename-LFxrOIbkOT)
name A conflicting repository operation is still in progress
```

`repo-list-rename` 会创建一个仓库并立即重命名，而 GitHub 有时尚未完成创建。
请求到达了 `gh-gateway.internal`，并带着 GitHub 经过考虑的答复返回，这正是
测试框架在报告它自身职责上的成功：路由生效了，而服务器出于自身原因拒绝了。
它在主干上也会间歇性失败。

## 遗留事项

没有测试框架能看见的东西。`gh` 把这十二个脚本中的每个请求都发送到主机的
`api_host`，并且在 `api.github.com` 不可达时，没有把其中任何一个发往别处。

这是一个关于十二个脚本的主张，而不是关于 `gh` 的。测试框架所证明的是，共享
客户端现在能够表达调用点所需的信息，因此迁移其余部分是机械性的工作，而非受
阻。`docs/api-host.md` 记录了仍未路由的部分。

## 运行记录

来自本提交的一次 `GH_APIHOST_ACCEPTANCE=yes` 运行。每个改变路由的提交都会用
自己的运行结果替换本节，因此对本文件执行 `git log -p` 可以看到统计由红转绿。

```
== Results
PHASE  RESULT   NAME
1      PASS     gh api user returns the authenticated login
1      PASS     gh api repos/cli/cli returns real repository data
1      PASS     gh api graphql returns the authenticated login
1      PASS     gh repo view returns real repository data
1      PASS     gh api --paginate followed the rewritten Link header (82 labels)
1      PASS     gateway recorded the authenticated REST request for /user
1      PASS     gateway recorded the authenticated REST request for the repository
1      PASS     gateway recorded the authenticated GraphQL requests
1      PASS     gateway recorded the second page of labels
1      PASS     second page request carried the token
2      PASS     gh api user still works without an override
2      PASS     gh api repos/cli/cli still works without an override
2      PASS     gh api graphql still works without an override
2      PASS     gh repo view still works without an override
2      PASS     gateway saw no traffic without an override
3      PASS     gh cannot reach GitHub directly while blackholed
4      PASS     basic-rest.txtar
4      PASS     basic-graphql.txtar
4      PASS     release-upload-download.txtar
4      PASS     repo-delete.txtar
4      FAIL     repo-list-rename.txtar
4      PASS     repo-read-file.txtar
4      PASS     repo-rename-transfer-ownership.txtar
4      PASS     run-download.txtar
4      PASS     extension.txtar
4      PASS     search-issues.txtar
4      PASS     auth-status.txtar
5      PASS     gist-create-view-delete.txtar

== Summary
1 subset script(s) red: repo-list-rename.txtar
```
