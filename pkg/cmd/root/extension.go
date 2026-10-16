package root

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cli/cli/v2/internal/update"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/extensions"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/utils"
	"github.com/spf13/cobra"
)

type ExternalCommandExitError struct {
	*exec.ExitError
}

func NewCmdExtension(io *iostreams.IOStreams, em extensions.ExtensionManager, ext extensions.Extension, checkExtensionReleaseInfo func(extensions.ExtensionManager, extensions.Extension) (*update.ReleaseInfo, error)) *cobra.Command {
	updateMessageChan := make(chan *update.ReleaseInfo)
	cs := io.ColorScheme()
	hasDebug, _ := utils.IsDebugEnabled()
	if checkExtensionReleaseInfo == nil {
		checkExtensionReleaseInfo = checkForExtensionUpdate
	}

	cmd := &cobra.Command{
		Use:   ext.Name(),
		Short: fmt.Sprintf("扩展(extension) %s", ext.Name()),
		// 仅在命令运行时，PreRun 才负责查询该扩展是否有最新版本。
		PreRun: func(c *cobra.Command, args []string) {
			go func() {
				releaseInfo, err := checkExtensionReleaseInfo(em, ext)
				if err != nil && hasDebug {
					fmt.Fprintf(io.ErrOut, "警告: 检查更新失败: %v", err)
				}
				updateMessageChan <- releaseInfo
			}()
		},
		RunE: func(c *cobra.Command, args []string) error {
			args = append([]string{ext.Name()}, args...)
			if _, err := em.Dispatch(args, io.In, io.Out, io.ErrOut); err != nil {
				if execError, ok := errors.AsType[*exec.ExitError](err); ok {
					return &ExternalCommandExitError{execError}
				}
				return fmt.Errorf("运行扩展(extension)失败: %w\n", err)
			}
			return nil
		},
		// 若已找到，PostRun 负责传达扩展的发布信息
		PostRun: func(c *cobra.Command, args []string) {
			select {
			case releaseInfo := <-updateMessageChan:
				if releaseInfo != nil {
					stderr := io.ErrOut
					fmt.Fprintf(stderr, "\n\n%s %s → %s\n",
						cs.Yellowf("%s 有新版本可用:", ext.Name()),
						cs.Cyan(strings.TrimPrefix(ext.CurrentVersion(), "v")),
						cs.Cyan(strings.TrimPrefix(releaseInfo.Version, "v")))
					if ext.IsPinned() {
						fmt.Fprintf(stderr, "升级命令: gh extension upgrade %s --force\n", ext.Name())
					} else {
						fmt.Fprintf(stderr, "升级命令: gh extension upgrade %s\n", ext.Name())
					}
					fmt.Fprintf(stderr, "%s\n\n",
						cs.Yellow(releaseInfo.URL))
				}
			default:
				// 若此时扩展更新检查尚未完成，不要让用户继续等待。
				// 这里以非阻塞的 default 分支处理，因为不像 gh 的更新检查那样有可取消的 context。
			}
		},
		GroupID:            "extension",
		DisableFlagParsing: true,
	}

	cmdutil.DisableAuthCheck(cmd)
	// 扩展由用户安装，其名称可以是任意的
	// (可能包含项目或组织名称等敏感标识符)，因此默认情况下
	// 不得为它们记录遥测(telemetry)数据。GitHub 官方扩展是一个已知的、
	// 固定的集合，可以安全地将其命令名贡献给遥测数据。
	if !extensions.IsOfficial(ext.Name(), ext.Owner()) {
		cmdutil.DisableTelemetry(cmd)
	}

	return cmd
}

func checkForExtensionUpdate(em extensions.ExtensionManager, ext extensions.Extension) (*update.ReleaseInfo, error) {
	if !update.ShouldCheckForExtensionUpdate() {
		return nil, nil
	}

	return update.CheckForExtensionUpdate(em, ext, time.Now())
}
