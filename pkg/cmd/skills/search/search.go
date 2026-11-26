package search

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/registry"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	defaultLimit = 15
	maxResults   = 1000 // GitHub Code Search API hard limit

	// searchPageSize 是每次调用 GitHub Search API 请求的原始结果数 (允许的最大值)。
	searchPageSize = 100
)

// SkillSearchFields 定义 --json 输出可用的字段集合。
var SkillSearchFields = []string{
	"repo",
	"skillName",
	"namespace",
	"description",
	"stars",
	"path",
}

type SearchOptions struct {
	IO             *iostreams.IOStreams
	Telemetry      ghtelemetry.EventRecorder
	HttpClient     func() (*http.Client, error)
	Config         func() (gh.Config, error)
	Prompter       prompter.Prompter
	ExecutablePath string // 当前 gh 二进制路径，用于安装子进程
	Exporter       cmdutil.Exporter

	// User inputs
	// 用户输入
	Query string
	Owner string // 可选: 将结果限定到特定的 GitHub owner
	Page  int
	Limit int
}

// NewCmdSearch 创建 "skills search" 命令。
func NewCmdSearch(f *cmdutil.Factory, telemetry ghtelemetry.EventRecorder, runF func(*SearchOptions) error) *cobra.Command {
	opts := &SearchOptions{
		IO:             f.IOStreams,
		Telemetry:      telemetry,
		HttpClient:     f.HttpClient,
		Config:         f.Config,
		Prompter:       f.Prompter,
		ExecutablePath: f.ExecutablePath,
	}

	cmd := &cobra.Command{
		Use:   "search <query> [flags]",
		Short: "在 GitHub 上搜索技能 (预览版)",
		Long: heredoc.Docf(`
			在所有公开的 GitHub 仓库中搜索与关键词匹配的技能。

			使用 GitHub Code Search API 查找名称或描述与查询词匹配的
			%[1]sSKILL.md%[1]s 文件。

			结果按相关性排序: 名称包含查询词的技能排在最前。

			使用 %[1]s--owner%[1]s 将结果限定到特定的 GitHub 用户或组织。

			交互模式下，可以从结果中选择技能直接安装。
		`, "`"),
		Example: heredoc.Doc(`
			# 搜索与 terraform 相关的技能
			$ gh skill search terraform

			# 搜索特定 owner 的技能
			$ gh skill search terraform --owner hashicorp

			# 查看结果的第二页
			$ gh skill search terraform --page 2

			# 将结果限制为 5 条
			$ gh skill search terraform --limit 5
		`),
		Args: cmdutil.MinimumArgs(1, "无法搜索: 需要查询参数"),
		RunE: func(c *cobra.Command, args []string) error {
			opts.Query = strings.Join(args, " ")

			if len(strings.TrimSpace(opts.Query)) < 2 {
				return cmdutil.FlagErrorf("搜索查询至少需要 2 个字符")
			}

			if opts.Page < 1 {
				return cmdutil.FlagErrorf("无效的页码: %d", opts.Page)
			}

			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %d", opts.Limit)
			}

			opts.Owner = strings.TrimSpace(opts.Owner)
			if opts.Owner != "" && !couldBeOwner(opts.Owner) {
				return cmdutil.FlagErrorf("无效的 owner %q: 必须是有效的 GitHub 用户名或组织", opts.Owner)
			}

			if runF != nil {
				return runF(opts)
			}
			return searchRun(opts)
		},
	}

	cmd.Flags().IntVar(&opts.Page, "page", 1, "要获取的结果页码")
	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", defaultLimit, "每页最大结果数")
	cmd.Flags().StringVar(&opts.Owner, "owner", "", "将结果筛选到特定的 GitHub 用户或组织")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, SkillSearchFields)

	return cmd
}

// codeSearchResult 表示 GitHub Code Search API 响应。
type codeSearchResult struct {
	TotalCount        int              `json:"total_count"`
	IncompleteResults bool             `json:"incomplete_results"`
	Items             []codeSearchItem `json:"items"`
}

// codeSearchItem 表示单条代码搜索命中。
type codeSearchItem struct {
	Name       string               `json:"name"`
	Path       string               `json:"path"`
	SHA        string               `json:"sha"`
	Repository codeSearchRepository `json:"repository"`
}

