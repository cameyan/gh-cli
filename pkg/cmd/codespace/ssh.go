package codespace

// 本文件定义 'gh cs ssh' 与 'gh cs cp' 子命令。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/codespaces/portforwarder"
	"github.com/cli/cli/v2/internal/codespaces/rpc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/ssh"
	"github.com/cli/safeexec"
	"github.com/spf13/cobra"
)

// 在 2.13.0 版本中，这些命令开始自动生成名为 'codespaces' 和 'codespaces.pub' 的密钥对，
// 这可能与同样名为 'codespaces' 的建议 ssh 配置冲突。现在改用 'codespaces.auto'
// 和 'codespaces.auto.pub' 以避免该冲突。
const automaticPrivateKeyNameOld = "codespaces"
const automaticPrivateKeyName = "codespaces.auto"

var errKeyFileNotFound = errors.New("SSH 密钥文件不存在")

type sshOptions struct {
	selector         *CodespaceSelector
	profile          string
	serverPort       int
	printConnDetails bool
	debug            bool
	debugFile        string
	stdio            bool
	config           bool
	scpArgs          []string // scp 参数，用于 'cs cp' ('cs ssh' 时为 nil)
}

func newSSHCmd(app *App) *cobra.Command {
	var opts sshOptions

	sshCmd := &cobra.Command{
		Use:   "ssh [<flags>...] [-- <ssh-flags>...] [<command>]",
		Short: "通过 SSH 连接到 codespace",
		Long: heredoc.Docf(`
			%[1]sssh%[1]s 命令用于通过 SSH 连接到 codespace。最简单的用法是运行
			%[1]sgh cs ssh%[1]s，交互式选择一个 codespace 并连接。

			如果你没有现有的有效密钥对，%[1]sssh%[1]s 命令会在 %[1]s~/.ssh%[1]s 目录中
			自动创建一个公钥/私钥对。选择要使用的密钥对时，优先顺序如下:

			1. 在 %[1]s<ssh-flags>%[1]s 中通过 %[1]s-i%[1]s 指定的密钥
			2. 自动密钥 (若已存在)
			3. ssh 配置中的第一个有效密钥对 (依据 %[1]sssh -G%[1]s)
			4. 自动密钥 (新创建)

			%[1]sssh%[1]s 命令还通过 %[1]s--config%[1]s 选项支持与 OpenSSH 的深度集成，
			该选项以 OpenSSH 格式生成每个 codespace 的 ssh 配置。
			将该配置包含在 %[1]s~/.ssh/config%[1]s 中，可以改善与 OpenSSH 集成的
			工具的使用体验，例如 ssh 主机名的 Bash/Zsh 补全、
			%[1]sscp/rsync/sshfs%[1]s 的远程路径补全、%[1]sgit%[1]s 的 ssh 远程等。

			完成该设置后 (参见下方第二个示例)，你就可以像连接普通远程主机一样
			通过 SSH 连接 codespace (使用 %[1]sssh%[1]s，而非 %[1]sgh cs ssh%[1]s)。

			注意: 要连接的 codespace 必须预装 SSH 服务器。
			如果 codespace 使用的 docker 镜像没有 SSH 服务器，
			请在 %[1]sDockerfile%[1]s 中安装；对于使用 Debian 系镜像的 codespace，
			可以在 %[1]sdevcontainer.json%[1]s 中添加以下内容:

				"features": {
					"ghcr.io/devcontainers/features/sshd:1": {
						"version": "latest"
					}
				}
		`, "`"),
		Example: heredoc.Doc(`
			$ gh codespace ssh

			$ gh codespace ssh --config > ~/.ssh/codespaces
			$ printf 'Match all\nInclude ~/.ssh/codespaces\n' >> ~/.ssh/config
		`),
		PreRunE: func(c *cobra.Command, args []string) error {
			if opts.stdio {
				if opts.selector.codespaceName == "" {
					return errors.New("`--stdio` 需要显式指定 `--codespace`")
				}
				if opts.config {
					return errors.New("`--stdio` 不能与 `--config` 同时使用")
				}
				if opts.serverPort != 0 {
					return errors.New("`--stdio` 不能与 `--server-port` 同时使用")
				}
				if opts.profile != "" {
					return errors.New("`--stdio` 不能与 `--profile` 同时使用")
				}
			}
			if opts.config {
				if opts.profile != "" {
					return errors.New("`--config` 不能与 `--profile` 同时使用")
				}
				if opts.serverPort != 0 {
					return errors.New("`--config` 不能与 `--server-port` 同时使用")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flag("server-port").Changed {
				opts.printConnDetails = true
			}
			if opts.config {
				return app.printOpenSSHConfig(cmd.Context(), opts)
			} else {
				return app.SSH(cmd.Context(), args, opts)
			}
		},
		DisableFlagsInUseLine: true,
	}

	sshCmd.Flags().StringVarP(&opts.profile, "profile", "", "", "要使用的 SSH 配置名称")
	sshCmd.Flags().IntVarP(&opts.serverPort, "server-port", "", 0, "SSH 服务器端口 (0 => 自动选择未用端口)")
	opts.selector = AddCodespaceSelector(sshCmd, app.apiClient)
	sshCmd.Flags().BoolVarP(&opts.debug, "debug", "d", false, "将调试数据记录到文件")
	sshCmd.Flags().StringVarP(&opts.debugFile, "debug-file", "", "", "日志文件路径")
	sshCmd.Flags().BoolVarP(&opts.config, "config", "", false, "将 OpenSSH 配置写入 stdout")
	sshCmd.Flags().BoolVar(&opts.stdio, "stdio", false, "将 sshd 连接代理到 stdio")
	if err := sshCmd.Flags().MarkHidden("stdio"); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}

	return sshCmd
}

type combinedReadWriteHalfCloser struct {
	io.ReadCloser
	io.WriteCloser
}

func (crwc *combinedReadWriteHalfCloser) Close() error {
	werr := crwc.WriteCloser.Close()
	rerr := crwc.ReadCloser.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

func (crwc *combinedReadWriteHalfCloser) CloseWrite() error {
	return crwc.WriteCloser.Close()
}

// SSH 在 codespace 中打开 ssh 会话或执行 ssh 命令。
func (a *App) SSH(ctx context.Context, sshArgs []string, opts sshOptions) (err error) {
	// 确保所有子任务 (如端口转发) 在返回前结束。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := sshArgs
	if opts.scpArgs != nil {
		args = opts.scpArgs
	}

	sshContext := ssh.Context{}
	startSSHOptions := rpc.StartSSHServerOptions{}

	keyPair, shouldAddArg, err := selectSSHKeys(ctx, sshContext, args, opts)
	if err != nil {
		return fmt.Errorf("选择 SSH 密钥失败: %w", err)
	}

	startSSHOptions.UserPublicKeyFile = keyPair.PublicKeyPath

	if shouldAddArg {
		// 对 cp 和 ssh 而言，标志需要放在参数最前面 (在 ssh 的命令和 cp 的文件之前)，因此前置该标志
		args = append([]string{"-i", keyPair.PrivateKeyPath}, args...)
	}

	codespace, err := opts.selector.Select(ctx)
	if err != nil {
		return err
	}

	codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, codespace)
	if err != nil {
		return fmt.Errorf("连接 codespace 时出错: %w", err)
	}

	fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
	if err != nil {
		return fmt.Errorf("创建端口转发器失败: %w", err)
	}
	defer safeClose(fwd, &err)

	var (
		invoker             rpc.Invoker
		remoteSSHServerPort int
		sshUser             string
	)
	err = a.RunWithProgress("正在获取 SSH 详情", func() (err error) {
		invoker, err = rpc.CreateInvoker(ctx, fwd)
		if err != nil {
			return
		}

		remoteSSHServerPort, sshUser, err = invoker.StartSSHServerWithOptions(ctx, startSSHOptions)
		return
	})
	if invoker != nil {
		defer safeClose(invoker, &err)
	}
	if err != nil {
		return fmt.Errorf("获取 SSH 服务器详情时出错: %w", err)
	}

	if opts.stdio {
		stdio := &combinedReadWriteHalfCloser{os.Stdin, os.Stdout}
		opts := portforwarder.ForwardPortOpts{
			Port:      remoteSSHServerPort,
			Internal:  true,
			KeepAlive: true,
		}

		// Forward the port
		// 转发端口
		err = fwd.ForwardPort(ctx, opts)
		if err != nil {
			return fmt.Errorf("转发端口失败: %w", err)
		}

		// Connect to the forwarded port
		// 连接到转发后的端口
		err = fwd.ConnectToForwardedPort(ctx, stdio, opts)
		if err != nil {
			return fmt.Errorf("连接转发端口失败: %w", err)
		}

		return fmt.Errorf("隧道已关闭: %w", err)
	}

	localSSHServerPort := opts.serverPort

	// Ensure local port is listening before client (Shell) connects.
	// Unless the user specifies a server port, localSSHServerPort is 0
	// and thus the client will pick a random port.
	// 在客户端 (Shell) 连接之前，确保本地端口已开始监听。
	// 用户未指定服务器端口时 localSSHServerPort 为 0，
	// 因此客户端会随机选择一个端口。
	listen, localSSHServerPort, err := codespaces.ListenTCP(localSSHServerPort, false)
	if err != nil {
		return err
	}
	defer listen.Close()

	connectDestination := opts.profile
	if connectDestination == "" {
		connectDestination = fmt.Sprintf("%s@localhost", sshUser)
	}

	tunnelClosed := make(chan error, 1)
	go func() {
		opts := portforwarder.ForwardPortOpts{
			Port:      remoteSSHServerPort,
			Internal:  true,
			KeepAlive: true,
		}
		tunnelClosed <- fwd.ForwardPortToListener(ctx, opts, listen)
	}()

	shellClosed := make(chan error, 1)
	go func() {
		if opts.scpArgs != nil {
			// args is the correct variable to use here, we just use scpArgs as the check for which command to run
			shellClosed <- codespaces.Copy(ctx, args, localSSHServerPort, connectDestination)
		} else {
			// Parse the ssh args to determine if the user specified a command
			args, command, err := codespaces.ParseSSHArgs(args)
			if err != nil {
				shellClosed <- err
				return
			}

			// If the user specified a command, we need to keep the shell alive
			// since it will be non-interactive and the codespace might shut down
			// before the command finishes
			if command != nil {
				invoker.KeepAlive()
			}

			shellClosed <- codespaces.Shell(
				ctx, a.errLogger, args, command, localSSHServerPort, connectDestination, opts.printConnDetails,
			)
		}
	}()

	select {
	case err := <-tunnelClosed:
		return fmt.Errorf("tunnel closed: %w", err)
	case err := <-shellClosed:
		if err != nil {
			return fmt.Errorf("shell closed: %w", err)
		}
		return nil // success
	}
}

// selectSSHKeys evaluates available key pairs and select which should be used to connect to the codespace
// using the precedence rules below. If there is no error, a keypair is always returned and additionally a
// bool flag is returned to specify if the private key need be appended to the ssh arguments (it doesn't need
// to be if the key was selected from a -i argument).
//
// Precedence rules:
// 1. Key which is specified by -i
// 2. Automatic key, if it already exists
// 3. First valid keypair in ssh config (according to ssh -G)
// 4. Automatic key, newly created
func selectSSHKeys(
	ctx context.Context,
	sshContext ssh.Context,
	args []string,
	opts sshOptions,
) (*ssh.KeyPair, bool, error) {
	customConfigPath := ""
	for i := range args {
		arg := args[i]

		if arg == "-i" {
			if i+1 >= len(args) {
				return nil, false, errors.New("missing value to -i argument")
			}

			privateKeyPath := args[i+1]

			// The --config setup will set the automatic key with -i, but it might not actually be created, so we need to ensure that here
			if automaticPrivateKeyPath, _ := automaticPrivateKeyPath(sshContext); automaticPrivateKeyPath == privateKeyPath {
				_, err := generateAutomaticSSHKeys(sshContext)
				if err != nil {
					return nil, false, fmt.Errorf("generating automatic keypair: %w", err)
				}
			}

			// User manually specified an identity file so just trust it is correct
			return &ssh.KeyPair{
				PrivateKeyPath: privateKeyPath,
				PublicKeyPath:  privateKeyPath + ".pub",
			}, false, nil
		}

		if arg == "-F" && i+1 < len(args) {
			// ssh only pays attention to that last specified -F value, so it's correct to overwrite here
			customConfigPath = args[i+1]
		}
	}

	if autoKeyPair := automaticSSHKeyPair(sshContext); autoKeyPair != nil {
		// If the automatic keys already exist, just use them
		return autoKeyPair, true, nil
	}

	keyPair, err := firstConfiguredKeyPair(ctx, customConfigPath, opts.profile)
	if err != nil {
		if !errors.Is(err, errKeyFileNotFound) {
			return nil, false, fmt.Errorf("checking configured keys: %w", err)
		}

		// no valid key in ssh config, generate one
		keyPair, err = generateAutomaticSSHKeys(sshContext)
		if err != nil {
			return nil, false, fmt.Errorf("generating automatic keypair: %w", err)
		}
	}

	return keyPair, true, nil
}

// automaticSSHKeyPair returns the paths to the automatic key pair files, if they both exist
func automaticSSHKeyPair(sshContext ssh.Context) *ssh.KeyPair {
	publicKeys, err := sshContext.LocalPublicKeys()
	if err != nil {
		// The error would be that the .ssh dir doesn't exist, which just means that the keypair also doesn't exist
		return nil
	}

	for _, publicKey := range publicKeys {
		if filepath.Base(publicKey) != automaticPrivateKeyName+".pub" {
			continue
		}

		privateKey := strings.TrimSuffix(publicKey, ".pub")

		_, err := os.Stat(privateKey)
		if err == nil {
			return &ssh.KeyPair{
				PrivateKeyPath: privateKey,
				PublicKeyPath:  publicKey,
			}
		}
	}

	return nil
}

func generateAutomaticSSHKeys(sshContext ssh.Context) (*ssh.KeyPair, error) {
	keyPair := checkAndUpdateOldKeyPair(sshContext)
	if keyPair != nil {
		return keyPair, nil
	}

	keyPair, err := sshContext.GenerateSSHKey(automaticPrivateKeyName, "")
	if err != nil && !errors.Is(err, ssh.ErrKeyAlreadyExists) {
		return nil, err
	}

	return keyPair, nil
}

// checkAndUpdateOldKeyPair handles backward compatibility with the old keypair names.
// If the old public and private keys both exist they are renamed to the new name.
// The return value is non-nil only if the rename happens.
func checkAndUpdateOldKeyPair(sshContext ssh.Context) *ssh.KeyPair {
	publicKeys, err := sshContext.LocalPublicKeys()
	if err != nil {
		return nil
	}

	for _, publicKey := range publicKeys {
		if filepath.Base(publicKey) != automaticPrivateKeyNameOld+".pub" {
			continue
		}

		privateKey := strings.TrimSuffix(publicKey, ".pub")
		_, err := os.Stat(privateKey)
		if err != nil {
			continue
		}

		// Both old public and private keys exist, rename them to the new name

		sshDir := filepath.Dir(publicKey)

		publicKeyNew := filepath.Join(sshDir, automaticPrivateKeyName+".pub")
		err = os.Rename(publicKey, publicKeyNew)
		if err != nil {
			return nil
		}

		privateKeyNew := filepath.Join(sshDir, automaticPrivateKeyName)
		err = os.Rename(privateKey, privateKeyNew)
		if err != nil {
			return nil
		}

		keyPair := &ssh.KeyPair{
			PublicKeyPath:  publicKeyNew,
			PrivateKeyPath: privateKeyNew,
		}

		return keyPair
	}

	return nil
}

// firstConfiguredKeyPair reads the effective configuration for a localhost
// connection and returns the first valid key pair which would be tried for authentication
func firstConfiguredKeyPair(
	ctx context.Context,
	customConfigFile string,
	customHost string,
) (*ssh.KeyPair, error) {
	sshExe, err := safeexec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("could not find ssh executable: %w", err)
	}

	// The -G option tells ssh to output the effective config for the given host, but not connect
	sshGArgs := []string{"-G"}

	if customConfigFile != "" {
		sshGArgs = append(sshGArgs, "-F", customConfigFile)
	}

	if customHost != "" {
		sshGArgs = append(sshGArgs, customHost)
	} else {
		sshGArgs = append(sshGArgs, "localhost")
	}

	sshGCmd := exec.CommandContext(ctx, sshExe, sshGArgs...)
	configBytes, err := sshGCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("could not load ssh configuration: %w", err)
	}

	configLines := strings.SplitSeq(string(configBytes), "\n")
	for line := range configLines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "identityfile ") {
			privateKeyPath := strings.SplitN(line, " ", 2)[1]

			keypair, err := keypairForPrivateKey(privateKeyPath)
			if errors.Is(err, errKeyFileNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("loading ssh config: %w", err)
			}

			return keypair, nil
		}
	}

	return nil, errKeyFileNotFound
}

