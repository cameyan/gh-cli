package codespace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/codespaces/portforwarder"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/microsoft/dev-tunnels/go/tunnels"
	"github.com/muhammadmuzzammil1998/jsonc"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// newPortsCmd 返回一个 Cobra "ports" 命令，根据指定标志显示可用端口的表格。
func newPortsCmd(app *App) *cobra.Command {
	var (
		selector *CodespaceSelector
		exporter cmdutil.Exporter
	)

	portsCmd := &cobra.Command{
		Use:   "ports",
		Short: "列出 codespace 中的端口",
		Args:  noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.ListPorts(cmd.Context(), selector, exporter)
		},
	}

	selector = AddCodespaceSelector(portsCmd, app.apiClient)

	cmdutil.AddJSONFlags(portsCmd, &exporter, portFields)

	portsCmd.AddCommand(newPortsForwardCmd(app, selector))
	portsCmd.AddCommand(newPortsVisibilityCmd(app, selector))

	return portsCmd
}

// ListPorts 列出 codespace 中已知的端口。
func (a *App) ListPorts(ctx context.Context, selector *CodespaceSelector, exporter cmdutil.Exporter) (err error) {
	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
	}

	devContainerCh := getDevContainer(ctx, a.apiClient, codespace)

	codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, codespace)
	if err != nil {
		return fmt.Errorf("连接 codespace 时出错: %w", err)
	}

	fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
	if err != nil {
		return fmt.Errorf("创建端口转发器失败: %w", err)
	}
	defer safeClose(fwd, &err)

	var ports []*tunnels.TunnelPort
	err = a.RunWithProgress("正在获取端口", func() (err error) {
		ports, err = fwd.ListPorts(ctx)
		return
	})
	if err != nil {
		return fmt.Errorf("获取共享服务器端口时出错: %w", err)
	}

	devContainerResult := <-devContainerCh
	if devContainerResult.err != nil {
		// 读取 devcontainer 文件失败的警告，不属于 codespace 命令错误。
		a.errLogger.Printf("获取端口名称失败: %v", devContainerResult.err.Error())
	}

	var portInfos []*portInfo

	for _, p := range ports {
		// 从列表中过滤掉内部端口
		if portforwarder.IsInternalPort(p) {
			continue
		}

		portInfos = append(portInfos, &portInfo{
			Port:         p,
			codespace:    codespace,
			devContainer: devContainerResult.devContainer,
		})
	}

	if err := a.io.StartPager(); err != nil {
		a.errLogger.Printf("启动分页器出错: %v", err)
	}
	defer a.io.StopPager()

	if exporter != nil {
		return exporter.Write(a.io, portInfos)
	}

	cs := a.io.ColorScheme()
	tp := tableprinter.New(a.io, tableprinter.WithHeader("LABEL", "PORT", "VISIBILITY", "BROWSE URL"))

	for _, port := range portInfos {
		// 将 ACE 转换为友好的可见性字符串 (private、org、public)
		visibility := portforwarder.AccessControlEntriesToVisibility(port.Port.AccessControl.Entries)

		tp.AddField(iostreams.NewUntrusted(port.Label()).String())
		tp.AddField(cs.Yellow(fmt.Sprintf("%d", port.Port.PortNumber)))
		tp.AddField(visibility)
		tp.AddField(port.BrowseURL())
		tp.EndRow()
	}
	return tp.Render()
}

type portInfo struct {
	Port         *tunnels.TunnelPort
	codespace    *api.Codespace
	devContainer *devContainer
}

func (pi *portInfo) BrowseURL() string {
	return fmt.Sprintf("https://%s-%d.app.github.dev", pi.codespace.Name, pi.Port.PortNumber)
}

func (pi *portInfo) Label() string {
	if pi.devContainer != nil {
		portStr := strconv.Itoa(int(pi.Port.PortNumber))
		if attributes, ok := pi.devContainer.PortAttributes[portStr]; ok {
			return attributes.Label
		}
	}
	return ""
}

var portFields = []string{
	"sourcePort",
	"visibility",
	"label",
	"browseUrl",
}

func (pi *portInfo) ExportData(fields []string) map[string]any {
	data := map[string]any{}

	for _, f := range fields {
		switch f {
		case "sourcePort":
			data[f] = pi.Port.PortNumber
		case "visibility":
			data[f] = portforwarder.AccessControlEntriesToVisibility(pi.Port.AccessControl.Entries)
		case "label":
			data[f] = pi.Label()
		case "browseUrl":
			data[f] = pi.BrowseURL()
		default:
			panic("unknown field: " + f)
		}
	}

	return data
}

type devContainerResult struct {
	devContainer *devContainer
	err          error
}

type devContainer struct {
	PortAttributes map[string]portAttribute `json:"portsAttributes"`
}

type portAttribute struct {
	Label string `json:"label"`
}

func getDevContainer(ctx context.Context, apiClient apiClient, codespace *api.Codespace) <-chan devContainerResult {
	ch := make(chan devContainerResult, 1)
	go func() {
		contents, err := apiClient.GetCodespaceRepositoryContents(ctx, codespace, ".devcontainer/devcontainer.json")
		if err != nil {
			ch <- devContainerResult{nil, fmt.Errorf("获取内容时出错: %w", err)}
			return
		}

		if contents == nil {
			ch <- devContainerResult{nil, nil}
			return
		}

		convertedJSON := normalizeJSON(jsonc.ToJSON(contents))
		if !jsonc.Valid(convertedJSON) {
			ch <- devContainerResult{nil, errors.New("将 JSONC 转换为标准 JSON 失败")}
			return
		}

		var container devContainer
		if err := json.Unmarshal(convertedJSON, &container); err != nil {
			ch <- devContainerResult{nil, fmt.Errorf("反序列化时出错: %w", err)}
			return
		}

		ch <- devContainerResult{&container, nil}
	}()
	return ch
}

