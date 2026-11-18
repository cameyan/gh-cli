package preview

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

type PreviewOptions struct {
	IO             *iostreams.IOStreams
	Telemetry      ghtelemetry.EventRecorder
	HttpClient     func() (*http.Client, error)
	Prompter       prompter.Prompter
	ExecutablePath string
	RenderFile     func(string, string) string

	RepoArg         string
	SkillName       string
	Version         string // 从 SkillName 的 @ 后缀解析
	AllowHiddenDirs bool   // 包含点前缀目录中的技能

	repo ghrepo.Interface
}

// NewCmdPreview 创建 "skills preview" 命令。
func NewCmdPreview(f *cmdutil.Factory, telemetry ghtelemetry.EventRecorder, runF func(*PreviewOptions) error) *cobra.Command {
	opts := &PreviewOptions{
		IO:             f.IOStreams,
		Telemetry:      telemetry,
		HttpClient:     f.HttpClient,
		Prompter:       f.Prompter,
		ExecutablePath: f.ExecutablePath,
	}
	opts.RenderFile = func(filePath, content string) string {
		return renderMarkdownPreview(opts.IO, filePath, content)
	}

	cmd := &cobra.Command{
		Use:   "preview <repository> [<skill>]",
		Short: "从 GitHub 仓库预览技能 (预览版)",
		Long: heredoc.Docf(`
			在终端中渲染技能的 %[1]sSKILL.md%[1]s 内容。该命令会从仓库获取
			技能文件，并使用配置的分页器显示，不会安装任何内容。

			先显示文件树，随后显示渲染后的 %[1]sSKILL.md%[1]s 内容。
			交互运行且技能包含其他文件 (脚本、参考资料等) 时，
			会提供文件选择器，可逐个浏览这些文件。

			仅提供仓库参数运行时，会列出可用技能并提示选择。

			技能参数可以是名称、带命名空间的名称 (%[1]sauthor/skill%[1]s)，
			或仓库内的精确路径 (%[1]sskills/author/skill%[1]s、
			%[1]spackages/agent-skills/code-review%[1]s，或任何 %[1]s.../SKILL.md%[1]s 路径)。
			带一个斜杠的命名空间名称按名称匹配。使用 %[1]sSKILL.md%[1]s
			后缀可强制匹配标准约定之外的单层目录路径。

			要预览技能的特定版本，可在技能名后追加 %[1]s@VERSION%[1]s。
			版本将解析为 git 标签、分支或 commit SHA。
		`, "`"),
		Example: heredoc.Doc(`
			# 预览指定技能
			$ gh skill preview github/awesome-copilot documentation-writer

			# 预览特定版本的技能
			$ gh skill preview github/awesome-copilot documentation-writer@v1.2.0

			# 预览特定 commit SHA 的技能
			$ gh skill preview github/awesome-copilot documentation-writer@abc123def456

			# 从非标准嵌套路径预览 (高效，跳过完整发现)
			$ gh skill preview monalisa/skills-repo packages/agent-skills/code-review

			# 交互式浏览和预览
			$ gh skill preview github/awesome-copilot
		`),
		Aliases: []string{"show"},
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			opts.RepoArg = args[0]
			if len(args) == 2 {
				opts.SkillName = args[1]
			}

			if i := strings.LastIndex(opts.SkillName, "@"); i > 0 {
				opts.Version = opts.SkillName[i+1:]
				opts.SkillName = opts.SkillName[:i]
			}

			repo, err := ghrepo.FromFullName(opts.RepoArg)
			if err != nil {
				return err
			}
			opts.repo = repo

			if runF != nil {
				return runF(opts)
			}
			return previewRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.AllowHiddenDirs, "allow-hidden-dirs", false, "包含隐藏目录中的技能 (如 .claude/skills/、.agents/skills/)")

	return cmd
}