// keypairForPrivateKey returns the KeyPair with the specified private key if it and the public key both exist
func keypairForPrivateKey(privateKeyPath string) (*ssh.KeyPair, error) {
	if strings.HasPrefix(privateKeyPath, "~") {
		userHomeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("getting home dir: %w", err)
		}

		// os.Stat can't handle ~, so convert it to the real path
		privateKeyPath = strings.Replace(privateKeyPath, "~", userHomeDir, 1)
	}

	// The default configuration includes standard keys like id_rsa or id_ed25519,
	// but these may not actually exist
	if _, err := os.Stat(privateKeyPath); err != nil {
		return nil, errKeyFileNotFound
	}

	publicKeyPath := privateKeyPath + ".pub"
	if _, err := os.Stat(publicKeyPath); err != nil {
		return nil, errKeyFileNotFound
	}

	return &ssh.KeyPair{
		PrivateKeyPath: privateKeyPath,
		PublicKeyPath:  publicKeyPath,
	}, nil
}

func (a *App) printOpenSSHConfig(ctx context.Context, opts sshOptions) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var csList []*api.Codespace
	if opts.selector.codespaceName == "" {
		err = a.RunWithProgress("正在获取 codespace", func() (err error) {
			csList, err = a.apiClient.ListCodespaces(ctx, api.ListCodespacesOptions{})
			return
		})
	} else {
		var codespace *api.Codespace
		codespace, err = opts.selector.Select(ctx)
		csList = []*api.Codespace{codespace}
	}
	if err != nil {
		return fmt.Errorf("获取 codespace 信息时出错: %w", err)
	}

	type sshResult struct {
		codespace *api.Codespace
		user      string // 成功时为远程 ssh 用户名；否则为 nil
		err       error
	}

	sshUsers := make(chan sshResult, len(csList))
	var wg sync.WaitGroup
	var status error
	for _, cs := range csList {
		if cs.State != "Available" && opts.selector.codespaceName == "" {
			fmt.Fprintf(os.Stderr, "跳过不可用的 codespace %s: %s\n", cs.Name, cs.State)
			status = cmdutil.SilentError
			continue
		}

		wg.Add(1)
		go func(cs *api.Codespace) {
			result := sshResult{}
			defer wg.Done()

			codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, cs)
			if err != nil {
				result.err = fmt.Errorf("连接 codespace 时出错: %w", err)
				sshUsers <- result
				return
			}

			fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
			if err != nil {
				result.err = fmt.Errorf("创建端口转发器失败: %w", err)
				sshUsers <- result
				return
			}
			defer safeClose(fwd, &err)

			invoker, err := rpc.CreateInvoker(ctx, fwd)
			if err != nil {
				result.err = fmt.Errorf("连接 codespace 时出错: %w", err)
				sshUsers <- result
				return
			}
			defer safeClose(invoker, &err)

			_, result.user, err = invoker.StartSSHServer(ctx)
			if err != nil {
				result.err = fmt.Errorf("获取 SSH 服务器详情时出错: %w", err)
				sshUsers <- result
				return
			}

			result.codespace = cs
			sshUsers <- result
		}(cs)
	}

	go func() {
		wg.Wait()
		close(sshUsers)
	}()

	t, err := template.New("ssh_config").Parse(heredoc.Doc(`
		Host cs.{{.Name}}.{{.EscapedRef}}
			User {{.SSHUser}}
			ProxyCommand {{.GHExec}} cs ssh -c {{.Name}} --stdio -- -i {{.AutomaticIdentityFilePath}}
			UserKnownHostsFile=/dev/null
			StrictHostKeyChecking no
			LogLevel quiet
			ControlMaster auto
			IdentityFile {{.AutomaticIdentityFilePath}}

	`))
	if err != nil {
		return fmt.Errorf("格式化模板时出错: %w", err)
	}

	sshContext := ssh.Context{}
	automaticIdentityFilePath, err := automaticPrivateKeyPath(sshContext)
	if err != nil {
		return fmt.Errorf("查找 .ssh 目录时出错: %w", err)
	}

	ghExec := a.executable.Executable()
	for result := range sshUsers {
		if result.err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", result.err)
			status = cmdutil.SilentError
			continue
		}

		// codespaceSSHConfig 包含为单个 codespace 编写 OpenSSH 主机
		// 配置所需的值。例如:
		//
		// Host {{Name}}.{{EscapedRef}
		//   User {{SSHUser}
		//   ProxyCommand {{GHExec}} cs ssh -c {{Name}} --stdio
		//
		// 名称中包含 EscapedRef 是为了在按 Tab 补全 ssh 主机名时
		// 更容易区分不同 codespace。EscapedRef 中的 '/' 字符会被
		// 压平为 '-'，以避免 Tab 补全出现问题，或主机名出现在
		// ControlMaster 套接字路径中时出问题。
		type codespaceSSHConfig struct {
			Name                      string // codespace 名称，传给 `ssh -c`
			EscapedRef                string // 当前检出的分支
			SSHUser                   string // 远程 ssh 用户名
			GHExec                    string // 用于调用当前 `gh` 二进制的路径
			AutomaticIdentityFilePath string // 自动生成私钥时 `gh cs ssh` 使用的路径
		}

		conf := codespaceSSHConfig{
			Name:                      result.codespace.Name,
			EscapedRef:                strings.ReplaceAll(result.codespace.GitStatus.Ref, "/", "-"),
			SSHUser:                   result.user,
			GHExec:                    ghExec,
			AutomaticIdentityFilePath: automaticIdentityFilePath,
		}
		if err := t.Execute(a.io.Out, conf); err != nil {
			return err
		}
	}

	return status
}