// codeSearchRepository 是代码搜索命中中内嵌的仓库信息。
type codeSearchRepository struct {
	FullName string `json:"full_name"`
}

// skillResult 是去重后的搜索结果。
type skillResult struct {
	Repo        string
	Owner       string // 从 Repo 解析
	RepoName    string // 从 Repo 解析
	SkillName   string
	Namespace   string // 命名空间前缀: skills/{author}/* 为 author/scope，plugins/{plugin}/skills/* 为插件名
	Description string
	Path        string // 原始文件路径 (如 skills/terraform/SKILL.md)
	BlobSHA     string
	Stars       int // 仓库 star 数
}

// qualifiedName 返回带命名空间的技能名 (如 "author/skill")，
// 无命名空间时仅返回技能名。
func (s skillResult) qualifiedName() string {
	if s.Namespace != "" {
		return s.Namespace + "/" + s.SkillName
	}
	return s.SkillName
}

// ExportData 实现 cmdutil.exportable，用于 --json 输出。
func (s skillResult) ExportData(fields []string) map[string]any {
	data := map[string]any{}
	for _, f := range fields {
		switch f {
		case "repo":
			data[f] = s.Repo
		case "skillName":
			data[f] = s.SkillName
		case "namespace":
			data[f] = s.Namespace
		case "description":
			data[f] = s.Description
		case "stars":
			data[f] = s.Stars
		case "path":
			data[f] = s.Path
		}
	}
	return data
}

func searchRun(opts *SearchOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	apiClient := api.NewClientFromHTTP(httpClient)

	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	host, _ := cfg.Authentication().DefaultHost()
	if err := source.ValidateSupportedHost(host); err != nil {
		return err
	}

	opts.IO.StartProgressIndicatorWithLabel("正在搜索技能")

	skills, err := searchByKeyword(apiClient, host, opts.Query, opts.Owner, opts.Page, opts.Limit)
	if err != nil {
		opts.IO.StopProgressIndicator()
		return err
	}

	if len(skills) == 0 {
		opts.IO.StopProgressIndicator()
		return noResults(opts, noResultsMessage(opts))
	}

	// Pre-rank before expensive enrichment, then truncate working set.
	// 在开销较大的数据补充前先预排序，然后截断工作集。
	rankByRelevance(skills, opts.Query)
	skills = truncateForProcessing(skills, opts.Page, opts.Limit)

	enrichSkills(apiClient, host, skills)
	opts.IO.StopProgressIndicator()

	// Filter out noise and re-rank with enriched data (descriptions, stars).
	// 过滤噪声，并使用补充后的数据 (描述、star 数) 重新排序。
	skills = filterByRelevance(skills, opts.Query)
	if len(skills) == 0 {
		return noResults(opts, noResultsMessage(opts))
	}
	rankByRelevance(skills, opts.Query)

	// Collapse duplicate skill names across repos, keeping up to 3
	// top-ranked instances of each. Prevents aggregator repos
	// (which copy popular skills) from flooding results.
	// 合并跨仓库的重复技能名，每个名称最多保留排名前 3 的实例，
	// 防止聚合仓库 (复制热门技能的仓库) 刷屏结果。
	skills = deduplicateByName(skills)

	// Paginate to the requested page window.
	// 分页到请求的页窗口。
	var totalPages int
	skills, totalPages = paginate(skills, opts.Page, opts.Limit)
	if len(skills) == 0 {
		msg := fmt.Sprintf("查询 %q 在第 %d 页没有找到技能", opts.Query, opts.Page)
		if opts.Owner != "" {
			msg = fmt.Sprintf("owner %q 的查询 %q 在第 %d 页没有找到技能", opts.Owner, opts.Query, opts.Page)
		}
		return noResults(opts, msg)
	}

	return renderResults(opts, skills, totalPages)
}

// noResultsMessage 返回合适的"无结果"消息。
func noResultsMessage(opts *SearchOptions) string {
	if opts.Owner != "" {
		return fmt.Sprintf("owner %q 下没有与 %q 匹配的技能", opts.Owner, opts.Query)
	}
	return fmt.Sprintf("没有与 %q 匹配的技能", opts.Query)
}

