package list

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/installer"
	"github.com/cli/cli/v2/internal/skills/registry"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/go-gh/v2/pkg/asciisanitizer"
	"github.com/spf13/cobra"
	"golang.org/x/text/transform"
)

var skillListFields = []string{
	"skillName",
	"agentHosts",
	"scope",
	"sourceURL",
	"version",
	"pinned",
	"path",
}

const (
	agentHostPublished        = "published"
	agentHostPublishedDisplay = "n/a (published)"
	scopeCustom               = "custom"
)

type scanFilter int

const (
	scanAllSkills scanFilter = iota
	scanInstalledOnly
	scanPublishedOnly
)

type ListOptions struct {
	IO        *iostreams.IOStreams
	Telemetry ghtelemetry.EventRecorder
	GitClient *git.Client
	Exporter  cmdutil.Exporter

	Agent        string
	Scope        string
	ScopeChanged bool
	Dir          string
}

type scanTarget struct {
	dir          string
	agentHostIDs []string
	scope        string
	filter       scanFilter
}

type listedSkill struct {
	skillName    string
	agentHostIDs []string
	scope        string
	source       string
	sourceURL    string
	version      string
	pinned       bool
	path         string
}

// ExportData 实现 cmdutil.exportable，用于 --json 输出。
func (s listedSkill) ExportData(fields []string) map[string]any {
	data := map[string]any{}
	for _, f := range fields {
		switch f {
		case "skillName":
			data[f] = s.skillName
		case "agentHosts":
			data[f] = s.agentHostIDs
		case "scope":
			data[f] = s.scope
		case "sourceURL":
			data[f] = s.sourceURL
		case "version":
			data[f] = s.version
		case "pinned":
			data[f] = s.pinned
		case "path":
			data[f] = s.path
		}
	}
	return data
}

