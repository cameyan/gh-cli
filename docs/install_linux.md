# 在 Linux 和 BSD 上安装 gh

## 推荐 _(官方)_

> [!IMPORTANT]
> 我们的 Linux 软件包和仓库元数据使用以下 PGP 密钥指纹签名:
> - `2C6106201985B60E6C7AC87323F3D4EA75716059`
> - `7F38BBB59D064DBCB3D84D725612B36462313325`
>
> 在安装过程中，系统可能会提示您确认导入这些密钥。
>
> <details><summary>展开查看我们官方密钥环文件的 SHA256/SHA512/MD5 校验和。</summary>
> <p>
>
>  **出于安全考虑，强烈建议仅依赖 SHA256/SHA512 校验和。以下 MD5 校验和仅供无法使用 SHA256/SHA512 工具的旧系统使用。**
>
> - `https://cli.github.com/packages/githubcli-archive-keyring.gpg` (二进制):
>    ```
>    SHA256: 6084d5d7bd8e288441e0e94fc6275570895da18e6751f70f057485dc2d1a811b
>    SHA512: ce6b9466dbd2a90b3227e177aa9b8187bd2405b1c29f91d78de83b9699dbbe2af35efd733bf53da622e7a38c59a7bc55539d63a3deae3c9ff9c2bff8af626434
>    MD5:    23748c0965069fb1edae1b83c17890e1
>    ```
> - `https://cli.github.com/packages/githubcli-archive-keyring.asc` (ASCII 装甲格式):
>    ```
>    SHA256: cec6e9ed82d3949ca5f4428cc968b41ef5e7416cb3653cdfc2a421977663bbfd
>    SHA512: 2ca9487d88a508a1c87f06b46ba336b11cc5f20bd83915b4c2acde49d2cffbbce76af1641bf8494c29a765f96bc1fd694ebde2954b28b80dcc76376b6f1b766d
>    MD5:    97100400ef48007b69e42be348cc6582
>    ```
>
> </p>
> </details>

### Debian