// searchByKeyword 并行执行多路搜索: 内容匹配、路径匹配、owner 匹配
// (针对单词查询)，以及 (针对多词查询) 连字符形式的内容匹配，
// 以便用户输入 "mcp apps" 时能命中 "mcp-apps" 这类技能名。
// 当 owner 非空时，所有查询都通过 user:<owner> 限定到该 GitHub
// 用户/组织，并跳过隐式 owner 搜索。
func searchByKeyword(client *api.Client, host, queryTerm, owner string, page, limit int) ([]skillResult, error) {
	ownerScope := ""
	if owner != "" {
		ownerScope = " user:" + owner
	}

	primaryQ := fmt.Sprintf("filename:SKILL.md %s%s", queryTerm, ownerScope)
	pathTerm := strings.ReplaceAll(queryTerm, " ", "-")
	pathQ := fmt.Sprintf("filename:SKILL.md path:%s%s", pathTerm, ownerScope)

	var (
		primaryItems []codeSearchItem
		primaryErr   error
		pathResult   *codeSearchResult
		pathErr      error
		ownerResult  *codeSearchResult
		ownerErr     error
		hyphenResult *codeSearchResult
		hyphenErr    error
	)

	hasSpaces := strings.Contains(queryTerm, " ")

	var wg sync.WaitGroup

	wg.Go(func() {
		pathResult, pathErr = executeSearch(client, host, pathQ, 1, searchPageSize)
	})

	// When no explicit --owner is set and the query looks like it could be a
	// GitHub username, fire an additional user:<query> search to discover
	// skills published by that org. Results compete on the same footing as
	// everything else (no scoring boost).
	// 未显式设置 --owner 且查询看起来可能是 GitHub 用户名时，
	// 额外发起一次 user:<query> 搜索，以发现该组织发布的技能。
	// 结果与其他来源在同一基准上竞争 (无加分)。
	if owner == "" && couldBeOwner(queryTerm) {
		ownerQ := fmt.Sprintf("filename:SKILL.md user:%s", queryTerm)
		wg.Go(func() {
			ownerResult, ownerErr = executeSearch(client, host, ownerQ, 1, searchPageSize)
		})
	}

	// When the query has spaces (e.g. "mcp apps"), run an additional content
	// search with the hyphenated form ("mcp-apps") so we don't miss skills
	// whose names use hyphens as word separators.
	// 查询包含空格时 (如 "mcp apps")，额外执行一次连字符形式
	// ("mcp-apps") 的内容搜索，避免漏掉名称以连字符分词的技能。
	if hasSpaces {
		hyphenQ := fmt.Sprintf("filename:SKILL.md %s%s", pathTerm, ownerScope)
		wg.Go(func() {
			hyphenResult, hyphenErr = executeSearch(client, host, hyphenQ, 1, searchPageSize)
		})
	}

	// Primary content search runs on the main goroutine.
	primaryItems, _, primaryErr = fetchPrimaryPages(client, host, primaryQ, page, limit)
	wg.Wait()

	if primaryErr != nil {
		return nil, primaryErr
	}

	// Merge: path-matched > hyphen-matched > owner-matched > primary content.
	// 合并: 路径匹配 > 连字符匹配 > owner 匹配 > 主内容搜索。
	var merged []codeSearchItem

	if pathErr == nil && pathResult != nil {
		merged = append(merged, pathResult.Items...)
	}
	if hasSpaces && hyphenErr == nil && hyphenResult != nil {
		merged = append(merged, hyphenResult.Items...)
	}
	if ownerErr == nil && ownerResult != nil {
		merged = append(merged, ownerResult.Items...)
	}
	merged = append(merged, primaryItems...)

	return deduplicateResults(merged), nil
}

// noResults 为导出器返回空 JSON 数组，或返回无结果错误。
func noResults(opts *SearchOptions, msg string) error {
	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, []skillResult{})
	}
	return cmdutil.NewNoResultsError(msg)
}

// truncateForProcessing 在开销较大的数据补充前限制工作集大小。
// 工作集中的每个技能都会触发一次 blob 获取 (描述) 以及可能的仓库
// 获取 (star 数)，因此保持该集合较小对性能很重要。预排序确保
// 最优候选排在前面。
func truncateForProcessing(skills []skillResult, page, limit int) []skillResult {
	maxToProcess := page * limit * 3
	if len(skills) > maxToProcess {
		return skills[:maxToProcess]
	}
	return skills
}