func previewRun(opts *PreviewOptions) error {
	cs := opts.IO.ColorScheme()

	repo := opts.repo
	owner := repo.RepoOwner()
	repoName := repo.RepoName()
	hostname := repo.RepoHost()
	if err := source.ValidateSupportedHost(hostname); err != nil {
		return err
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	// Kick off the visibility fetch in parallel with the preview work so
	// the extra API roundtrip doesn't add latency on the critical path.
	// The result is consumed when the telemetry event is emitted below.
	// 将仓库可见性查询与预览工作并行发起，避免额外的 API 往返
	// 增加关键路径上的延迟。结果在下方发出遥测事件时消费。
	type visResult struct {
		vis discovery.RepoVisibility
		err error
	}
	visCh := make(chan visResult, 1)
	go func() {
		vis, err := discovery.FetchRepoVisibility(apiClient, hostname, owner, repoName)
		visCh <- visResult{vis: vis, err: err}
	}()

	opts.IO.StartProgressIndicatorWithLabel(fmt.Sprintf("正在解析 %s/%s", owner, repoName))
	resolved, err := discovery.ResolveRef(apiClient, hostname, owner, repoName, opts.Version)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return fmt.Errorf("无法解析版本: %w", err)
	}

	var skill discovery.Skill
	if discovery.IsSkillPath(opts.SkillName) {
		opts.IO.StartProgressIndicatorWithLabel("正在查找技能")
		found, err := discovery.DiscoverSkillByPathWithOptions(apiClient, hostname, owner, repoName, resolved.SHA, opts.SkillName, discovery.DiscoverSkillByPathOptions{SkipDescription: true})
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}
		skill = *found
	} else {
		opts.IO.StartProgressIndicatorWithLabel("正在发现技能")
		allSkills, err := discovery.DiscoverSkillsWithOptions(apiClient, hostname, owner, repoName, resolved.SHA, discovery.DiscoverOptions{})
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}

		skills, err := filterHiddenDirSkills(opts, allSkills)
		if err != nil {
			return err
		}

		sort.Slice(skills, func(i, j int) bool {
			return skills[i].DisplayName() < skills[j].DisplayName()
		})

		skill, err = selectSkill(opts, skills)
		if err != nil {
			return err
		}
	}

	opts.IO.StartProgressIndicatorWithLabel("正在获取技能内容")
	var files []discovery.SkillFile
	if skill.TreeSHA != "" {
		files, err = discovery.ListSkillFiles(apiClient, hostname, owner, repoName, skill.TreeSHA)
		if err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "警告: 无法列出技能文件: %v\n", err)
			files = nil
		}
	}
	content, err := discovery.FetchBlob(apiClient, hostname, owner, repoName, skill.BlobSHA)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	rendered := opts.renderFile("SKILL.md", content.String())

	// Collect extra files (everything that isn't SKILL.md)
	var extraFiles []discovery.SkillFile
	for _, f := range files {
		if f.Path != "SKILL.md" {
			extraFiles = append(extraFiles, f)
		}
	}

	canPrompt := opts.IO.CanPrompt()

	// Non-interactive or skill has only SKILL.md: dump through pager
	if !canPrompt || len(extraFiles) == 0 {
		renderAllFiles(opts, cs, skill, files, rendered, extraFiles, apiClient, hostname, owner, repoName)
	} else {
		// Interactive with multiple files: show tree, then file picker
		renderInteractive(opts, cs, skill, files, rendered, extraFiles, apiClient, hostname, owner, repoName)
	}

	dims := map[string]string{
		"skill_host_type": ghinstance.CategorizeHost(opts.repo.RepoHost()),
	}
	select {
	case r := <-visCh:
		if r.err == nil {
			dims["repo_visibility"] = string(r.vis)
			if r.vis == discovery.RepoVisibilityPublic {
				dims["skill_owner"] = opts.repo.RepoOwner()
				dims["skill_repo"] = opts.repo.RepoName()
				dims["skill_name"] = skill.DisplayName()
			}
		} else {
			dims["repo_visibility"] = "unknown"
		}
	case <-time.After(visibilityWaitTimeout):
		dims["repo_visibility"] = "unknown"
	}
	opts.Telemetry.Record(ghtelemetry.Event{
		Type:       "skill_preview",
		Dimensions: dims,
	})

	return nil
}

// visibilityWaitTimeout 是在发出遥测事件时等待进行中的仓库可见性查询
// 的最长时间，超时则放弃并以 repo_visibility="unknown" 发出。到这一步时
// 命令已完成了多次串行 API 调用和渲染工作，该查询几乎总是已完成；
// 此预算仅为该单个 REST 调用停滞的情况提供一个短暂的安全兜底。
const visibilityWaitTimeout = 200 * time.Millisecond

