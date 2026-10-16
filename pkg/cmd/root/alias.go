package root

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/cli/cli/v2/internal/run"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/findsh"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/google/shlex"
	"github.com/spf13/cobra"
)

func NewCmdShellAlias(io *iostreams.IOStreams, aliasName, aliasValue string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   aliasName,
		Short: fmt.Sprintf("%q 的 shell 别名(alias)", text.Truncate(80, aliasValue)),
		RunE: func(c *cobra.Command, args []string) error {
			expandedArgs, err := expandShellAlias(aliasValue, args, nil)
			if err != nil {
				return err
			}
			externalCmd := exec.Command(expandedArgs[0], expandedArgs[1:]...)
			externalCmd.Stderr = io.ErrOut
			externalCmd.Stdout = io.Out
			externalCmd.Stdin = io.In
			preparedCmd := run.PrepareCmd(externalCmd)
			if err = preparedCmd.Run(); err != nil {
				if execError, ok := errors.AsType[*exec.ExitError](err); ok {
					return &ExternalCommandExitError{execError}
				}
				return fmt.Errorf("运行外部命令失败: %w\n", err)
			}
			return nil
		},
		GroupID:            "alias",
		DisableFlagParsing: true,
	}
	cmdutil.DisableAuthCheck(cmd)
	// 别名(alias)是用户定义的名称，不得作为遥测(telemetry)维度上报，
	// 因为名称本身可能包含敏感信息(例如项目或组织名称)。
	cmdutil.DisableTelemetry(cmd)
	return cmd
}

func NewCmdAlias(io *iostreams.IOStreams, aliasName, aliasValue string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   aliasName,
		Short: fmt.Sprintf("%q 的别名(alias)", text.Truncate(80, aliasValue)),
		RunE: func(c *cobra.Command, args []string) error {
			expandedArgs, err := expandAlias(aliasValue, args)
			if err != nil {
				return err
			}
			root := c.Root()
			root.SetArgs(expandedArgs)
			return root.Execute()
		},
		GroupID:            "alias",
		DisableFlagParsing: true,
		Annotations: map[string]string{
			aliasExpansionAnnotation: aliasValue,
		},
	}
	cmdutil.DisableAuthCheck(cmd)
	// 别名(alias)是用户定义的名称，不得作为遥测(telemetry)维度上报，
	// 因为名称本身可能包含敏感信息(例如项目或组织名称)。
	cmdutil.DisableTelemetry(cmd)
	return cmd
}

// ExpandAlias 处理 argv，判断是否应根据用户的别名(alias)进行重写。
func expandAlias(expansion string, args []string) ([]string, error) {
	extraArgs := []string{}
	for i, a := range args {
		if !strings.Contains(expansion, "$") {
			extraArgs = append(extraArgs, a)
		} else {
			expansion = strings.ReplaceAll(expansion, fmt.Sprintf("$%d", i+1), a)
		}
	}

	lingeringRE := regexp.MustCompile(`\$\d`)
	if lingeringRE.MatchString(expansion) {
		return nil, fmt.Errorf("别名(alias)的参数不足: %s", expansion)
	}

	newArgs, err := shlex.Split(expansion)
	if err != nil {
		return nil, err
	}

	expanded := append(newArgs, extraArgs...)

	return expanded, nil
}

// ExpandShellAlias 处理 argv，判断是否应根据用户的别名(alias)进行重写。
func expandShellAlias(expansion string, args []string, findShFunc func() (string, error)) ([]string, error) {
	if findShFunc == nil {
		findShFunc = findSh
	}

	shPath, shErr := findShFunc()
	if shErr != nil {
		return nil, shErr
	}

	expanded := []string{shPath, "-c", expansion[1:]}

	if len(args) > 0 {
		expanded = append(expanded, "--")
		expanded = append(expanded, args...)
	}

	return expanded, nil
}

func findSh() (string, error) {
	shPath, err := findsh.Find()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			if runtime.GOOS == "windows" {
				return "", errors.New("无法找到用于执行 shell 别名(alias)的 sh。sh.exe 解释器通常随 Git for Windows 一起分发。")
			}
			return "", errors.New("无法找到用于执行 shell 别名(alias)的 sh")
		}
		return "", err
	}
	return shPath, nil
}
