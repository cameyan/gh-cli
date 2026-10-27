package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cli/cli/v2/git"
)

// WorktreeTarget 描述某个路径是新工作树(worktree)的位置，
// 还是可复用的已有关联工作树的根目录。
type WorktreeTarget struct {
	Path  string
	Reuse bool
}

// ResolveWorktreeTarget 校验 path，并判断它是否为当前仓库某个已有
// 关联工作树的根目录。
func ResolveWorktreeTarget(client *git.Client, path string) (WorktreeTarget, error) {
	target := WorktreeTarget{Path: path}
	if err := ensureWorktreePathSafe(path); err != nil {
		return target, err
	}

	reuse, err := resolveWorktreeTarget(client, path)
	if err != nil {
		return target, err
	}
	if !reuse {
		if err := ensureWorktreePathEmpty(path); err != nil {
			return target, err
		}
	}
	target.Reuse = reuse
	return target, nil
}

// WorktreeCheckoutCommands 返回将 branch 放入 target 所需的命令。
// startPoint 必须是适合 --track 的远端分支(branch)。
func WorktreeCheckoutCommands(client *git.Client, target WorktreeTarget, branch, startPoint string) ([][]string, bool) {
	branchExists := client.HasLocalBranch(context.Background(), branch)

	if target.Reuse {
		if branchExists {
			return [][]string{{"-C", target.Path, "checkout", branch}}, true
		}
		return [][]string{{"-C", target.Path, "checkout", "-b", branch, "--track", startPoint}}, false
	}

	if branchExists {
		return [][]string{{"worktree", "add", "--", target.Path, branch}}, true
	}
	return [][]string{{"worktree", "add", "--track", "-b", branch, "--", target.Path, startPoint}}, false
}

// resolveWorktreeTarget 询问 git path 所在位置，让 git 替我们解析符号链接、
// ".."、大小写与尾部斜杠，而不是自己比较路径。
// 它返回位于该处的已有关联工作树是否应被复用，并在该路径无法容纳新工作树时报错:
// 路径位于另一个仓库中、是另一个工作树的子目录，或是我们当前正在运行的工作树。
// 检测为尽力而为(best-effort): 若 git 无法解析当前或目标工作树
// (例如路径尚不存在)，则 reuse 为 false，交由 git worktree add 处理该路径。
func resolveWorktreeTarget(client *git.Client, path string) (reuseWorktree bool, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}

	// git 每个 flag 输出一行，因此这里预期正好两行。
	current, ok := revParseFacts(client, "", "--show-toplevel", "--git-common-dir")
	if !ok || len(current) != 2 {
		return false, nil
	}
	currentToplevel, currentCommonDir := current[0], current[1]

	// 不存在或非 git 的目标会在此失败: 它是用于新工作树(worktree)的全新路径。
	target, ok := revParseFacts(client, abs, "--show-toplevel", "--show-prefix", "--git-common-dir")
	if !ok || len(target) != 3 {
		return false, nil
	}
	targetToplevel, targetPrefix, targetCommonDir := target[0], target[1], target[2]

	switch {
	case targetCommonDir != currentCommonDir:
		return false, fmt.Errorf("--worktree 路径位于另一个仓库内")
	case targetToplevel == currentToplevel:
		return false, fmt.Errorf("--worktree 路径指向您当前所在的仓库; 省略 --worktree 即可在本地检出(check out)")
	case targetPrefix != "":
		return false, fmt.Errorf("--worktree 路径位于某个已有的工作树(worktree)内")
	}
	return true, nil
}

func revParseFacts(client *git.Client, dir string, flags ...string) (fields []string, ok bool) {
	args := append([]string{"rev-parse", "--path-format=absolute"}, flags...)
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd, err := client.Command(context.Background(), args...)
	if err != nil {
		return nil, false
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), true
}

func ensureWorktreePathSafe(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("--worktree 路径不能是符号链接(symlink): %s", path)
	case !fi.IsDir():
		return fmt.Errorf("--worktree 路径必须是目录: %s", path)
	}
	return nil
}

func ensureWorktreePathEmpty(path string) error {
	dir, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()

	_, err = dir.Readdirnames(1)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return fmt.Errorf("--worktree 路径必须为空: %s", path)
	}
}
