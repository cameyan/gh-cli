# 从源码安装

1. 确认已安装 Go 1.26+

   ```sh
   $ go version
   ```

   如果未安装 `go`，请按照 [Go 官网](https://golang.org/doc/install) 上的说明进行安装。

2. 克隆本仓库

   ```sh
   $ git clone https://github.com/cli/cli.git gh-cli
   $ cd gh-cli
   ```

3. 构建并安装

   **Unix 类系统**

   ```sh
   # 默认安装到 '/usr/local'; 可能需要 sudo, 配置了 go 环境时可用 sudo -E
   $ make install

   # 或者, 安装到其他位置
   $ make install prefix=/path/to/gh
   ```

   **Windows**

   ```pwsh
   # 构建 `bin\gh.exe` 二进制文件
   > go run script\build.go
   ```

   Windows 上没有可用的安装步骤。

4. 运行 `gh version` 检查是否安装成功。

   **Windows**

   运行 `bin\gh version` 检查是否安装成功。

## 为不同平台交叉编译二进制文件

您可以在任何安装了 Go 的平台上构建面向其他平台或 CPU 架构的二进制文件。这是通过设置 GOOS 和 GOARCH 等环境变量来实现的。

例如, 为 32 位 Raspberry Pi OS 编译 `gh` 二进制文件:

```sh
# 在 Unix 类系统上:
$ GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 make clean bin/gh
```

```pwsh
# 在 Windows 上, 将环境变量作为参数传给构建脚本:
> go run script\build.go clean bin\gh GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0
```

运行 `go tool dist list` 可列出 GOOS/GOARCH 的所有支持值。

提示: 要减小生成的二进制文件的大小, 可以使用 `GO_LDFLAGS="-s -w"`。这会省略用于调试的符号表。请参阅 [受支持的链接器标志](https://golang.org/cmd/link/) 列表。