// renderAllFiles 将文件树、SKILL.md 和所有其他文件输出到分页器。
func renderAllFiles(opts *PreviewOptions, cs *iostreams.ColorScheme, skill discovery.Skill,
	files []discovery.SkillFile, rendered string, extraFiles []discovery.SkillFile,
	apiClient *api.Client, hostname, owner, repo string) {

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	out := opts.IO.Out

	if len(files) > 0 {
		fmt.Fprintf(out, "%s\n", cs.Bold(skill.DisplayName()+"/"))
		renderFileTree(out, cs, files)
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "%s\n\n", cs.Bold("── SKILL.md ──"))
	fmt.Fprint(out, rendered)

	const maxFiles = 20
	const maxTotalBytes = 512 * 1024
	fetched := 0
	totalBytes := 0
	for _, f := range extraFiles {
		if fetched >= maxFiles {
			fmt.Fprintf(out, "\n%s\n", cs.Muted(fmt.Sprintf("(已跳过其余文件，仅显示前 %d 个)", maxFiles)))
			break
		}
		if totalBytes+f.Size > maxTotalBytes {
			fmt.Fprintf(out, "\n%s\n", cs.Muted("(已跳过其余文件，达到大小上限)"))
			break
		}
		fileContent, fetchErr := discovery.FetchBlob(apiClient, hostname, owner, repo, f.SHA)
		if fetchErr != nil {
			fmt.Fprintf(out, "\n%s\n\n%s\n", cs.Bold("── "+f.Path+" ──"), cs.Muted("(无法获取文件)"))
			continue
		}
		fetched++
		sanitized := fileContent.String()
		totalBytes += len(sanitized)
		fmt.Fprintf(out, "\n%s\n\n", cs.Bold("── "+f.Path+" ──"))
		fmt.Fprint(out, sanitized)
		if !strings.HasSuffix(sanitized, "\n") {
			fmt.Fprintln(out)
		}
	}
}

// renderInteractive 显示文件树，然后提供选择器逐个浏览文件。
func renderInteractive(opts *PreviewOptions, cs *iostreams.ColorScheme, skill discovery.Skill,
	files []discovery.SkillFile, renderedSkillMD string, extraFiles []discovery.SkillFile,
	apiClient *api.Client, hostname, owner, repo string) {

	// Show the file tree to stderr so it persists above the prompt
	// 将文件树输出到 stderr，使其保留在提示符上方
	fmt.Fprintf(opts.IO.ErrOut, "\n%s\n", cs.Bold(skill.DisplayName()+"/"))
	renderFileTree(opts.IO.ErrOut, cs, files)
	fmt.Fprintln(opts.IO.ErrOut)

	// Build choices: SKILL.md first, then extra files
	// 构建选项: SKILL.md 在前，其余文件在后
	choices := make([]string, 0, len(extraFiles)+1)
	choices = append(choices, "SKILL.md")
	for _, f := range extraFiles {
		choices = append(choices, f.Path)
	}

	// Save original stdout. StopPager closes IO.Out, so we need to
	// restore a working writer before each StartPager call.
	// 保存原始 stdout。StopPager 会关闭 IO.Out，
	// 因此每次 StartPager 前都需要恢复一个可用的写入器。
	originalOut := opts.IO.Out

	for {
		// Restore original Out before each pager cycle. StartPager replaces
		// IO.Out with a pipe; StopPager closes that pipe but does not
		// restore the original. The original writer remains valid.
		// 每次分页器周期前恢复原始 Out。StartPager 会将 IO.Out 替换为管道；
		// StopPager 关闭该管道但不恢复原始值。原始写入器仍然有效。
		opts.IO.Out = originalOut

		idx, err := opts.Prompter.Select("查看文件 (按 Esc 退出):", "", choices)
		if err != nil {
			return // Prompter returns error on Esc/Ctrl-C; treat as graceful exit
		}

		var content string

		if idx == 0 {
			content = renderedSkillMD
		} else {
			selectedFile := extraFiles[idx-1]

			// Fetch on demand; don't hold blob data in memory
			// 按需获取；不在内存中保留 blob 数据
			fileContent, fetchErr := discovery.FetchBlob(apiClient, hostname, owner, repo, selectedFile.SHA)
			if fetchErr != nil {
				fmt.Fprintf(opts.IO.ErrOut, "%s 无法获取 %s: %v\n", cs.Red("!"), selectedFile.Path, fetchErr)
				continue
			}
			content = renderSelectedFilePreview(opts, selectedFile.Path, fileContent.String())
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
		}

		if err := opts.IO.StartPager(); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
		}
		fmt.Fprint(opts.IO.Out, content)
		opts.IO.StopPager()
	}
}

func (opts *PreviewOptions) renderFile(filePath, content string) string {
	if opts.RenderFile != nil {
		return opts.RenderFile(filePath, content)
	}

	return renderMarkdownPreview(opts.IO, filePath, content)
}

func renderSelectedFilePreview(opts *PreviewOptions, filePath, content string) string {
	if !isMarkdownFile(filePath) {
		return content
	}

	return opts.renderFile(filePath, content)
}

func renderMarkdownPreview(io *iostreams.IOStreams, filePath, content string) string {
	if filePath == "SKILL.md" {
		parsed, err := frontmatter.Parse(content)
		if err == nil {
			content = parsed.Body
		}
	}

	rendered, err := markdown.Render(content,
		markdown.WithTheme(io.TerminalTheme()),
		markdown.WithWrap(io.TerminalWidth()),
		markdown.WithoutIndentation())
	if err != nil {
		return content
	}

	return rendered
}