// enrichSkills 并发获取描述与 star 数。各函数将结果收集到 map 中；
// 两者都完成后才合并，避免对共享的 skills 切片并发写入。
func enrichSkills(client *api.Client, host string, skills []skillResult) {
	var descMap map[int]string
	var starsMap map[int]int

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		descMap = fetchDescriptions(client, host, skills)
	}()
	go func() {
		defer wg.Done()
		starsMap = fetchRepoStars(client, host, skills)
	}()
	wg.Wait()

	for i := range skills {
		if desc, ok := descMap[i]; ok {
			skills[i].Description = desc
		}
		if stars, ok := starsMap[i]; ok {
			skills[i].Stars = stars
		}
	}
}

// paginate 将结果切分到请求的页窗口。
func paginate(skills []skillResult, page, limit int) ([]skillResult, int) {
	total := len(skills)
	totalPages := (total + limit - 1) / limit
	start := (page - 1) * limit
	if start >= total {
		return nil, totalPages
	}
	end := min(start+limit, total)
	return skills[start:end], totalPages
}

// deduplicateByName 限制相同限定技能名的结果数量。由于结果已按
// 相关性得分预排序，先出现的即最优实例。这可以防止聚合仓库
// (原样复制热门技能的仓库) 刷屏结果，同时仍展示少量备选来源。
// 带命名空间的技能 (如 "author/skill") 与裸名称视为不同。
func deduplicateByName(skills []skillResult) []skillResult {
	const maxPerName = 3
	counts := make(map[string]int)
	var result []skillResult
	for _, s := range skills {
		key := strings.ToLower(s.qualifiedName())
		if counts[key] >= maxPerName {
			continue
		}
		counts[key]++
		result = append(result, s)
	}
	return result
}

// renderResults 处理所有输出模式: JSON、交互选择器或表格。
func renderResults(opts *SearchOptions, skills []skillResult, totalPages int) error {
	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, skills)
	}

	cs := opts.IO.ColorScheme()
	header := fmt.Sprintf("\n%s 显示 %s，匹配 %q",
		cs.SuccessIcon(),
		text.Pluralize(len(skills), "skill"),
		opts.Query,
	)
	if totalPages > 1 {
		header += fmt.Sprintf(" (第 %d/%d 页)", opts.Page, totalPages)
	}

	if opts.IO.CanPrompt() {
		fmt.Fprintln(opts.IO.ErrOut, header)
		if opts.Page < totalPages {
			fmt.Fprintf(opts.IO.ErrOut, "使用 --page %d 查看更多结果。\n", opts.Page+1)
		}
		return promptInstall(opts, skills)
	}

	// Non-interactive mode: render table.
	// 非交互模式: 渲染表格。
	if opts.IO.IsStdoutTTY() {
		fmt.Fprintln(opts.IO.Out, header)
		fmt.Fprintln(opts.IO.Out)
	}

	if err := renderTable(opts.IO, skills); err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() && opts.Page < totalPages {
		fmt.Fprintf(opts.IO.ErrOut, "\n使用 --page %d 查看更多结果。\n", opts.Page+1)
	}

	return nil
}

// renderTable 输出格式化的技能结果表格。
func renderTable(io *iostreams.IOStreams, skills []skillResult) error {
	isTTY := io.IsStdoutTTY()
	tw := io.TerminalWidth()
	descWidth := max(tw-70, 20)

	table := tableprinter.New(io, tableprinter.WithHeader("REPOSITORY", "SKILL", "DESCRIPTION", "STARS"))
	for _, s := range skills {
		table.AddField(s.Repo)
		table.AddField(s.qualifiedName())
		desc := iostreams.NewUntrusted(s.Description).String()
		if isTTY {
			desc = text.Truncate(descWidth, desc)
		}
		table.AddField(desc)
		table.AddField(formatStars(s.Stars))
		table.EndRow()
	}
	return table.Render()
}