// NewCmdList 创建 "skills list" 命令。
func NewCmdList(f *cmdutil.Factory, telemetry ghtelemetry.EventRecorder, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:        f.IOStreams,
		Telemetry: telemetry,
		GitClient: f.GitClient,
	}

	cmd := &cobra.Command{
		Use:     "list [flags]",
		Short:   "列出已安装的技能 (预览版)",
		Aliases: []string{"ls"},
		Long: heredoc.Docf(`
			列出已知 agent 宿主目录中已安装的 agent 技能。

			默认扫描项目作用域与用户作用域下所有受支持的 agent 宿主。
			使用 %[1]s--agent%[1]s 扫描单个宿主，%[1]s--scope%[1]s 仅扫描项目或
			用户作用域，或使用 %[1]s--dir%[1]s 扫描自定义技能目录。

			项目作用域技能相对于当前 git 仓库根目录发现。
			用户作用域技能相对于主目录发现。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出所有已安装的技能
			$ gh skill list

			# 列出为 GitHub Copilot 安装的技能
			$ gh skill list --agent github-copilot

			# 列出用户作用域的技能
			$ gh skill list --scope user

			# 以 JSON 格式列出技能
			$ gh skill list --json skillName,sourceURL,scope,version,pinned,path
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ScopeChanged = cmd.Flags().Changed("scope")

			if err := cmdutil.MutuallyExclusive("--dir 和 --agent 不能同时使用", opts.Dir != "", opts.Agent != ""); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("--dir 和 --scope 不能同时使用", opts.Dir != "", opts.ScopeChanged); err != nil {
				return err
			}

			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}

	cmdutil.StringEnumFlag(cmd, &opts.Agent, "agent", "", "", registry.AgentIDs(), "按目标 agent 筛选")
	cmdutil.StringEnumFlag(cmd, &opts.Scope, "scope", "", "", []string{string(registry.ScopeProject), string(registry.ScopeUser)}, "按安装作用域筛选")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "扫描自定义目录中的已安装技能")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, skillListFields)

	return cmd
}

func listRun(opts *ListOptions) error {
	skills, err := listInstalledSkills(opts)
	if err != nil {
		return err
	}
	sortListedSkills(skills)
	recordListTelemetry(opts, len(skills))

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, skills)
	}

	if len(skills) == 0 {
		return cmdutil.NewNoResultsError("未找到已安装的技能")
	}

	return renderTable(opts.IO, skills)
}

func listInstalledSkills(opts *ListOptions) ([]listedSkill, error) {
	targets, err := buildScanTargets(opts)
	if err != nil {
		return nil, err
	}

	var all []listedSkill
	for _, target := range targets {
		skills, scanErr := scanInstalledSkills(target.dir, target.agentHostIDs, target.scope, target.filter)
		if scanErr != nil {
			if opts.Dir != "" {
				return nil, fmt.Errorf("无法扫描目录: %w", scanErr)
			}
			continue
		}
		all = append(all, skills...)
	}

	return all, nil
}

func buildScanTargets(opts *ListOptions) ([]scanTarget, error) {
	if opts.Dir != "" {
		dir, err := filepath.Abs(opts.Dir)
		if err != nil {
			return nil, fmt.Errorf("无法解析路径: %w", err)
		}
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("无法访问目录: %w", err)
		}
		return []scanTarget{{dir: dir, scope: scopeCustom}}, nil
	}

	gitRoot := installer.ResolveGitRoot(opts.GitClient)
	homeDir := installer.ResolveHomeDir()

	agentHosts, err := selectedAgentHosts(opts.Agent)
	if err != nil {
		return nil, err
	}
	scopes := selectedScopes(opts.Scope)

	byDir := map[string]int{}
	var targets []scanTarget
	for _, agentHost := range agentHosts {
		for _, scope := range scopes {
			dir, installErr := agentHost.InstallDir(scope, gitRoot, homeDir)
			if installErr != nil {
				continue
			}

			if idx, ok := byDir[dir]; ok {
				targets[idx].agentHostIDs = appendAgentHostID(targets[idx].agentHostIDs, agentHost.ID)
				targets[idx].filter = mergeScanFilters(targets[idx].filter, scanFilterForAgentHost(agentHost, scope))
				continue
			}

			byDir[dir] = len(targets)
			targets = append(targets, scanTarget{
				dir:          dir,
				agentHostIDs: []string{agentHost.ID},
				scope:        string(scope),
				filter:       scanFilterForAgentHost(agentHost, scope),
			})
		}
	}
	if shouldListPublishedProjectSkills(opts.Agent, scopes, gitRoot) {
		targets = append(targets, scanTarget{
			dir:          filepath.Join(gitRoot, "skills"),
			agentHostIDs: []string{agentHostPublished},
			scope:        string(registry.ScopeProject),
			filter:       scanPublishedOnly,
		})
	}

	return targets, nil
}

func selectedAgentHosts(agentID string) ([]*registry.AgentHost, error) {
	if agentID != "" {
		host, err := registry.FindByID(agentID)
		if err != nil {
			return nil, err
		}
		return []*registry.AgentHost{host}, nil
	}

	agentHosts := make([]*registry.AgentHost, len(registry.Agents))
	for i := range registry.Agents {
		agentHosts[i] = &registry.Agents[i]
	}
	return agentHosts, nil
}

func selectedScopes(scope string) []registry.Scope {
	if scope != "" {
		return []registry.Scope{registry.Scope(scope)}
	}
	return []registry.Scope{registry.ScopeProject, registry.ScopeUser}
}

func appendAgentHostID(agentHostIDs []string, agentHostID string) []string {
	if slices.Contains(agentHostIDs, agentHostID) {
		return agentHostIDs
	}
	return append(agentHostIDs, agentHostID)
}

func scanFilterForAgentHost(agentHost *registry.AgentHost, scope registry.Scope) scanFilter {
	if scope == registry.ScopeProject && agentHost.ProjectDir == "skills" {
		return scanInstalledOnly
	}
	return scanAllSkills
}

func mergeScanFilters(a, b scanFilter) scanFilter {
	if a == b {
		return a
	}
	return scanAllSkills
}

func shouldListPublishedProjectSkills(agentID string, scopes []registry.Scope, gitRoot string) bool {
	if agentID != "" || gitRoot == "" {
		return false
	}
	return slices.Contains(scopes, registry.ScopeProject)
}

func scanInstalledSkills(skillsDir string, agentHostIDs []string, scope string, filter scanFilter) ([]listedSkill, error) {
	entries, err := os.ReadDir(skillsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取技能目录: %w", err)
	}

	var skills []listedSkill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		// 扁平布局: {dir}/{name}/SKILL.md。
		skillDir := filepath.Join(skillsDir, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		// TODO: 也许应该暴露此错误，而不是静默跳过
		if data, readErr := readSkillFile(skillFile); readErr == nil {
			skill, hasInstallMetadata := parseInstalledSkill(data, e.Name(), skillDir, agentHostIDs, scope)
			if shouldIncludeSkill(filter, hasInstallMetadata) {
				skills = append(skills, skill)
			}
			continue
		}

		// 命名空间布局: {dir}/{namespace}/{name}/SKILL.md。
		subEntries, subErr := os.ReadDir(skillDir)
		if subErr != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subSkillDir := filepath.Join(skillDir, sub.Name())
			subSkillFile := filepath.Join(subSkillDir, "SKILL.md")
			if data, readErr := readSkillFile(subSkillFile); readErr == nil {
				installName := e.Name() + "/" + sub.Name()
				skill, hasInstallMetadata := parseInstalledSkill(data, installName, subSkillDir, agentHostIDs, scope)
				if shouldIncludeSkill(filter, hasInstallMetadata) {
					skills = append(skills, skill)
				}
			}
		}
	}

	return skills, nil
}

// readSkillFile 仅在 SKILL.md 解析为普通文件时读取它。
func readSkillFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("SKILL.md 不是普通文件: %s", path)
	}
	return os.ReadFile(path)
}

func shouldIncludeSkill(filter scanFilter, hasInstallMetadata bool) bool {
	switch filter {
	case scanInstalledOnly:
		return hasInstallMetadata
	case scanPublishedOnly:
		return !hasInstallMetadata
	default:
		return true
	}
}

func parseInstalledSkill(data []byte, name, dir string, agentHostIDs []string, scope string) (listedSkill, bool) {
	s := listedSkill{
		skillName:    name,
		agentHostIDs: agentHostIDs,
		scope:        scope,
		path:         dir,
	}

	result, err := frontmatter.Parse(string(data))
	if err != nil {
		return s, false
	}

	meta := result.Metadata.Meta
	if meta == nil {
		return s, false
	}
	installMetadata := hasInstallMetadata(meta)

	if sourcePath, _ := meta["github-path"].(string); sourcePath != "" {
		if skillName := skillNameFromSourcePath(sourcePath); skillName != "" {
			s.skillName = skillName
		}
	}

	if repoURL, _ := meta["github-repo"].(string); repoURL != "" {
		s.sourceURL = repoURL
		s.source = repoURL
		if repo, parseErr := source.ParseRepoURL(repoURL); parseErr == nil {
			s.source = ghrepo.FullName(repo)
			s.sourceURL = source.BuildRepoURL(repo.RepoHost(), repo.RepoOwner(), repo.RepoName())
		}
	} else if localPath, _ := meta["local-path"].(string); localPath != "" {
		s.sourceURL = localPath
		s.source = localPath
	}

	if ref, _ := meta["github-ref"].(string); ref != "" {
		s.version = discovery.ShortRef(ref)
	}
	if pinnedRef, _ := meta["github-pinned"].(string); pinnedRef != "" {
		s.pinned = true
		if s.version == "" {
			s.version = pinnedRef
		}
	}

	return s, installMetadata
}

func hasInstallMetadata(meta map[string]any) bool {
	for _, key := range []string{"github-repo", "github-ref", "github-tree-sha", "github-path", "github-pinned", "local-path"} {
		value, ok := meta[key]
		if !ok {
			continue
		}
		if str, ok := value.(string); !ok || strings.TrimSpace(str) != "" {
			return true
		}
	}
	return false
}

func skillNameFromSourcePath(sourcePath string) string {
	sourcePath = strings.TrimSuffix(sourcePath, "/SKILL.md")
	sourcePath = strings.Trim(sourcePath, "/")
	if sourcePath == "" {
		return ""
	}

	parts := strings.Split(sourcePath, "/")
	for i, part := range slices.Backward(parts) {
		if part != "skills" {
			continue
		}

		if i >= 2 && parts[i-2] == "plugins" && i+1 < len(parts) {
			return parts[i-1] + "/" + parts[len(parts)-1]
		}

		afterSkills := len(parts) - i - 1
		switch afterSkills {
		case 0:
			return ""
		case 1:
			return parts[i+1]
		default:
			return parts[i+1] + "/" + parts[len(parts)-1]
		}
	}

	return parts[len(parts)-1]
}

func sortListedSkills(skills []listedSkill) {
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].skillName != skills[j].skillName {
			return skills[i].skillName < skills[j].skillName
		}
		if skills[i].scope != skills[j].scope {
			return skills[i].scope < skills[j].scope
		}
		if formatAgentHosts(skills[i].agentHostIDs) != formatAgentHosts(skills[j].agentHostIDs) {
			return formatAgentHosts(skills[i].agentHostIDs) < formatAgentHosts(skills[j].agentHostIDs)
		}
		return skills[i].path < skills[j].path
	})
}

func renderTable(io *iostreams.IOStreams, skills []listedSkill) error {
	table := tableprinter.New(io, tableprinter.WithHeader("Name", "Agent", "Scope", "Source"))

	for _, skill := range skills {
		table.AddField(sanitizeForTerminal(skill.skillName))
		table.AddField(formatAgentHosts(skill.agentHostIDs))
		table.AddField(displayOrDash(skill.scope))
		table.AddField(displayOrDash(sanitizeForTerminal(skill.source)))
		table.EndRow()
	}

	return table.Render()
}

// sanitizeForTerminal 将 s 中的 ASCII 控制字符替换为无害的插入符式
// 替代符号，防止 frontmatter 值注入终端转义序列。
func sanitizeForTerminal(s string) string {
	var buf bytes.Buffer
	r := transform.NewReader(bytes.NewReader([]byte(s)), &asciisanitizer.Sanitizer{})
	if _, err := io.Copy(&buf, r); err != nil {
		return "Unknown"
	}
	return buf.String()
}

func displayOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatAgentHosts(agentHostIDs []string) string {
	if len(agentHostIDs) == 0 {
		return "-"
	}
	if len(agentHostIDs) == 1 && agentHostIDs[0] == agentHostPublished {
		return agentHostPublishedDisplay
	}
	return strings.Join(agentHostIDs, ", ")
}

func recordListTelemetry(opts *ListOptions, skillCount int) {
	if opts.Telemetry == nil {
		return
	}

	agentHosts := opts.Agent
	if agentHosts == "" {
		agentHosts = "all"
	}
	scope := opts.Scope
	if scope == "" {
		scope = "all"
	}
	customDir := "false"
	if opts.Dir != "" {
		customDir = "true"
		scope = scopeCustom
	}
	format := "table"
	if opts.Exporter != nil {
		format = "json"
	}

	opts.Telemetry.Record(ghtelemetry.Event{
		Type: "skill_list",
		Dimensions: ghtelemetry.Dimensions{
			"agent_hosts": agentHosts,
			"custom_dir":  customDir,
			"format":      format,
			"scope":       scope,
		},
		Measures: ghtelemetry.Measures{
			"skill_count": int64(skillCount),
		},
	})
}
