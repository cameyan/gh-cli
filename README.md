# GitHub CLI

GitHub CLI, or the gh CLI, is GitHub on the command line. It brings pull requests, issues, and other GitHub concepts to the terminal next to where you are already working with `git` and your code.

> [!IMPORTANT]
> GitHub CLI 面向 Linux 软件包仓库的 PGP 签名密钥轮换将于 2026 年 9 月 5 日生效；遇到软件包安装或更新问题的用户可以在 [公开公告](https://github.com/cli/cli/issues/13118) 中查找解决方案。

![gh pr status 截图](https://user-images.githubusercontent.com/98482/84171218-327e7a80-aa40-11ea-8cd1-5177fc2d0e72.png)

GitHub CLI 支持 GitHub.com、GitHub Enterprise Cloud 以及 [受支持的 GitHub 企业版服务器](https://docs.github.com/en/enterprise-server/admin/all-releases) 的用户，支持 macOS、Windows 和 Linux 平台。

## 文档

[安装选项请见下文](#安装)，[使用说明请参阅手册](https://cli.github.com/manual/)。

## Agent 技能

我们提供了一个 [agent 技能](https://agentskills.io)，用于在编码 agent 中驱动 `gh`。使用内置的 `gh skill` 命令安装或更新它:

```shell
# 安装技能(推荐使用 user 作用域)
gh skill install cli/cli gh --scope user

# 在 `gh` 发布新版本后更新技能
gh skill update gh
```

## 参与贡献

如果在使用中感觉有任何不妥之处，或者认为缺少某些功能，请查看 [贡献页面](.github/CONTRIBUTING.md)。在那里你可以找到如何分享反馈、在本地构建工具以及向项目提交拉取请求的说明。

如果你是 hubber 并且有兴趣为 CLI 开发新命令，请查看我们的 [内部贡献文档](docs/working-with-us.md)。

<!-- this anchor is linked to from elsewhere, so avoid renaming it -->
## 安装

### [macOS](docs/install_macos.md)

- [Homebrew](docs/install_macos.md#homebrew)
- [预编译二进制文件](docs/install_macos.md#precompiled-binaries)，见 [发布页面][]

如需了解其他 macOS 软件包和安装程序，请参阅 [社区支持文档](docs/install_macos.md#community-unofficial)

### [Linux 与 Unix](docs/install_linux.md)

- [Debian、Raspberry Pi、Ubuntu](docs/install_linux.md#debian)
- [Amazon Linux、CentOS、Fedora、openSUSE、RHEL、SUSE](docs/install_linux.md#rpm)
- [预编译二进制文件](docs/install_linux.md#precompiled-binaries)，见 [发布页面][]

如需了解其他 Linux 与 Unix 软件包和安装程序，请参阅 [社区支持文档](docs/install_linux.md#community-unofficial)

### [Windows](docs/install_windows.md)

- [WinGet](docs/install_windows.md#winget)
- [预编译二进制文件](docs/install_windows.md#precompiled-binaries)，见 [发布页面][]

如需了解其他 Windows 软件包和安装程序，请参阅 [社区支持文档](docs/install_windows.md#community-unofficial)

### 从源码构建

请参阅 [如何从源码构建 GitHub CLI](docs/install_source.md)。

### GitHub Codespaces

要将 GitHub CLI 添加到你的 codespace，请在你的 [devcontainer 文件](https://docs.github.com/en/codespaces/setting-up-your-project-for-codespaces/adding-features-to-a-devcontainer-file) 中添加以下内容:

```json
"features": {
  "ghcr.io/devcontainers/features/github-cli:1": {}
}
```

### GitHub Actions

[GitHub 托管的运行器](https://docs.github.com/en/actions/using-github-hosted-runners/about-github-hosted-runners) 已预装 GitHub CLI，并且每周更新。

如果需要特定版本，你的 GitHub Actions 工作流需要按照上文 [macOS](#macos)、[Linux 与 Unix](#linux--unix) 或 [Windows](#windows) 的说明进行安装。

有关所有预安装工具的信息，请参阅 [`actions/runner-images`](https://github.com/actions/runner-images)

### 二进制文件的验证

从 v2.93.0 开始，`gh` 的发布版本以不可变发布的形式发布。更多信息请参阅 [不可变发布版本](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)。

自 2.50.0 版本起，`gh` 一直在生成 [构建来源证明](https://github.blog/changelog/2024-06-25-artifact-attestations-is-generally-available/)，提供一条可加密验证的追溯链，回溯到源 GitHub 仓库、git 修订版本以及所使用的构建指令。构建来源证明经过签名，并依赖公益基础设施 [Sigstore](https://www.sigstore.dev/) 提供 PKI 支持。

验证已下载的发布版本有两种常见方式，具体取决于 `gh` 是否已安装。如果 `gh` 已安装，验证新版本非常简单:

- **方式 1: 如果已安装 `gh`，直接使用它:**

  ```shell
  $ gh at verify -R cli/cli gh_2.62.0_macOS_arm64.zip
  Loaded digest sha256:fdb77f31b8a6dd23c3fd858758d692a45f7fc76383e37d475bdcae038df92afc for file://gh_2.62.0_macOS_arm64.zip
  Loaded 1 attestation from GitHub API
  ✓ Verification succeeded!

  sha256:fdb77f31b8a6dd23c3fd858758d692a45f7fc76383e37d475bdcae038df92afc was attested by:
  REPO     PREDICATE_TYPE                  WORKFLOW
  cli/cli  https://slsa.dev/provenance/v1  .github/workflows/deployment.yml@refs/heads/trunk
  ```

- **方式 2: 使用 Sigstore 的 [`cosign`](https://github.com/sigstore/cosign):**

  为此，请下载所获取发布版本的 [证明文件](https://github.com/cli/cli/attestations)，并使用 cosign 验证该发布版本的真实性:

  ```shell
  $ cosign verify-blob-attestation --bundle cli-cli-attestation-3120304.sigstore.json \
        --new-bundle-format \
        --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
        --certificate-identity="https://github.com/cli/cli/.github/workflows/deployment.yml@refs/heads/trunk" \
        gh_2.62.0_macOS_arm64.zip
  Verified OK
  ```

## 与 hub 的比较

多年来，[hub](https://github.com/github/hub) 一直作为非官方的 GitHub CLI 工具。`gh` 是一个新项目，帮助我们探索一款官方 GitHub CLI 工具在采用截然不同的设计时可以呈现的形态。虽然这两款工具都将 GitHub 带到终端，但 `hub` 的行为类似于 `git` 的代理，而 `gh` 是一个独立的工具。欲了解更多信息，请查看我们的 [更详细说明](docs/gh-vs-hub.md)。

[发布页面]: https://github.com/cli/cli/releases/latest