// promptInstall 显示多选选择器，让用户从搜索结果中选择要安装的技能，
// 然后为每个技能运行安装命令。
func promptInstall(opts *SearchOptions, skills []skillResult) error {
	fmt.Fprintln(opts.IO.ErrOut)

	cs := opts.IO.ColorScheme()

	// Reserve space for the checkbox UI prefix ("[ ] ") and the description
	// indent ("\n       " = 7 chars), then use the remaining terminal width.
	// 为复选框 UI 前缀 ("[ ] ") 和描述缩进 ("\n       " = 7 字符) 预留空间，
	// 其余使用终端宽度。
	tw := opts.IO.TerminalWidth()
	descWidth := max(tw-11, 30)

	options := make([]string, len(skills))
	for i, s := range skills {
		starStr := ""
		if s.Stars > 0 {
			starStr = "  " + cs.Muted("★ "+formatStars(s.Stars))
		}
		descStr := ""
		if s.Description != "" {
			desc := strings.Join(strings.Fields(iostreams.NewUntrusted(s.Description).String()), " ")
			descStr = "\n       " + cs.Muted(text.Truncate(descWidth, desc))
		}
		options[i] = s.qualifiedName() + "  " + cs.Muted(s.Repo) + starStr + descStr
	}

	indices, err := opts.Prompter.MultiSelect(
		"选择要安装的技能:",
		nil,
		options,
	)
	if err != nil {
		return err
	}

	if len(indices) == 0 {
		return nil
	}

	opts.Telemetry.Record(ghtelemetry.Event{
		Type: "skill_search_install",
		Measures: ghtelemetry.Measures{
			"install_count": int64(len(indices)),
		},
	})

	// Prompt for target agent host (once for all selected skills)
	// 提示选择目标 agent 宿主 (所有选定技能共用一次)
	hostNames := registry.AgentNames()
	hostIdx, err := opts.Prompter.Select("选择目标 agent:", "", hostNames)
	if err != nil {
		return err
	}
	host := registry.Agents[hostIdx]

	// Prompt for installation scope
	// 提示选择安装作用域
	scopeIdx, err := opts.Prompter.Select("安装作用域:", "", registry.ScopeLabels(""))
	if err != nil {
		return err
	}
	scope := string(registry.ScopeProject)
	if scopeIdx == 1 {
		scope = string(registry.ScopeUser)
	}

	for _, idx := range indices {
		s := skills[idx]
		displayName := s.qualifiedName()
		fmt.Fprintf(opts.IO.ErrOut, "\n%s 正在从 %s 安装 %s...\n",
			cs.Blue("::"), displayName, s.Repo)

		//nolint:gosec // executable is the current gh binary
		cmd := exec.Command(opts.ExecutablePath, installCommandArgs(s, host.ID, scope)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = opts.IO.Out
		cmd.Stderr = opts.IO.ErrOut
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "%s 从 %s 安装 %s 失败: %s\n",
				cs.Red("!"), displayName, s.Repo, err)
		}
	}

	return nil
}

func installCommandArgs(skill skillResult, agent, scope string) []string {
	// Use the repo-relative directory path (e.g. "skills/author/name")
	// for disambiguation when installing namespaced skills, so the
	// install command can resolve the exact skill without ambiguity.
	installArg := skill.SkillName
	if skill.Namespace != "" {
		installArg = strings.TrimSuffix(skill.Path, "/SKILL.md")
	}

	return []string{
		"skills", "install",
		"--agent", agent,
		"--scope", scope,
		// Keep untrusted repository and skill-selector values after -- so
		// they cannot be parsed as flags.
		"--",
		skill.Repo, installArg,
	}
}

// relevanceScore 为搜索结果计算数值相关性得分。
// 得分越高排名越靠前。信号 (按优先级排序):
//   - 技能名精确匹配 (3000 分)
//   - 技能名部分匹配 (1000 分)
//   - 命名空间匹配 (500 分)
//   - 描述包含查询词 (100 分)
//   - 仓库 star 数 (√n 加分，6k star 约 2400 分)
func relevanceScore(s skillResult, query string) int {
	term := strings.ToLower(query)
	termHyphen := strings.ReplaceAll(term, " ", "-")
	score := 0

	// Name match. Normalize spaces to hyphens since skill directory names
	// use hyphens as word separators (e.g. query "mcp apps" > "mcp-apps").
	// 名称匹配。将空格规范化为连字符，因为技能目录名使用连字符分词
	// (如查询 "mcp apps" > "mcp-apps")。
	skillLower := strings.ToLower(s.SkillName)
	if skillLower == term || skillLower == termHyphen {
		score += 3_000
	} else if strings.Contains(skillLower, term) || strings.Contains(skillLower, termHyphen) {
		score += 1_000
	}

	// Namespace match.
	// 命名空间匹配。
	if s.Namespace != "" && strings.Contains(strings.ToLower(s.Namespace), term) {
		score += 500
	}

	// Description match.
	// 描述匹配。
	if strings.Contains(strings.ToLower(s.Description), term) {
		score += 100
	}

	// Stars bonus: use √n scaling so popular repos rank meaningfully higher
	// without completely drowning out less-popular but more relevant results.
	// star 加分: 使用 √n 缩放，让热门仓库的排名显著提高，
	// 又不至于完全淹没相关性更高但较冷门的结果。
	if s.Stars > 0 {
		score += int(math.Sqrt(float64(s.Stars)) * 30)
	}

	return score
}