func isMarkdownFile(filePath string) bool {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".md", ".markdown", ".mdown", ".mkd", ".mkdn":
		return true
	default:
		return false
	}
}

// filterHiddenDirSkills 应用 --allow-hidden-dirs 标志的逻辑。设置该标志时，
// 返回所有技能并给出警告；否则以提示或错误排除隐藏目录中的技能。
func filterHiddenDirSkills(opts *PreviewOptions, allSkills []discovery.Skill) ([]discovery.Skill, error) {
	cs := opts.IO.ColorScheme()

	if opts.AllowHiddenDirs {
		if discovery.HasHiddenDirSkills(allSkills) {
			fmt.Fprint(opts.IO.ErrOut, heredoc.Docf(`
				%[1]s 隐藏目录 (如 .claude/、.agents/) 中的技能可能是从其他发布者
				  复制的副本。请核实技能的来源并检查是否存在权威来源。
			`, cs.WarningIcon()))
		}
		return allSkills, nil
	}

	r := discovery.PartitionHiddenDirSkills(allSkills)
	if r.HiddenCount > 0 {
		if len(r.Standard) == 0 {
			return nil, fmt.Errorf(
				"未找到标准技能，但存在 %d 个位于隐藏目录中的技能\n"+
					"  使用 --allow-hidden-dirs 可包含它们",
				r.HiddenCount,
			)
		}
		fmt.Fprintf(opts.IO.ErrOut, "%s 已排除 %d 个位于隐藏目录中的技能，使用 --%s 可包含它们\n",
			cs.Yellow("!"), r.HiddenCount, "allow-hidden-dirs")
	}

	return r.Standard, nil
}

func selectSkill(opts *PreviewOptions, skills []discovery.Skill) (discovery.Skill, error) {
	if opts.SkillName != "" {
		for _, s := range skills {
			if s.DisplayName() == opts.SkillName || s.Name == opts.SkillName {
				return s, nil
			}
		}
		// 回退到 InstallName，以接受安装后提示生成的带命名空间
		// 标识符 (如 "namespace/skill")。
		for _, s := range skills {
			if s.InstallName() == opts.SkillName {
				return s, nil
			}
		}
		return discovery.Skill{}, fmt.Errorf("在 %s 中未找到技能 %q", ghrepo.FullName(opts.repo), opts.SkillName)
	}

	if !opts.IO.CanPrompt() {
		return discovery.Skill{}, fmt.Errorf("非交互运行时必须指定技能名")
	}

	choices := make([]string, len(skills))
	for i, s := range skills {
		choices[i] = s.DisplayName()
	}

	idx, err := opts.Prompter.Select("选择要预览的技能:", "", choices)
	if err != nil {
		return discovery.Skill{}, err
	}

	return skills[idx], nil
}

// treeNode 表示渲染用文件树中的文件或目录。
type treeNode struct {
	name     string
	children []*treeNode
	isDir    bool
}

// renderFileTree 使用制表符绘制字符打印技能文件的树状结构。
func renderFileTree(w io.Writer, cs *iostreams.ColorScheme, files []discovery.SkillFile) {
	root := buildTree(files)
	printTree(w, cs, root.children, "")
}

// buildTree 从扁平的文件路径构建树结构。
func buildTree(files []discovery.SkillFile) *treeNode {
	root := &treeNode{isDir: true}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		current := root
		for i, part := range parts {
			isLast := i == len(parts)-1
			found := false
			for _, child := range current.children {
				if child.name == part {
					current = child
					found = true
					break
				}
			}
			if !found {
				node := &treeNode{name: part, isDir: !isLast}
				current.children = append(current.children, node)
				current = node
			}
		}
	}
	sortTree(root)
	return root
}

func sortTree(node *treeNode) {
	sort.Slice(node.children, func(i, j int) bool {
		if node.children[i].isDir != node.children[j].isDir {
			return node.children[i].isDir
		}
		return node.children[i].name < node.children[j].name
	})
	for _, child := range node.children {
		if child.isDir {
			sortTree(child)
		}
	}
}

func printTree(w io.Writer, cs *iostreams.ColorScheme, nodes []*treeNode, indent string) {
	for i, node := range nodes {
		isLast := i == len(nodes)-1
		connector := "├── "
		childIndent := "│   "
		if isLast {
			connector = "└── "
			childIndent = "    "
		}
		if node.isDir {
			fmt.Fprintf(w, "%s%s%s\n", indent, cs.Muted(connector), cs.Bold(node.name+"/"))
			printTree(w, cs, node.children, indent+cs.Muted(childIndent))
		} else {
			fmt.Fprintf(w, "%s%s%s\n", indent, cs.Muted(connector), node.name)
		}
	}
}