Debian 软件包托管在 [GitHub CLI 营销站点](https://cli.github.com/) 上，适用于各种操作系统，包括:

- [Debian](https://www.debian.org/)
- [Raspberry Pi](https://www.raspberrypi.com/)
- [Ubuntu Linux](https://ubuntu.com/)

这些软件包由 GitHub CLI 维护者提供支持，更新由 [GitHub CLI 部署工作流](https://github.com/cli/cli/actions/workflows/deployment.yml) 驱动。

安装:

```bash
(type -p wget >/dev/null || (sudo apt update && sudo apt install wget -y)) \
	&& sudo mkdir -p -m 755 /etc/apt/keyrings \
	&& out=$(mktemp) && wget -nv -O$out https://cli.github.com/packages/githubcli-archive-keyring.gpg \
	&& cat $out | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
	&& sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
	&& sudo mkdir -p -m 755 /etc/apt/sources.list.d \
	&& echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
	&& sudo apt update \
	&& sudo apt install gh -y
```

升级:

```bash
sudo apt update
sudo apt install gh
```

> [!TIP]
> 要在安装 `gh` 之前验证 PGP 密钥，您可以运行以下命令，并将列出的指纹与本文档顶部的指纹进行比对:
>
> ```shell
> curl -fsSL -o - https://cli.github.com/packages/githubcli-archive-keyring.gpg | gpg --show-keys
> ```

### RPM

RPM 软件包托管在 [GitHub CLI 营销站点](https://cli.github.com) 上，适用于各种操作系统，包括:

- [Amazon Linux 2](https://aws.amazon.com/amazon-linux-2/)
- [CentOS](https://www.centos.org/)
- [Fedora](https://fedoraproject.org/)
- [Red Hat Enterprise Linux](https://www.redhat.com/en/technologies/linux-platforms/enterprise-linux)
- [openSUSE](https://www.opensuse.org/)
- [SUSE](https://www.suse.com/)

这些软件包由 GitHub CLI 维护者提供支持，更新由 [GitHub CLI 部署工作流](https://github.com/cli/cli/actions/workflows/deployment.yml) 驱动。

> [!TIP]
> 在安装过程中，系统可能会提示您确认导入 PGP 密钥。您可以使用本文档顶部的指纹列表来验证这些密钥。
>
> 要在安装 `gh` 之前验证 PGP 密钥，您可以运行以下命令，并将列出的指纹与本文档顶部的指纹进行比对:
>
> ```shell
> curl -fsSL -o - https://cli.github.com/packages/githubcli-archive-keyring.asc | gpg --show-keys
> ```

#### DNF5

> [!IMPORTANT]
> **以下命令仅适用于 DNF5**。如果您使用的是 DNF4，请使用 [DNF4 说明](#dnf4)。

安装:

```bash
sudo dnf install dnf5-plugins
sudo dnf config-manager addrepo --from-repofile=https://cli.github.com/packages/rpm/gh-cli.repo
sudo dnf install gh
```

升级:

```bash
sudo dnf update gh
```

#### DNF4

> [!IMPORTANT]
> **以下命令仅适用于 DNF4**。如果您使用的是 DNF5，请使用 [DNF5 说明](#dnf5)。

安装:

```bash
sudo dnf install 'dnf-command(config-manager)'
sudo dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
sudo dnf install gh
```

升级:

```bash
sudo dnf update gh
```

#### Amazon Linux 2 (yum)

安装:

```bash
type -p yum-config-manager >/dev/null || sudo yum install yum-utils
sudo yum-config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
sudo yum install gh
```

升级:

```bash
sudo yum update gh
```

#### openSUSE/SUSE Linux (zypper)

安装:

```bash
sudo zypper addrepo https://cli.github.com/packages/rpm/gh-cli.repo
sudo zypper ref
sudo zypper install gh
```

升级:

```bash
sudo zypper ref
sudo zypper update gh
```

### Homebrew

[Homebrew](https://brew.sh/) 是一个免费的开源软件包管理系统，可简化 Apple 操作系统 macOS 以及 Linux 上的软件安装。

[GitHub CLI 公式](https://formulae.brew.sh/formula/gh) 由 GitHub CLI 维护者提供支持，并得到 Homebrew 朋友们的帮助，更新由 [homebrew/homebrew-core](https://github.com/Homebrew/homebrew-core/blob/main/Formula/g/gh.rb) 驱动。

安装:

```shell
brew install gh
```

升级:

```shell
brew upgrade gh
```

### 预编译二进制文件

[GitHub CLI 发布版本](https://github.com/cli/cli/releases/latest) 包含适用于 `386`、`amd64`、`arm64` 和 `armv6` 架构的预编译二进制文件。

## 社区 _(非官方)_

> [!IMPORTANT]
> GitHub CLI 团队不维护以下软件包或仓库。我们无法为这些安装方法提供支持，也无法对这些安装方法的稳定性、安全性或可用性提供任何保证。

### Alpine Linux

[GitHub CLI 软件包](https://pkgs.alpinelinux.org/package/edge/community/x86_64/github-cli) 由 Alpine Linux 社区提供支持，更新由 [alpine/aports](https://gitlab.alpinelinux.org/alpine/aports/-/tree/master/community/github-cli) 驱动。

安装稳定版:

```bash
apk add github-cli
```

安装 edge 版本:

```bash
echo "@community http://dl-cdn.alpinelinux.org/alpine/edge/community" >> /etc/apk/repositories
apk add github-cli@community
```

### Android

[GitHub CLI 软件包](https://packages.termux.dev/apt/termux-main/pool/main/g/gh/) 由 Termux 社区提供支持，更新由 [termux/termux-packages](https://github.com/termux/termux-packages/tree/master/packages/gh) 驱动。

安装和升级:

```bash
pkg install gh
```

### Arch Linux

[GitHub CLI 软件包](https://www.archlinux.org/packages/extra/x86_64/github-cli) 由 Arch Linux 社区提供支持，更新由 [Arch Linux 打包](https://gitlab.archlinux.org/archlinux/packaging/packages/github-cli) 驱动。

安装:

```bash
sudo pacman -S github-cli
```

升级所有软件包:

```bash
sudo pacman -Syu
```

或者，使用 [非官方 AUR 软件包](https://aur.archlinux.org/packages/github-cli-git) 从源码构建 GitHub CLI。

### Conda

[Conda](https://docs.conda.io/en/latest/) 是一个开源软件包管理系统和环境管理系统，用于安装多个版本的软件包及其依赖项，并可在它们之间轻松切换。它适用于 Linux、OS X 和 Windows，最初是为 Python 程序而创建，但可以打包和分发任何软件。

[GitHub CLI 软件包](https://anaconda.org/conda-forge/gh) 由 Conda 社区提供支持，更新由 [conda-forge/gh-feedstock](https://github.com/conda-forge/gh-feedstock#installing-gh) 驱动。

安装:

```shell
conda install gh --channel conda-forge
```

升级:

```shell
conda update gh --channel conda-forge
```

### Debian (extrepo)

The [extrepo tool](https://packages.debian.org/stable/extrepo) and [GitHub CLI repository metadata](https://salsa.debian.org/extrepo-team/extrepo-data/-/blob/master/repos/debian/github-cli.yaml) are supported by the Debian community. The packages installed from the configured repository are the [official GitHub CLI packages](#debian).

To install:

```shell
sudo apt install extrepo
sudo extrepo enable github-cli
sudo apt update
sudo apt install gh
```

> [!WARNING]
> The keyring downloaded by `extrepo` may differ from our published keyring, so the checksums listed at the top of this document may not apply.

### Debian 社区

[GitHub CLI 软件包](https://packages.debian.org/stable/gh) 由 Debian 社区提供支持，更新由 [Debian Go 打包团队](https://salsa.debian.org/go-team/packages/gh) 驱动。

> [!NOTE]
> 截至 2025 年 11 月，GitHub CLI 维护者强烈建议使用 [官方 Debian 软件包](#debian)，尤其是因为社区分发的 `2.45.x` / `2.46.x` 版本由于 GitHub API 弃用而无法正常工作。

### Fedora 社区

[GitHub CLI 软件包](https://packages.fedoraproject.org/pkgs/gh/gh/) 由 Fedora 社区提供支持，更新由 [Fedora 项目](https://src.fedoraproject.org/rpms/gh) 驱动。

安装:

```bash
sudo dnf install gh
```

升级:

```bash
sudo dnf update gh
```

### Flox

[Flox](https://flox.dev/) 是一个集虚拟环境与软件包管理器于一体的工具。借助 Flox，您可以在关键位置创建分层并替换依赖项的环境，使其在整个软件生命周期中可移植。

Flox 依赖由 [NixOS 社区](https://nixos.org/) 支持的 [GitHub CLI 软件包](https://github.com/NixOS/nixpkgs/blob/master/pkgs/by-name/gh/gh/package.nix)

安装:

```shell
flox install gh
```

升级:

```shell
flox upgrade toplevel
```

### FreeBSD

[GitHub CLI 移植版](https://www.freshports.org/devel/gh/) 由 FreeBSD 社区提供支持，更新由 [FreeBSD ports](https://cgit.freebsd.org/ports/tree/devel/gh) 驱动。

```bash
cd /usr/ports/devel/gh/ && make install clean
```

或通过 [pkg(8)](https://www.freebsd.org/cgi/man.cgi?pkg(8)):

```bash
pkg install gh
```

### Funtoo

GitHub CLI 的 portage 由 Funtoo 社区提供支持，更新由 [funtoo/dev-kit](https://github.com/funtoo/dev-kit/tree/1.4-release/dev-util/github-cli) 驱动。

安装:

```bash
emerge -av github-cli
```

升级:

```bash
ego sync
emerge -u github-cli
```

### Gentoo

[GitHub CLI portage](https://packages.gentoo.org/packages/dev-util/github-cli) 由 Gentoo 社区提供支持，更新由 [Gentoo portage](https://gitweb.gentoo.org/repo/gentoo.git/tree/dev-util/github-cli) 驱动。

安装:

``` bash
emerge -av github-cli
```

升级:

``` bash
emerge --sync
emerge -u github-cli
```

### Manjaro Linux

[GitHub CLI 软件包](https://manjaristas.org/branch_compare?q=github-cli) 与 [Arch Linux 社区](#arch-linux) 制作的软件包相同

安装和升级:

```bash
pamac install github-cli
```

### MidnightBSD

[GitHub CLI 移植版](https://www.midnightbsd.org/mports/devel/gh/README.html) 由 MidnightBSD 社区提供支持，更新由 [MidnightBSD/mports](https://github.com/MidnightBSD/mports/tree/master/devel/gh) 驱动。

安装:

```bash
cd /usr/mports/devel/gh/ && make install clean
```

或通过 [mport(1)](http://man.midnightbsd.org/cgi-bin/man.cgi/mport):

```bash
mport install gh
```

### NetBSD/pkgsrc

[GitHub CLI 软件包](https://pkgsrc.se/net/gh) 由 NetBSD 社区提供支持，更新由 [NetBSD/pkgsrc](https://github.com/NetBSD/pkgsrc/tree/trunk/net/gh) 驱动。

安装:

```bash
pkgin install gh
```

### Nix/NixOS

[GitHub CLI 软件包](https://search.nixos.org/packages?query=gh&sort=relevance&show=gh) 由 NixOS 社区提供支持，更新由 [NixOS/nixpkgs](https://github.com/NixOS/nixpkgs/tree/master/pkgs/by-name/gh/gh) 驱动。

For temporary use:

```bash
nix-shell -p gh
```

For permanent installation, see the [NixOS package instructions](https://search.nixos.org/packages?query=gh&sort=relevance&show=gh) to declare `pkgs.gh` in your NixOS configuration or Home Manager.

### OpenBSD

[GitHub CLI 移植版](https://openports.pl/path/devel/github-cli) 由 OpenBSD 社区提供支持，更新由 [OpenBSD ports](https://cvsweb.openbsd.org/ports/devel/github-cli/) 驱动。

安装:

```shell
pkg_add github-cli
```

### openSUSE Tumbleweed

[GitHub CLI 软件包](https://software.opensuse.org/package/gh) 由 openSUSE 社区提供支持。

安装:

```bash
sudo zypper install gh
```

升级:

```bash
sudo zypper update gh
```

### Solus Linux

GitHub CLI 软件包由 Solus Linux 社区提供支持，更新由 [getsolus/packages](https://github.com/getsolus/packages/blob/main/packages/g/github-cli/) 驱动。

安装:

```bash
sudo eopkg install github-cli
```

### Spack

[Spack](https://spack.io/) 是一个灵活的软件包管理器，支持适用于超级计算机、Linux 和 macOS 的多种版本、配置、平台和编译器。

[GitHub CLI 软件包](https://packages.spack.io/package.html?name=gh) 由 Spack 社区提供支持，更新由 [spack/spack-packages](https://github.com/spack/spack-packages/tree/develop/repos/spack_repo/builtin/packages/gh) 驱动。

安装:

```shell
spack install gh
```

升级:

```shell
spack uninstall gh && spack install gh
```

### Ubuntu 社区

[GitHub CLI 软件包](https://packages.ubuntu.com/noble/gh) 从 [上游 Debian 社区软件包](#debian-community) 同步而来。

> [!NOTE]
> 截至 2025 年 11 月，GitHub CLI 维护者强烈建议使用 [官方 Debian 软件包](#debian)，尤其是因为社区分发的 `2.45.x` / `2.46.x` 版本由于 GitHub API 弃用而无法正常工作。

### Void Linux

[GitHub CLI 软件包](https://voidlinux.org/packages/?arch=x86_64&q=github-cli): 由 Void Linux 社区提供支持，更新由 [void-linux/void-packages](https://github.com/void-linux/void-packages/tree/master/srcpkgs/github-cli) 驱动。

安装:

```bash
sudo xbps-install github-cli
```

### Webi

[Webi](https://webinstall.dev/) 是一款旨在通过易于记忆的 URL 快速、轻松地安装开发者工具的工具，无需 sudo 或管理员权限，无需软件包管理器，也无需更改系统文件权限。

[GitHub CLI 软件包](https://webinstall.dev/gh/) 由 Webi 社区提供支持，更新由 [webinstall/webi-installers](https://github.com/webinstall/webi-installers/tree/main/gh) 驱动。

安装:

```shell
curl -sS https://webi.sh/gh | sh
```

升级:

```shell
webi gh@stable
```

## 不推荐

> [!WARNING]
> GitHub CLI 团队明确不鼓励使用以下安装方法。

### Snap

[GitHub CLI 软件包](https://snapcraft.io/gh) 作为像 GitHub CLI 这样的应用的运行时机制，存在 [诸多与 Snap 相关的问题](https://github.com/casperdcl/cli/issues/7)，因此我们的团队建议 _永远不要将 gh 作为 snap 安装_。