// filterByRelevance 移除与查询无实质关联的结果。若查询词出现在
// 技能名、命名空间、YAML 描述或仓库 owner 或名称中，则保留该结果。
func filterByRelevance(skills []skillResult, query string) []skillResult {
	queryTerm := strings.ToLower(query)
	termHyphen := strings.ReplaceAll(queryTerm, " ", "-")

	filtered := skills[:0] // reuse backing array
	for _, s := range skills {
		nameLower := strings.ToLower(s.SkillName)
		namespaceLower := strings.ToLower(s.Namespace)
		descLower := strings.ToLower(s.Description)
		ownerLower := strings.ToLower(s.Owner)
		repoLower := strings.ToLower(s.RepoName)

		if strings.Contains(nameLower, queryTerm) ||
			strings.Contains(nameLower, termHyphen) ||
			strings.Contains(namespaceLower, queryTerm) ||
			strings.Contains(descLower, queryTerm) ||
			strings.Contains(ownerLower, queryTerm) ||
			strings.Contains(repoLower, queryTerm) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// rankByRelevance 按多信号得分降序排序结果。
func rankByRelevance(skills []skillResult, query string) {
	sort.SliceStable(skills, func(i, j int) bool {
		return relevanceScore(skills[i], query) > relevanceScore(skills[j], query)
	})
}

// couldBeOwner returns true if s looks like a valid GitHub username/org.
// GitHub usernames: 1-39 chars, alphanumeric or hyphen, no leading/trailing hyphens.
func couldBeOwner(s string) bool {
	if len(s) == 0 || len(s) > 39 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			continue
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isRateLimitError checks whether err is a GitHub API rate-limit response.
// Per GitHub docs, a rate limit is indicated by:
//   - HTTP 429 (always a rate limit)
//   - HTTP 403 with x-ratelimit-remaining: 0 (primary rate limit)
//   - HTTP 403 with a retry-after header (secondary rate limit)
func isRateLimitError(err error) bool {
	var httpErr api.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == 429 {
		return true
	}
	if httpErr.StatusCode == 403 {
		if httpErr.Headers.Get("x-ratelimit-remaining") == "0" {
			return true
		}
		if httpErr.Headers.Get("retry-after") != "" {
			return true
		}
	}
	return false
}

// rateLimitErrorMessage returns a user-friendly message for rate-limit errors.
const rateLimitErrorMessage = "GitHub API rate limit exceeded. Please wait a minute and try again."

// executeSearch performs a single GitHub Code Search API call.
func executeSearch(client *api.Client, host, query string, page, pageSize int) (*codeSearchResult, error) {
	apiPath, err := safeurl.JoinPath("search", "code")
	if err != nil {
		return nil, err
	}
	apiPath.SetQuery("q", query)
	apiPath.SetQuery("per_page", strconv.Itoa(pageSize))
	apiPath.SetQuery("page", strconv.Itoa(page))
	var result codeSearchResult
	err = client.REST(host, "GET", apiPath.String(), nil, &result)
	if err != nil && isRateLimitError(err) {
		return nil, fmt.Errorf("%s", rateLimitErrorMessage)
	}
	return &result, err
}

// fetchPrimaryPages fetches enough API pages from GitHub Code Search to
// cover the requested display page, accounting for filtering losses.
func fetchPrimaryPages(client *api.Client, host, query string, displayPage, displayLimit int) ([]codeSearchItem, int, error) {
	// Over-fetch to account for deduplication + filtering losses.
	// The Code Search API is rate-limited at 10 req/min, so we keep
	// page fetching conservative. Two pages (200 results) provides a
	// good buffer for typical filter rates while staying well within
	// the rate-limit budget.
	needed := displayPage * displayLimit * 3
	numPages := max((needed+searchPageSize-1)/searchPageSize, 1)
	maxAPIPages := maxResults / searchPageSize
	if numPages > maxAPIPages {
		numPages = maxAPIPages
	}

	var allItems []codeSearchItem
	var totalCount int
	for p := 1; p <= numPages; p++ {
		result, err := executeSearch(client, host, query, p, searchPageSize)
		if err != nil {
			if p == 1 {
				return nil, 0, err
			}
			break // partial results from earlier pages are OK
		}
		allItems = append(allItems, result.Items...)
		totalCount = result.TotalCount
		if len(result.Items) < searchPageSize {
			break // no more results available
		}
	}
	return allItems, totalCount, nil
}

// deduplicateResults 从代码搜索命中中提取唯一的 (仓库, 命名空间, 技能名) 三元组。
func deduplicateResults(items []codeSearchItem) []skillResult {
	// skillResultKey is a typed map key that deduplicates by (repo, namespace,
	// skill name). All fields are lowercased for case-insensitive comparison.
	type skillResultKey struct {
		repo      string
		namespace string
		skillName string
	}
	seen := make(map[skillResultKey]struct{})
	var results []skillResult

	for _, item := range items {
		skillName, namespace := extractSkillInfo(item.Path)
		if skillName == "" {
			continue
		}
		key := skillResultKey{
			repo:      strings.ToLower(item.Repository.FullName),
			namespace: strings.ToLower(namespace),
			skillName: strings.ToLower(skillName),
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		owner, repoName := splitRepo(item.Repository.FullName)
		results = append(results, skillResult{
			Repo:      item.Repository.FullName,
			Owner:     owner,
			RepoName:  repoName,
			SkillName: skillName,
			Namespace: namespace,
			Path:      item.Path,
			BlobSHA:   item.SHA,
		})
	}

	return results
}

// splitRepo 将 "owner/repo" 拆分为各组成部分。
func splitRepo(fullName string) (string, string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fullName, ""
	}
	return parts[0], parts[1]
}

// fetchDescriptions 并发获取所有搜索结果的 SKILL.md frontmatter 描述。
// 每个结果可能来自不同的仓库。
func fetchDescriptions(client *api.Client, host string, skills []skillResult) map[int]string {
	const maxWorkers = 10
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex

	descs := make(map[int]string)

	for i := range skills {
		if skills[i].BlobSHA == "" {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			content, err := discovery.FetchBlob(client, host, skills[idx].Owner, skills[idx].RepoName, skills[idx].BlobSHA)
			if err != nil {
				return
			}
			result, err := frontmatter.Parse(content.Raw())
			if err != nil {
				return
			}

			mu.Lock()
			descs[idx] = result.Metadata.Description
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	return descs
}

// extractSkillInfo 从 SKILL.md 路径推导技能名与命名空间，
// 但仅当路径符合已知技能约定时才推导；不符合时返回空字符串。
func extractSkillInfo(filePath string) (name, namespace string) {
	return discovery.MatchSkillPath(filePath)
}

// formatStars 格式化 star 数用于展示 (如 1700 > "1.7k")。
// TODO kw: Could be swapped for go-humanize.
func formatStars(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// repoInfo 保存为排序而获取的仓库元数据子集。
type repoInfo struct {
	StargazersCount int `json:"stargazers_count"`
}

// fetchRepoStars 以有界并发获取结果集中每个唯一仓库的 star 数。
func fetchRepoStars(client *api.Client, host string, skills []skillResult) map[int]int {
	const maxWorkers = 10
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex

	repoStars := make(map[string]int)
	seen := make(map[string]bool)

	for _, s := range skills {
		if seen[s.Repo] {
			continue
		}
		seen[s.Repo] = true

		wg.Add(1)
		go func(owner, repo, fullName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			apiPath, err := safeurl.JoinPath("repos", owner, repo)
			if err != nil {
				return
			}
			var info repoInfo
			if err := client.REST(host, "GET", apiPath.String(), nil, &info); err != nil {
				return
			}
			mu.Lock()
			repoStars[fullName] = info.StargazersCount
			mu.Unlock()
		}(s.Owner, s.RepoName, s.Repo)
	}
	wg.Wait()

	result := make(map[int]int, len(skills))
	for i, s := range skills {
		if stars, ok := repoStars[s.Repo]; ok {
			result[i] = stars
		}
	}
	return result
}