func automaticPrivateKeyPath(sshContext ssh.Context) (string, error) {
	sshDir, err := sshContext.SshDir()
	if err != nil {
		return "", err
	}

	return path.Join(sshDir, automaticPrivateKeyName), nil
}

type cpOptions struct {
	sshOptions
	recursive bool // -r
	expand    bool // -e
}

func newCpCmd(app *App) *cobra.Command {
	var opts cpOptions

	cpCmd := &cobra.Command{
		Use:   "cp [-e] [-r] [-- [<scp flags>...]] <sources>... <dest>",
		Short: "在本地与远程文件系统之间复制文件",
		Long: heredoc.Docf(`
			%[1]scp%[1]s 命令在本地与远程文件系统之间复制文件。

			与 UNIX %[1]sscp%[1]s 命令一样，第一个参数指定源，最后一个参数指定
			目标；若目标为目录，则第一个参数之后还可以指定更多源。

			任一源为目录时必须使用 %[1]s--recursive%[1]s 标志。

			文件名参数带 %[1]sremote:%[1]s 前缀表示引用远程 (Codespace) 机器的
			文件系统，相对远程用户主目录解析。

			默认按字面解释远程文件名。使用 %[1]s--expand%[1]s 标志时，
			每个此类参数会按 %[1]sscp%[1]s 的方式处理，作为在远程机器上求值的
			Bash 表达式，可进行波浪号、花括号、glob、环境变量和反引号展开。
			出于安全考虑，不要将此标志用于不可信用户提供的参数；
			相关讨论见 <https://lwn.net/Articles/835962/>。

			默认情况下，%[1]scp%[1]s 命令会在 %[1]s~/.ssh 目录%[1]s 中创建
			公钥/私钥对，用于向 codespace 进行身份验证。
		`, "`"),
		Example: heredoc.Doc(`
			$ gh codespace cp -e README.md 'remote:/workspaces/$RepositoryName/'
			$ gh codespace cp -e 'remote:~/*.go' ./gofiles/
			$ gh codespace cp -e 'remote:/workspaces/myproj/go.{mod,sum}' ./gofiles/
			$ gh codespace cp -e -- -F ~/.ssh/codespaces_config 'remote:~/*.go' ./gofiles/
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Copy(cmd.Context(), args, opts)
		},
		DisableFlagsInUseLine: true,
	}

	// We don't expose all sshOptions.
	// 不对外暴露全部 sshOptions。
	cpCmd.Flags().BoolVarP(&opts.recursive, "recursive", "r", false, "递归复制目录")
	cpCmd.Flags().BoolVarP(&opts.expand, "expand", "e", false, "在远程 shell 上展开远程文件名")
	opts.selector = AddCodespaceSelector(cpCmd, app.apiClient)
	cpCmd.Flags().StringVarP(&opts.profile, "profile", "p", "", "要使用的 SSH 配置名称")
	return cpCmd
}

// Copy 在本地与远程文件系统之间复制文件。
// 机制与 'ssh' 类似，但使用 'scp'。
func (a *App) Copy(ctx context.Context, args []string, opts cpOptions) error {
	if len(args) < 2 {
		return fmt.Errorf("cp 需要源参数和目标参数")
	}
	if opts.recursive {
		opts.scpArgs = append(opts.scpArgs, "-r")
	}

	hasRemote := false
	for _, arg := range args {
		if rest, ok := strings.CutPrefix(arg, "remote:"); ok {
			hasRemote = true
			// scp treats each filename argument as a shell expression,
			// subjecting it to expansion of environment variables, braces,
			// tilde, backticks, globs and so on. Because these present a
			// security risk (see https://lwn.net/Articles/835962/), we
			// disable them by shell-escaping the argument unless the user
			// provided the -e flag.
			// scp 会将每个文件名参数视为 shell 表达式，对其进行环境变量、
			// 花括号、波浪号、反引号、glob 等展开。由于这会带来安全风险
			// (见 https://lwn.net/Articles/835962/)，除非用户提供了 -e 标志，
			// 否则我们会对参数做 shell 转义以禁用这些展开。
			if !opts.expand {
				arg = `remote:'` + strings.Replace(rest, `'`, `'\''`, -1) + `'`
			}

		} else if !filepath.IsAbs(arg) {
			// scp treats a colon in the first path segment as a host identifier.
			// Escape it by prepending "./".
			// TODO(adonovan): test on Windows, including with a c:\\foo path.
			// scp 会将第一个路径段中的冒号视为主机标识符。
			// 通过前置 "./" 进行转义。
			// TODO(adonovan): 在 Windows 上测试，包括 c:\\foo 这类路径。
			const sep = string(os.PathSeparator)
			first, _, _ := strings.Cut(filepath.ToSlash(arg), sep)
			if strings.Contains(first, ":") {
				arg = "." + sep + arg
			}
		}
		opts.scpArgs = append(opts.scpArgs, arg)
	}
	if !hasRemote {
		return cmdutil.FlagErrorf("至少一个参数必须带有 'remote:' 前缀")
	}
	return a.SSH(ctx, nil, opts.sshOptions)
}