func newPortsVisibilityCmd(app *App, selector *CodespaceSelector) *cobra.Command {
	return &cobra.Command{
		Use:   "visibility <port>:{public|private|org}...",
		Short: "更改转发端口的可见性",
		Example: heredoc.Doc(`
			$ gh codespace ports visibility 80:org 3000:private 8000:public
		`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.UpdatePortVisibility(cmd.Context(), selector, args)
		},
	}
}

func (a *App) UpdatePortVisibility(ctx context.Context, selector *CodespaceSelector, args []string) (err error) {
	ports, err := a.parsePortVisibilities(args)
	if err != nil {
		return fmt.Errorf("解析端口参数时出错: %w", err)
	}

	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
	}

	codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to codespace: %w", err)
	}

	fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
	if err != nil {
		return fmt.Errorf("创建端口转发器失败: %w", err)
	}
	defer safeClose(fwd, &err)

	// TODO: 检查端口可见性能否并行更新，而不是逐个顺序更新
	for _, port := range ports {
		err := a.RunWithProgress(fmt.Sprintf("正在将端口 %d 的可见性更新为: %s", port.number, port.visibility), func() (err error) {
			// 等待成功或失败
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			err = fwd.UpdatePortVisibility(ctx, port.number, port.visibility)
			if err != nil {
				return fmt.Errorf("将端口 %d 更新为 %s 时出错: %w", port.number, port.visibility, err)
			}
			return nil
		})
		if err != nil {
			return err
		}

	}

	return nil
}

type portVisibility struct {
	number     int
	visibility string
}

func (a *App) parsePortVisibilities(args []string) ([]portVisibility, error) {
	ports := make([]portVisibility, 0, len(args))
	for _, a := range args {
		fields := strings.Split(a, ":")
		if len(fields) != 2 {
			return nil, fmt.Errorf("%q 的端口可见性格式无效", a)
		}
		portStr, visibility := fields[0], fields[1]
		portNumber, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("无效的端口号: %w", err)
		}
		ports = append(ports, portVisibility{portNumber, visibility})
	}
	return ports, nil
}

// newPortsForwardCmd 返回一个 Cobra "ports forward" 子命令，将一组端口对
// 从 codespace 转发到本地主机。
func newPortsForwardCmd(app *App, selector *CodespaceSelector) *cobra.Command {
	var allInterfaces bool

	cmd := &cobra.Command{
		Use:   "forward <remote-port>:<local-port>...",
		Short: "转发端口",
		Long: heredoc.Docf(`
			将端口从 codespace 转发到本地计算机。

			端口默认绑定到回环地址 (%[1]s127.0.0.1%[1]s)。使用 %[1]s--all-interfaces%[1]s
			可绑定到所有网络接口。
		`, "`"),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.ForwardPorts(cmd.Context(), selector, args, allInterfaces)
		},
	}

	cmd.Flags().BoolVar(&allInterfaces, "all-interfaces", false, "监听所有网络接口")

	return cmd
}

func (a *App) ForwardPorts(ctx context.Context, selector *CodespaceSelector, ports []string, allInterfaces bool) (err error) {
	portPairs, err := getPortPairs(ports)
	if err != nil {
		return fmt.Errorf("获取端口对失败: %w", err)
	}

	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
	}

	codespaceConnection, err := codespaces.GetCodespaceConnection(ctx, a, a.apiClient, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to codespace: %w", err)
	}

	// 并发转发所有端口，任一失败即中止全部，
	// 包括取消 context。
	group, ctx := errgroup.WithContext(ctx)
	for _, pair := range portPairs {
		group.Go(func() error {
			listen, _, err := codespaces.ListenTCP(pair.local, allInterfaces)
			if err != nil {
				return err
			}
			defer listen.Close()

			a.errLogger.Printf("正在转发端口: 远程 %d <=> 本地 %s", pair.remote, listen.Addr())
			fwd, err := portforwarder.NewPortForwarder(ctx, codespaceConnection)
			if err != nil {
				return fmt.Errorf("创建端口转发器失败: %w", err)
			}
			defer safeClose(fwd, &err)

			opts := portforwarder.ForwardPortOpts{
				Port: pair.remote,
			}
			return fwd.ForwardPortToListener(ctx, opts, listen)
		})
	}
	return group.Wait() // first error
}

type portPair struct {
	remote, local int
}

// getPortPairs 将形如 "%d:%d" 的字符串列表解析为 (远程, 本地) 端口号对。
func getPortPairs(ports []string) ([]portPair, error) {
	pp := make([]portPair, 0, len(ports))

	for _, portString := range ports {
		parts := strings.Split(portString, ":")
		if len(parts) < 2 {
			return nil, fmt.Errorf("端口对 %q 无效", portString)
		}

		remote, err := strconv.Atoi(parts[0])
		if err != nil {
			return pp, fmt.Errorf("远程端口转整数失败: %w", err)
		}

		local, err := strconv.Atoi(parts[1])
		if err != nil {
			return pp, fmt.Errorf("本地端口转整数失败: %w", err)
		}

		pp = append(pp, portPair{remote, local})
	}

	return pp, nil
}

func normalizeJSON(j []byte) []byte {
	// remove trailing commas
	return bytes.ReplaceAll(j, []byte("},}"), []byte("}}"))
}
