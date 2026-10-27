package update

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/installer"
	"github.com/cli/cli/v2/internal/skills/registry"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// UpdateOptions 保存 update 命令的所有依赖项与用户提供的标志。
type UpdateOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	Prompter   prompter.Prompter
	GitClient  *git.Client

	Skills []string
	All    bool
	Force  bool
	DryRun bool
	Unpin  bool
	Dir    string
}

// installedSkill 表示从 SKILL.md frontmatter 解析的本地已安装技能。
type installedSkill struct {
	name        string
	repoHost    string
	owner       string
	repo        string
	treeSHA     string // 安装时的 tree SHA
	pinned      string // 显式固定值 (空 = 未固定)
	sourcePath  string // 源仓库中的原始路径 (如 "skills/author/name")
	dir         string // 本地目录路径
	host        *registry.AgentHost
	scope       registry.Scope
	metadataErr error
}

// pendingUpdate 描述一个有可用更新的技能。
type pendingUpdate struct {
	local    installedSkill
	newSHA   string // new tree SHA from remote
	resolved *discovery.ResolvedRef
	skill    discovery.Skill
}

// NewCmdUpdate 创建 "skills update" 命令。
func NewCmdUpdate(f *cmdutil.Factory, runF func(*UpdateOptions) error) *cobra.Command {
	opts := &UpdateOptions{
		IO:         f.IOStreams,
		Prompter:   f.Prompter,
		Config:     f.Config,
		GitClient:  f.GitClient,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "update [<skill>...] [flags]",
		Short: "将已安装的技能更新到最新版本 (预览版)",
		Long: heredoc.Docf(`
			通过比较本地 tree SHA (来自 %[1]sSKILL.md%[1]s frontmatter) 与远程仓库，
			检查已安装的技能是否有可用更新。

			自动扫描项目作用域与用户作用域下所有已知的 agent 宿主目录
			(包括 Copilot、Claude、Cursor、Gemini、Antigravity、Grok 等)。

			不带参数时检查所有已安装的技能。提供技能名时只检查这些技能。

			固定的技能 (使用 %[1]s--pin%[1]s 安装) 会被跳过并给出提示。
			使用 %[1]s--unpin%[1]s 清除固定版本，将这些技能纳入更新。

			缺少 GitHub 元数据的技能 (如手动安装或由其他工具安装) 在交互
			模式下会提示其来源仓库。使用 %[1]s--all%[1]s 或非交互模式时，
			会跳过并给出提示。更新会重新下载技能并注入元数据，
			因此后续更新可自动进行。

			使用 %[1]s--force%[1]s 时，即使远程版本与本地 tree SHA 一致也会重新下载。
			这会用原始内容覆盖本地修改过的技能文件，但不会删除本地新增的额外文件。

			交互模式下，会显示哪些技能有更新并在继续前请求确认。
			使用 %[1]s--all%[1]s 无需提示直接更新。
			使用 %[1]s--dry-run%[1]s 仅报告可用更新，不修改任何文件。
		`, "`"),
		Example: heredoc.Doc(`
			# 交互式检查并更新所有技能
			$ gh skill update

			# 更新指定技能
			$ gh skill update mcp-cli git-commit

			# 更新所有技能且不提示
			$ gh skill update --all

			# 重新下载所有技能 (恢复本地修改的文件)
			$ gh skill update --force --all

			# 只检查更新不应用 (只读)
			$ gh skill update --dry-run

			# 解除固定并更新到最新
			$ gh skill update --unpin
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Skills = args
			if runF != nil {
				return runF(opts)
			}
			return updateRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.All, "all", false, "更新所有技能且不提示")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "即使已是最新也重新下载")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "报告可用更新但不修改文件")
	cmd.Flags().BoolVar(&opts.Unpin, "unpin", false, "清除固定版本并将固定的技能纳入更新")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "扫描自定义目录中的已安装技能")

	return cmd
}

func updateRun(opts *UpdateOptions) error {
	cs := opts.IO.ColorScheme()
	canPrompt := opts.IO.CanPrompt()

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	gitRoot := installer.ResolveGitRoot(opts.GitClient)
	homeDir := installer.ResolveHomeDir()

	// Scan for installed skills
	var installed []installedSkill
	if opts.Dir != "" {
		skills, scanErr := scanInstalledSkills(opts.Dir, nil, "")
		if scanErr != nil {
			return fmt.Errorf("无法扫描目录: %w", scanErr)
		}
		installed = skills
	} else {
		installed = scanAllAgents(gitRoot, homeDir)
	}

	if len(installed) == 0 {
		fmt.Fprintf(opts.IO.ErrOut, "未找到已安装的技能。\n")
		return nil
	}

	// Filter to requested skills if specified
	if len(opts.Skills) > 0 {
		requested := make(map[string]bool, len(opts.Skills))
		for _, name := range opts.Skills {
			requested[name] = true
		}
		var filtered []installedSkill
		for _, s := range installed {
			if requested[s.name] {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("指定的技能均未安装")
		}
		installed = filtered
	}

	// Skip skills with invalid metadata rather than aborting the entire
	// update run. One corrupt skill should not prevent updating others.
	{
		var valid []installedSkill
		for _, s := range installed {
			if s.metadataErr != nil {
				fmt.Fprintf(opts.IO.ErrOut, "%s 跳过 %s: 仓库元数据无效: %s\n", cs.WarningIcon(), s.name, s.metadataErr)
				continue
			}
			valid = append(valid, s)
		}
		installed = valid
	}

	if len(installed) == 0 {
		fmt.Fprintf(opts.IO.ErrOut, "没有可更新的技能。\n")
		return nil
	}

	// Prompt for metadata on skills missing it (before starting progress indicator)
	// 为缺少元数据的技能提示补充 (在启动进度指示器之前)
	var noMeta []string
	// Track skills where the user provided a source repo interactively.
	// Keyed by directory path to avoid collisions when the same skill name
	// is installed across multiple hosts or scopes.
	// 记录用户在交互中提供来源仓库的技能。
	// 以目录路径为键，避免同一技能名安装在多个宿主或作用域时冲突。
	type promptedEntry struct {
		name   string
		source string // "owner/repo"
	}
	prompted := make(map[string]promptedEntry) // dir > entry
	for i := range installed {
		s := &installed[i]
		if s.owner != "" && s.repo != "" {
			continue
		}
		if !canPrompt || opts.All {
			noMeta = append(noMeta, s.name)
			continue
		}
		fmt.Fprintf(opts.IO.ErrOut, "%s %s 缺少 GitHub 元数据\n", cs.WarningIcon(), s.name)
		owner, repo, reason, ok, promptErr := promptForSkillOrigin(opts.Prompter, s.name)
		if promptErr != nil {
			return promptErr
		}
		if !ok {
			if reason != "" {
				fmt.Fprintf(opts.IO.ErrOut, "  %s %s\n", cs.WarningIcon(), reason)
			}
			fmt.Fprintf(opts.IO.ErrOut, "  跳过 %s\n", s.name)
			continue
		}
		s.owner = owner
		s.repo = repo
		s.repoHost = source.SupportedHost
		prompted[s.dir] = promptedEntry{name: s.name, source: owner + "/" + repo}
	}

	opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("Checking %d installed skill(s) for updates", len(installed)))

	var updates []pendingUpdate
	var pinned []installedSkill

	type repoKey struct{ host, owner, repo string }
	repoSkills := make(map[repoKey][]discovery.Skill)
	repoRefs := make(map[repoKey]*discovery.ResolvedRef)
	repoErrors := make(map[repoKey]bool)

	for _, s := range installed {
		if s.owner == "" || s.repo == "" {
			continue
		}
		if s.pinned != "" && !opts.Unpin {
			pinned = append(pinned, s)
			continue
		}

		key := repoKey{s.repoHost, s.owner, s.repo}

		if repoErrors[key] {
			continue
		}

		// Resolve ref and discover skills once per repo
		if _, ok := repoRefs[key]; !ok {
			resolved, resolveErr := discovery.ResolveRef(apiClient, s.repoHost, s.owner, s.repo, "")
			if resolveErr != nil {
				repoErrors[key] = true
				opts.IO.StopProgressIndicator()
				fmt.Fprintf(opts.IO.ErrOut, "%s 跳过 %s: 无法解析 %s/%s: %v\n", cs.WarningIcon(), s.name, s.owner, s.repo, resolveErr)
				opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在检查 %d 个已安装技能的更新", len(installed)))
				continue
			}
			repoRefs[key] = resolved

			skills, discoverErr := discovery.DiscoverSkills(apiClient, s.repoHost, s.owner, s.repo, resolved.SHA)
			if discoverErr != nil {
				repoErrors[key] = true
				opts.IO.StopProgressIndicator()
				fmt.Fprintf(opts.IO.ErrOut, "%s 跳过 %s: %v\n", cs.WarningIcon(), s.name, discoverErr)
				opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在检查 %d 个已安装技能的更新", len(installed)))
				continue
			}
			repoSkills[key] = skills
		}

		resolved := repoRefs[key]
		for _, remote := range repoSkills[key] {
			matched := false
			if s.sourcePath != "" {
				matched = remote.Path == s.sourcePath
			} else {
				matched = remote.InstallName() == s.name
			}
			if matched && (remote.TreeSHA != s.treeSHA || opts.Force) {
				updates = append(updates, pendingUpdate{
					local:    s,
					newSHA:   remote.TreeSHA,
					resolved: resolved,
					skill:    remote,
				})
				break
			}
		}
	}

	opts.IO.StopProgressIndicator()

	// Warn about prompted skills that weren't found in the remote repo
	for _, entry := range prompted {
		parts := strings.SplitN(entry.source, "/", 2)
		key := repoKey{source.SupportedHost, parts[0], parts[1]}
		skills, resolved := repoSkills[key]
		if !resolved {
			continue
		}
		found := false
		for _, remote := range skills {
			if remote.InstallName() == entry.name || remote.Name == entry.name {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(opts.IO.ErrOut, "%s 在 %s 中未找到技能 %s\n", cs.WarningIcon(), entry.source, entry.name)
		}
	}

	for _, s := range pinned {
		fmt.Fprintf(opts.IO.ErrOut, "%s %s 已固定到 %s (跳过)\n", cs.Muted("⊘"), s.name, s.pinned)
	}
	for _, name := range noMeta {
		fmt.Fprintf(opts.IO.ErrOut, "%s %s 缺少 GitHub 元数据。交互式运行 `gh skill update %s` 以补充元数据，或重新安装以启用更新\n", cs.WarningIcon(), name, name)
	}

	if len(updates) == 0 {
		if opts.Force && opts.DryRun {
			fmt.Fprintf(opts.IO.ErrOut, "所有技能均已最新。去掉 --dry-run 并配合 --force 可强制重新下载。\n")
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "所有技能均已最新。\n")
		}
		return nil
	}

	fmt.Fprintf(opts.IO.ErrOut, "\n有 %d 个可用更新:\n", len(updates))
	for _, u := range updates {
		if u.local.treeSHA == u.newSHA {
			fmt.Fprintf(opts.IO.Out, "  %s %s (%s/%s) %s (重新安装) [%s]\n",
				cs.Cyan("•"), u.local.name, u.local.owner, u.local.repo,
				git.ShortSHA(u.newSHA), discovery.ShortRef(u.resolved.Ref))
		} else {
			fmt.Fprintf(opts.IO.Out, "  %s %s (%s/%s) %s > %s [%s]\n",
				cs.Cyan("•"), u.local.name, u.local.owner, u.local.repo,
				cs.Muted(git.ShortSHA(u.local.treeSHA)), git.ShortSHA(u.newSHA),
				discovery.ShortRef(u.resolved.Ref))
		}
	}
	fmt.Fprintln(opts.IO.ErrOut)

	if opts.DryRun {
		return nil
	}

	if !opts.All {
		if !canPrompt {
			return fmt.Errorf("有可用更新；使用 --all 重新运行以应用，或交互运行以确认")
		}
		confirmed, confirmErr := opts.Prompter.Confirm(fmt.Sprintf("更新 %d 个技能？", len(updates)), true)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Fprintf(opts.IO.ErrOut, "更新已取消。\n")
			return cmdutil.CancelError
		}
	}

	var failed bool
	for _, u := range updates {
		if err := updateSkillInPlace(opts, u, apiClient, gitRoot, homeDir); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "%s 更新 %s 失败: %v\n", cs.FailureIcon(), u.local.name, err)
			failed = true
			continue
		}
		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.Out, "%s 已更新 %s\n", cs.SuccessIcon(), u.local.name)
		} else {
			fmt.Fprintf(opts.IO.Out, "已更新 %s\n", u.local.name)
		}
	}

	if failed {
		return cmdutil.SilentError
	}

	return nil
}

// updateSkillInPlace 将解析出的更新安装到现有技能目录旁边的暂存目录中，
// 成功后通过同文件系统重命名将暂存内容原子性地交换到位。这可以保证:
//
//   - 技能目录自身的 inode 保持不变，因此指向它的符号链接、挂载和
//     外部引用仍然有效。
//   - 上一版本的过期文件会被移除。
//   - 任何一步失败 (安装、读取、重命名) 都不会影响现有技能:
//     现有文件会先移入备份目录，后续步骤失败时再恢复。
func updateSkillInPlace(opts *UpdateOptions, u pendingUpdate, apiClient *api.Client, gitRoot, homeDir string) error {
	if u.local.dir == "" {
		return fmt.Errorf("无法更新 %s: 未记录安装位置", u.local.name)
	}

	parent := filepath.Dir(u.local.dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("无法确保父目录 %s 存在: %w", parent, err)
	}

	// Stage as a sibling of the existing skill directory so the swap stays
	// on the same filesystem and every rename is atomic.
	staging, err := os.MkdirTemp(parent, "."+u.skill.Name+".gh-skill-update-")
	if err != nil {
		return fmt.Errorf("无法创建暂存目录: %w", err)
	}
	defer os.RemoveAll(staging)

	installOpts := &installer.Options{
		Host:    u.local.repoHost,
		Owner:   u.local.owner,
		Repo:    u.local.repo,
		Ref:     u.resolved.Ref,
		SHA:     u.resolved.SHA,
		Skills:  []discovery.Skill{u.skill},
		Dir:     staging,
		GitRoot: gitRoot,
		HomeDir: homeDir,
		Client:  apiClient,
	}
	if _, err := installer.Install(installOpts); err != nil {
		return err
	}

	stagedSkillDir := filepath.Join(staging, u.skill.Name)
	if _, err := os.Stat(stagedSkillDir); err != nil {
		return fmt.Errorf("installer did not produce %s: %w", stagedSkillDir, err)
	}

	if err := os.MkdirAll(u.local.dir, 0o755); err != nil {
		return fmt.Errorf("无法确保技能目录 %s: %w", u.local.dir, err)
	}

	return swapDirectoryContents(u.local.dir, stagedSkillDir)
}

// swapDirectoryContents 用 src 中的条目替换 dest 中的条目，保留 dest 的 inode。
// 它先将所有现有条目移入同级备份目录，再将暂存条目移入 dest。
// 任何一步失败时，会从备份恢复原始内容。
//
// src 与 dest 必须位于同一文件系统，以保证重命名是原子操作。
func swapDirectoryContents(dest, src string) error {
	backup, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".gh-skill-backup-")
	if err != nil {
		return fmt.Errorf("无法创建备份目录: %w", err)
	}

	existing, err := os.ReadDir(dest)
	if err != nil {
		_ = os.RemoveAll(backup)
		return fmt.Errorf("无法读取技能目录 %s: %w", dest, err)
	}
	var movedOut []string
	for _, entry := range existing {
		if err := os.Rename(filepath.Join(dest, entry.Name()), filepath.Join(backup, entry.Name())); err != nil {
			restoreBackup(dest, backup, movedOut, nil)
			return fmt.Errorf("无法移开 %s: %w", entry.Name(), err)
		}
		movedOut = append(movedOut, entry.Name())
	}

	staged, err := os.ReadDir(src)
	if err != nil {
		restoreBackup(dest, backup, movedOut, nil)
		return fmt.Errorf("无法读取暂存的技能目录 %s: %w", src, err)
	}
	var movedIn []string
	for _, entry := range staged {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dest, entry.Name())
		if err := os.Rename(from, to); err != nil {
			restoreBackup(dest, backup, movedOut, movedIn)
			return fmt.Errorf("无法将 %s 移入到位: %w", entry.Name(), err)
		}
		movedIn = append(movedIn, entry.Name())
	}

	_ = os.RemoveAll(backup)
	return nil
}

// restoreBackup 撤销部分完成的交换: 删除新装入的条目，
// 并将备份中的原始条目移回 dest。
func restoreBackup(dest, backup string, movedOut, movedIn []string) {
	for _, name := range movedIn {
		_ = os.RemoveAll(filepath.Join(dest, name))
	}
	for _, name := range movedOut {
		_ = os.Rename(filepath.Join(backup, name), filepath.Join(dest, name))
	}
	_ = os.RemoveAll(backup)
}

// scanAllAgents 遍历每个已注册 agent 的技能目录 (项目 + 用户作用域)，
// 收集已安装的技能。共享的安装根目录只扫描一次。
func scanAllAgents(gitRoot, homeDir string) []installedSkill {
	scannedDirs := make(map[string]bool)
	var all []installedSkill

	for i := range registry.Agents {
		host := &registry.Agents[i]
		for _, scope := range []registry.Scope{registry.ScopeProject, registry.ScopeUser} {
			dir, err := host.InstallDir(scope, gitRoot, homeDir)
			if err != nil {
				continue
			}
			if scannedDirs[dir] {
				continue
			}
			scannedDirs[dir] = true
			skills, err := scanInstalledSkills(dir, host, scope)
			if err != nil {
				continue
			}
			all = append(all, skills...)
		}
	}

	return all
}

// scanInstalledSkills 读取技能目录中的所有 SKILL.md 文件，
// 并从其 frontmatter 提取 GitHub 元数据。它同时处理扁平布局
// ({dir}/{name}/SKILL.md) 和命名空间布局
// ({dir}/{namespace}/{name}/SKILL.md)。
func scanInstalledSkills(skillsDir string, host *registry.AgentHost, scope registry.Scope) ([]installedSkill, error) {
	entries, err := os.ReadDir(skillsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取技能目录: %w", err)
	}

	var skills []installedSkill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		// Flat layout: {dir}/{name}/SKILL.md
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if data, readErr := os.ReadFile(skillFile); readErr == nil {
			if s, ok := parseInstalledSkill(data, e.Name(), filepath.Join(skillsDir, e.Name()), host, scope); ok {
				skills = append(skills, s)
				continue
			}
		}

		// Namespaced layout: {dir}/{namespace}/{name}/SKILL.md
		subEntries, subErr := os.ReadDir(filepath.Join(skillsDir, e.Name()))
		if subErr != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subSkillFile := filepath.Join(skillsDir, e.Name(), sub.Name(), "SKILL.md")
			if data, readErr := os.ReadFile(subSkillFile); readErr == nil {
				installName := e.Name() + "/" + sub.Name()
				if s, ok := parseInstalledSkill(data, installName, filepath.Join(skillsDir, e.Name(), sub.Name()), host, scope); ok {
					skills = append(skills, s)
				}
			}
		}
	}

	return skills, nil
}

// parseInstalledSkill 解析 SKILL.md 文件并返回 installedSkill。
func parseInstalledSkill(data []byte, name, dir string, host *registry.AgentHost, scope registry.Scope) (installedSkill, bool) {
	result, err := frontmatter.Parse(string(data))
	if err != nil {
		return installedSkill{
			name:        name,
			dir:         dir,
			host:        host,
			scope:       scope,
			metadataErr: fmt.Errorf("invalid SKILL.md: %w", err),
		}, true
	}

	s := installedSkill{
		name:  name,
		dir:   dir,
		host:  host,
		scope: scope,
	}

	if result.Metadata.Meta != nil {
		repoInfo, ok, repoErr := source.ParseMetadataRepo(result.Metadata.Meta)
		if repoErr != nil {
			s.metadataErr = repoErr
		} else if ok {
			if err := source.ValidateSupportedHost(repoInfo.RepoHost()); err != nil {
				s.metadataErr = err
			} else {
				s.repoHost = repoInfo.RepoHost()
				s.owner = repoInfo.RepoOwner()
				s.repo = repoInfo.RepoName()
			}
		}
		s.treeSHA, _ = result.Metadata.Meta["github-tree-sha"].(string)
		s.pinned, _ = result.Metadata.Meta["github-pinned"].(string)
		s.sourcePath, _ = result.Metadata.Meta["github-path"].(string)
	}

	return s, true
}

// promptForSkillOrigin 为缺少 GitHub 元数据的技能向用户询问来源仓库。
func promptForSkillOrigin(p prompter.Prompter, skillName string) (owner, repo, reason string, ok bool, err error) {
	input, err := p.Input(
		fmt.Sprintf("%s 的来源仓库 (owner/repo):", skillName), "")
	if err != nil {
		return "", "", "", false, err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", "", false, nil
	}
	r, err := ghrepo.FromFullName(input)
	if err != nil {
		//nolint:nilerr // intentionally converting parse error into a user-facing validation message
		return "", "", fmt.Sprintf("无效的仓库 %q: 应为 owner/repo", input), false, nil
	}
	return r.RepoOwner(), r.RepoName(), "", true, nil
}
