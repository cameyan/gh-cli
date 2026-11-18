package install

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/installer"
	"github.com/cli/cli/v2/internal/skills/registry"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	// allSkillsKey 是选择全部技能时的持久选项标签。
	allSkillsKey = "(all skills)"

	// multiSelectLabelMargin 为可用的提示组件所使用的最宽选项前缀预留列数:
	// huh 的边框、内边距、光标与复选框。
	multiSelectLabelMargin = 8

	// maxSearchResults 限制交互式选择中每页搜索结果展示的技能数量上限，
	// 以保证提示内容易于阅读。
	maxSearchResults = 30
)

// InstallOptions 保存 install 命令的所有依赖项与用户提供的标志。
type InstallOptions struct {
	IO         *iostreams.IOStreams
	Telemetry  ghtelemetry.EventRecorder
	HttpClient func() (*http.Client, error)
	Prompter   prompter.Prompter
	GitClient  *git.Client
	Remotes    func() (ghContext.Remotes, error)

	SkillSource     string // owner/repo 或本地路径 (设置 --from-local 时)
	SkillName       string // 可带 @version 后缀
	Agent           string
	Scope           string
	ScopeChanged    bool // 显式设置 --scope 时为 true
	Pin             string
	Dir             string // 覆盖 --agent 和 --scope
	All             bool
	Force           bool
	FromLocal       bool // 将 SkillSource 视为本地目录路径
	AllowHiddenDirs bool // 包含点前缀目录中的技能
	Upstream        bool // 检测到重新发布的技能时从上游安装

	repo      ghrepo.Interface // SkillSource 为 GitHub 仓库时设置
	localPath string           // FromLocal 为 true 时设置
	version   string           // 从 SkillName@version 解析
}

// NewCmdInstall 创建 "skills install" 命令。
func NewCmdInstall(f *cmdutil.Factory, telemetry ghtelemetry.EventRecorder, runF func(*InstallOptions) error) *cobra.Command {
	opts := &InstallOptions{
		IO:         f.IOStreams,
		Telemetry:  telemetry,
		Prompter:   f.Prompter,
		GitClient:  f.GitClient,
		Remotes:    f.Remotes,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "install <repository> [<skill[@version]>] [flags]",
		Short: "从 GitHub 仓库安装 agent 技能 (预览版)",
		Long: heredoc.Docf(`
			从 GitHub 仓库或本地目录将 agent 技能安装到本地环境。技能会被放置在
			特定于宿主的目录中，可以是项目作用域 (当前 git 仓库内) 或用户作用域
			(主目录中，处处可用)。

			支持广泛的 AI 编码 agent，包括 GitHub Copilot、Claude Code、Cursor、
			Codex、Gemini CLI、Antigravity、Amp、Devin、Goose、Grok、Junie、
			OpenCode 等众多工具。

			支持的 %[1]s--agent%[1]s 取值:

			%[2]s

			使用 %[1]s--agent%[1]s 和 %[1]s--scope%[1]s 控制安装位置，或使用 %[1]s--dir%[1]s
			指定自定义目录。默认作用域为 %[1]sproject%[1]s，默认 agent 为
			%[1]sgithub-copilot%[1]s (非交互运行时)。

			在项目作用域下，多个 agent (包括 GitHub Copilot、Cursor、Codex、
			Gemini CLI、Antigravity、Amp、Cline、OpenCode 和 Warp) 共享
			%[1]s.agents/skills%[1]s 目录。如果你选择了多个解析到同一目标位置的宿主，
			每个技能在该目录中只会安装一次。

			第一个参数是 %[1]sOWNER/REPO%[1]s 格式的 GitHub 仓库。
			使用 %[1]s--from-local%[1]s 可改为从本地目录安装。
			本地技能按照与远程仓库相同的约定自动发现，文件以复制方式
			(而非符号链接) 安装，并在 frontmatter 中注入本地路径跟踪元数据。

			技能会按照 Agent Skills 规范定义的 %[1]sskills/*/SKILL.md%[1]s 约定自动发现，
			包括 %[1]sskills/%[1]s 目录嵌套在前缀下时 (如 %[1]sterraform/code-generation/skills/...%[1]s)。
			有关该规范的更多信息，
			参见: https://agentskills.io/specification

			技能参数可以是名称、带命名空间的名称 (%[1]sauthor/skill%[1]s)，
			或仓库内的精确路径 (%[1]sskills/author/skill%[1]s、
			%[1]spackages/agent-skills/code-review%[1]s，或任何 %[1]s.../SKILL.md%[1]s 路径)。
			带一个斜杠的命名空间名称按名称匹配。使用 %[1]sSKILL.md%[1]s
			后缀可强制匹配标准约定之外的单层目录路径。

			性能提示: 从包含大量技能的大型仓库安装时，提供精确路径而不是技能名称
			可以避免对仓库进行全树遍历，显著加快安装速度。

			当提供的技能名称不带版本时，CLI 按以下顺序解析版本:

			  1. 仓库中最新的标签发布
			  2. 默认分支的 HEAD

			要固定到特定版本，可在技能名称后追加 %[1]s@VERSION%[1]s，
			或使用 %[1]s--pin%[1]s 标志。版本将解析为 git 标签或 commit SHA。

			已安装的技能会在 frontmatter 中注入源跟踪元数据。该元数据标识
			来源仓库，并使 %[1]sgh skill update%[1]s 能够检测变更。

			交互运行时，该命令会对缺失的参数进行提示。

			使用 %[1]s--all%[1]s 可安装仓库中发现的所有技能，无需逐一选择。
			非交互运行时必须提供 %[1]srepository%[1]s；若既无技能名也无 %[1]s--all%[1]s，
			则会列出匹配的技能 (通过管道时以制表符分隔)，便于先用
			%[1]sgrep%[1]s 等工具浏览或筛选，再指定具体技能重新运行。
		`, "`", registry.AgentHelpList()),
		Example: heredoc.Doc(`
			# 交互式: 选择仓库、技能和 agent
			$ gh skill install

			# 以交互方式从仓库中选择一个技能
			$ gh skill install github/awesome-copilot

			# 非交互列出可用技能 (例如通过管道传给 grep)
			$ gh skill install github/awesome-copilot | grep review

			# 安装指定技能
			$ gh skill install github/awesome-copilot git-commit

			# 安装仓库中的所有技能
			$ gh skill install github/awesome-copilot --all

			# 安装特定版本
			$ gh skill install github/awesome-copilot git-commit@v1.2.0

			# 按路径从带命名空间的大型仓库安装 (高效，跳过完整发现)
			$ gh skill install github/awesome-copilot skills/monalisa/code-review

			# 从非标准嵌套路径安装 (高效，跳过完整发现)
			$ gh skill install monalisa/skills-repo packages/agent-skills/code-review

			# 从本地目录安装
			$ gh skill install ./my-skills-repo --from-local

			# 安装指定的本地技能
			$ gh skill install ./my-skills-repo git-commit --from-local

			# 为 Claude Code 以用户作用域安装
			$ gh skill install github/awesome-copilot git-commit --agent claude-code --scope user

			# 固定到特定 git ref
			$ gh skill install github/awesome-copilot git-commit --pin v2.0.0

			# 安装隐藏目录中的技能 (如 .claude/skills/)
			$ gh skill install owner/repo --allow-hidden-dirs
		`),
		Aliases: []string{"add"},
		Args:    cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) >= 1 {
				opts.SkillSource = args[0]
			}
			if len(args) >= 2 {
				opts.SkillName = args[1]
			}
			opts.ScopeChanged = cmd.Flags().Changed("scope")

			if opts.All && opts.SkillName != "" {
				return cmdutil.FlagErrorf("不能将 --all 与技能参数同时使用")
			}

			// 提前解析来源类型，便于 installRun 直接分支处理。
			if opts.FromLocal {
				if opts.SkillSource == "" {
					return cmdutil.FlagErrorf("--from-local 需要一个目录路径参数")
				}
				opts.localPath = opts.SkillSource
			} else if len(args) == 0 && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("必须指定要安装的仓库")
			}

			if err := cmdutil.MutuallyExclusive("--from-local 和 --pin 不能同时使用", opts.FromLocal, opts.Pin != ""); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive("--from-local 和 --upstream 不能同时使用", opts.FromLocal, opts.Upstream); err != nil {
				return err
			}

			if opts.Pin != "" && opts.SkillName != "" && strings.Contains(opts.SkillName, "@") {
				return cmdutil.FlagErrorf("技能名中已含 @version 时不能使用 --pin")
			}

			if runF != nil {
				return runF(opts)
			}
			return installRun(opts)
		},
	}

	agentFlag := cmdutil.StringEnumFlag(cmd, &opts.Agent, "agent", "", "", registry.AgentIDs(), "目标 agent")
	agentFlag.Usage = "目标 agent (取值见上文)"
	cmdutil.StringEnumFlag(cmd, &opts.Scope, "scope", "", "project", []string{"project", "user"}, "安装作用域")
	cmd.Flags().StringVar(&opts.Pin, "pin", "", "固定到特定的 git 标签或 commit SHA")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "安装到自定义目录 (覆盖 --agent 和 --scope)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "安装所有技能，无需逐一选择")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "覆盖已存在的技能且不提示")
	cmd.Flags().BoolVar(&opts.FromLocal, "from-local", false, "将参数视为本地目录路径而非仓库")
	cmd.Flags().BoolVar(&opts.AllowHiddenDirs, "allow-hidden-dirs", false, "包含隐藏目录中的技能 (如 .claude/skills/、.agents/skills/)")
	cmd.Flags().BoolVar(&opts.Upstream, "upstream", false, "检测到重新发布的技能时从上游来源安装")
	cmdutil.DisableAuthCheckFlag(cmd.Flags().Lookup("from-local"))

	return cmd
}

func installRun(opts *InstallOptions) error {
	cs := opts.IO.ColorScheme()
	canPrompt := opts.IO.CanPrompt()

	if opts.localPath != "" {
		return runLocalInstall(opts)
	}

	repo, repoSource, err := resolveRepoArg(opts.SkillSource, canPrompt, opts.Prompter)
	if err != nil {
		return err
	}
	opts.repo = repo
	opts.SkillSource = repoSource

	parseSkillFromOpts(opts)

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	hostname := opts.repo.RepoHost()
	if err := source.ValidateSupportedHost(hostname); err != nil {
		return err
	}

	// Kick off the visibility fetch in parallel with the install work so
	// the extra API roundtrip doesn't add latency on the critical path.
	// The result is consumed when the telemetry event is emitted below.
	// Capture repo fields now to avoid a data race if opts.repo is
	// swapped during an upstream redirect.
	// 将仓库可见性查询与安装工作并行发起，避免额外的 API 往返
	// 增加关键路径上的延迟。结果在下方发出遥测事件时消费。
	// 现在先捕获仓库字段，以防 opts.repo 在上游重定向期间被替换而引发数据竞争。
	type visResult struct {
		vis discovery.RepoVisibility
		err error
	}
	visCh := make(chan visResult, 1)
	visOwner := opts.repo.RepoOwner()
	visRepo := opts.repo.RepoName()
	go func() {
		vis, err := discovery.FetchRepoVisibility(apiClient, hostname, visOwner, visRepo)
		visCh <- visResult{vis: vis, err: err}
	}()

	resolved, err := resolveVersion(opts, apiClient, hostname)
	if err != nil {
		return err
	}

	var selectedSkills []discovery.Skill

	if discovery.IsSkillPath(opts.SkillName) {
		opts.IO.StartProgressIndicatorWithLabel("正在查找技能")
		skill, err := discovery.DiscoverSkillByPath(apiClient, hostname, opts.repo.RepoOwner(), opts.repo.RepoName(), resolved.SHA, opts.SkillName)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}
		selectedSkills = []discovery.Skill{*skill}
	} else {
		skills, err := discoverSkills(opts, apiClient, hostname, resolved)
		if err != nil {
			return err
		}

		selectedSkills, err = selectSkillsWithSelector(opts, skills, canPrompt, skillSelector{
			matchByName: matchSkillByName,
			sourceHint:  ghrepo.FullName(opts.repo),
			fetchDescriptions: func() {
				opts.IO.StartProgressIndicatorWithLabel("正在获取技能信息")
				discovery.FetchDescriptionsConcurrent(apiClient, hostname, opts.repo.RepoOwner(), opts.repo.RepoName(), skills, nil)
				opts.IO.StopProgressIndicator()
			},
		})
		if err != nil {
			if errors.Is(err, errSkillsListed) {
				return nil
			}
			return err
		}
	}

	// Track upstream provenance detection result for telemetry.
	// 记录上游来源检测结果，用于遥测。
	upstreamSource := "none"

	// Check if the selected skill was re-published from an upstream source.
	// The re-publisher's SKILL.md will have github-repo metadata pointing
	// to the original source repo. If detected, offer to install directly
	// from upstream instead.
	// 检查所选技能是否从上游来源重新发布。重新发布者的 SKILL.md 会包含
	// 指向原始来源仓库的 github-repo 元数据。若检测到，则改为提供
	// 直接从上游安装的选项。
	if len(selectedSkills) == 1 && selectedSkills[0].BlobSHA != "" {
		upstreamRepo, detected, err := checkUpstreamProvenance(opts, apiClient, hostname, selectedSkills[0], resolved.SHA)
		if err != nil {
			return err
		}
		if upstreamRepo != nil {
			redirectDims := map[string]string{}
			select {
			case r := <-visCh:
				if r.err == nil && r.vis == discovery.RepoVisibilityPublic {
					redirectDims["from_owner"] = visOwner
					redirectDims["from_repo"] = visRepo
				}
			case <-time.After(visibilityWaitTimeout):
			}
			opts.Telemetry.Record(ghtelemetry.Event{
				Type:       "skill_upstream_redirect",
				Dimensions: redirectDims,
			})
			opts.repo = upstreamRepo
			opts.SkillSource = ghrepo.FullName(upstreamRepo)
			opts.version = ""
			opts.Pin = ""
			return installRun(opts)
		}
		if detected {
			upstreamSource = "republisher"
		}
	}

	printPreInstallDisclaimer(opts.IO.ErrOut, cs)

	selectedHosts, err := resolveHosts(opts, canPrompt)
	if err != nil {
		return err
	}

	scope, err := resolveScope(opts, canPrompt)
	if err != nil {
		return err
	}

	gitRoot := installer.ResolveGitRoot(opts.GitClient)
	homeDir := installer.ResolveHomeDir()
	repoSource = ghrepo.FullName(opts.repo)

	plans, err := buildInstallPlans(opts, selectedSkills, selectedHosts, scope, gitRoot, homeDir, canPrompt)
	if err != nil {
		return err
	}

	for _, plan := range plans {
		if len(plans) > 1 {
			fmt.Fprintf(opts.IO.ErrOut, "\n正在为 %s 安装到 %s...\n", friendlyDir(plan.dir), formatPlanHosts(plan.hosts))
		}

		result, err := installer.Install(&installer.Options{
			Host:       hostname,
			Owner:      opts.repo.RepoOwner(),
			Repo:       opts.repo.RepoName(),
			Ref:        resolved.Ref,
			SHA:        resolved.SHA,
			PinnedRef:  opts.Pin,
			Skills:     plan.skills,
			Dir:        plan.dir,
			Client:     apiClient,
			OnProgress: installProgress(opts.IO, len(plan.skills)),
		})

		if result != nil {
			for _, w := range result.Warnings {
				fmt.Fprintf(opts.IO.ErrOut, "%s %s\n", cs.WarningIcon(), w)
			}

			for _, name := range result.Installed {
				fmt.Fprintf(opts.IO.Out, "%s 已安装 %s (来自 %s@%s) 到 %s\n",
					cs.SuccessIcon(), name, repoSource, discovery.ShortRef(resolved.Ref), friendlyDir(result.Dir))
			}

			printFileTree(opts.IO.ErrOut, cs, result.Dir, result.Installed)
			printReviewHint(opts.IO.ErrOut, repoSource, resolved.SHA, result.Installed, opts.AllowHiddenDirs)
			printHostHints(opts.IO.ErrOut, cs, plan.hosts, result.Installed, result.Dir, gitRoot)
		}

		if err != nil {
			return err
		}
	}

	dims := map[string]string{
		"agent_hosts":     mapAgentHostsToIDs(selectedHosts),
		"skill_host_type": ghinstance.CategorizeHost(opts.repo.RepoHost()),
		"upstream_source": upstreamSource,
	}
	select {
	case r := <-visCh:
		if r.err == nil {
			dims["repo_visibility"] = string(r.vis)
			if r.vis == discovery.RepoVisibilityPublic {
				dims["skill_owner"] = opts.repo.RepoOwner()
				dims["skill_repo"] = opts.repo.RepoName()
				dims["skill_names"] = mapSkillsToNames(selectedSkills)
			}
		} else {
			dims["repo_visibility"] = "unknown"
		}
	case <-time.After(visibilityWaitTimeout):
		dims["repo_visibility"] = "unknown"
	}
	opts.Telemetry.Record(ghtelemetry.Event{
		Type:       "skill_install",
		Dimensions: dims,
	})

	return nil
}

// visibilityWaitTimeout 是在发出遥测事件时等待进行中的仓库可见性查询
// 的最长时间，超时则放弃并以 repo_visibility="unknown" 发出。到这一步时
// 命令已完成了多次串行 API 调用和 (安装时的) git sparse-checkout，
// 该查询几乎总是已完成；此预算仅为该单个 REST 调用停滞的情况
// 提供一个短暂的安全兜底。
const visibilityWaitTimeout = 200 * time.Millisecond

func mapSkillsToNames(skills []discovery.Skill) string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.DisplayName()
	}
	return strings.Join(names, ",")
}

func mapAgentHostsToIDs(hosts []*registry.AgentHost) string {
	agentHostIDs := make([]string, len(hosts))
	for i, h := range hosts {
		agentHostIDs[i] = h.ID
	}
	return strings.Join(agentHostIDs, ",")
}

// runLocalInstall 处理来自本地目录路径的安装。
func runLocalInstall(opts *InstallOptions) error {
	cs := opts.IO.ColorScheme()
	canPrompt := opts.IO.CanPrompt()
	sourcePath := opts.localPath
	if sourcePath == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			sourcePath = home
		}
	} else if after, ok := strings.CutPrefix(sourcePath, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			sourcePath = filepath.Join(home, after)
		}
	}

	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("无法解析路径: %w", err)
	}

	opts.IO.StartProgressIndicatorWithLabel("正在发现技能")
	allSkills, err := discovery.DiscoverLocalSkillsWithOptions(absSource, discovery.DiscoverOptions{})
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	skills, err := filterHiddenDirSkills(opts, allSkills)
	if err != nil {
		return err
	}

	if canPrompt {
		fmt.Fprintf(opts.IO.ErrOut, "发现 %d 个技能\n", len(skills))
	}

	selectedSkills, err := selectSkillsWithSelector(opts, skills, canPrompt, skillSelector{
		matchByName: matchLocalSkillByName,
		sourceHint:  absSource,
	})
	if err != nil {
		if errors.Is(err, errSkillsListed) {
			return nil
		}
		return err
	}

	printPreInstallDisclaimer(opts.IO.ErrOut, cs)

	selectedHosts, err := resolveHosts(opts, canPrompt)
	if err != nil {
		return err
	}

	scope, err := resolveScope(opts, canPrompt)
	if err != nil {
		return err
	}

	gitRoot := installer.ResolveGitRoot(opts.GitClient)
	homeDir := installer.ResolveHomeDir()

	plans, err := buildInstallPlans(opts, selectedSkills, selectedHosts, scope, gitRoot, homeDir, canPrompt)
	if err != nil {
		return err
	}

	for _, plan := range plans {
		if len(plans) > 1 {
			fmt.Fprintf(opts.IO.ErrOut, "\nInstalling to %s for %s...\n", friendlyDir(plan.dir), formatPlanHosts(plan.hosts))
		}

		result, err := installer.InstallLocal(&installer.LocalOptions{
			SourceDir: absSource,
			Skills:    plan.skills,
			Dir:       plan.dir,
		})
		if err != nil {
			return err
		}

		for _, name := range result.Installed {
			fmt.Fprintf(opts.IO.Out, "已安装 %s (来自 %s) 到 %s\n",
				name, opts.SkillSource, friendlyDir(result.Dir))
		}

		printFileTree(opts.IO.ErrOut, cs, result.Dir, result.Installed)
		printReviewHint(opts.IO.ErrOut, "", "", result.Installed, false)
		printHostHints(opts.IO.ErrOut, cs, plan.hosts, result.Installed, result.Dir, gitRoot)
	}

	return nil
}

func resolveRepoArg(skillSource string, canPrompt bool, p prompter.Prompter) (ghrepo.Interface, string, error) {
	if skillSource == "" {
		if !canPrompt {
			return nil, "", cmdutil.FlagErrorf("必须指定要安装的仓库")
		}
		repoInput, err := p.Input("仓库 (owner/repo):", "")
		if err != nil {
			return nil, "", err
		}
		skillSource = strings.TrimSpace(repoInput)
		if skillSource == "" {
			return nil, "", fmt.Errorf("必须指定要安装的仓库")
		}
	}
	repo, err := ghrepo.FromFullName(skillSource)
	if err != nil {
		return nil, "", cmdutil.FlagErrorf("无效的仓库引用 %q: 应为 OWNER/REPO、HOST/OWNER/REPO 或完整 URL", skillSource)
	}
	return repo, skillSource, nil
}

func parseSkillFromOpts(opts *InstallOptions) {
	if opts.SkillName != "" {
		if name, version, ok := cutLast(opts.SkillName, "@"); ok && name != "" {
			opts.version = version
			opts.SkillName = name
			return
		}
	}
	if opts.Pin != "" {
		opts.version = opts.Pin
	}
}

// cutLast 在最后一次出现 sep 的位置切分 s，
// 返回 sep 前后的文本，以及是否找到 sep。
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

func resolveVersion(opts *InstallOptions, client *api.Client, hostname string) (*discovery.ResolvedRef, error) {
	opts.IO.StartProgressIndicatorWithLabel("正在解析版本")
	resolved, err := discovery.ResolveRef(client, hostname, opts.repo.RepoOwner(), opts.repo.RepoName(), opts.version)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return nil, fmt.Errorf("无法解析版本: %w", err)
	}
	fmt.Fprintf(opts.IO.ErrOut, "使用 ref %s (%s)\n", discovery.ShortRef(resolved.Ref), git.ShortSHA(resolved.SHA))
	return resolved, nil
}

func discoverSkills(opts *InstallOptions, client *api.Client, hostname string, resolved *discovery.ResolvedRef) ([]discovery.Skill, error) {
	opts.IO.StartProgressIndicatorWithLabel("正在发现技能")
	allSkills, err := discovery.DiscoverSkillsWithOptions(client, hostname, opts.repo.RepoOwner(), opts.repo.RepoName(), resolved.SHA, discovery.DiscoverOptions{})
	opts.IO.StopProgressIndicator()
	if err != nil {
		if treeTooLarge, ok := errors.AsType[*discovery.TreeTooLargeError](err); ok {
			fmt.Fprintf(opts.IO.ErrOut, "%s\n  请改用基于路径的安装: gh skill install %s/%s skills/<skill-name>\n",
				err, treeTooLarge.Owner, treeTooLarge.Repo)
			return nil, err
		}
		return nil, err
	}
	skills, filterErr := filterHiddenDirSkills(opts, allSkills)
	if filterErr != nil {
		return nil, filterErr
	}
	logConventions(opts.IO, skills)
	for _, s := range skills {
		if !discovery.IsSpecCompliant(s.Name) {
			fmt.Fprintf(opts.IO.ErrOut, "警告: 技能 %q 不符合 agentskills.io 命名规范\n", s.DisplayName())
		}
	}
	return skills, nil
}

func logConventions(io *iostreams.IOStreams, skills []discovery.Skill) {
	conventions := make(map[string]int)
	for _, s := range skills {
		conventions[s.Convention]++
	}
	if n, ok := conventions["skills-namespaced"]; ok {
		fmt.Fprintf(io.ErrOut, "注意: 在 skills/{author}/ 目录中发现 %d 个带命名空间的技能\n", n)
	}
	if n, ok := conventions["plugins"]; ok {
		fmt.Fprintf(io.ErrOut, "注意: 发现 %d 个使用 plugins/ 约定的技能\n", n)
	}
	if n, ok := conventions["root"]; ok {
		fmt.Fprintf(io.ErrOut, "注意: 在仓库根目录发现 %d 个技能\n", n)
	}
}

// skillSelector 保存远程与本地技能选择之间不同的回调。
type skillSelector struct {
	// matchByName 将技能名称解析为匹配的技能。
	matchByName func(opts *InstallOptions, skills []discovery.Skill) ([]discovery.Skill, error)
	// sourceHint 在名称冲突错误指引中显示 (如 "owner/repo" 或 "/path/to/skills")。
	sourceHint string
	// fetchDescriptions 若非 nil，在提示前调用以预填描述信息。
	fetchDescriptions func()
}

type installPlan struct {
	dir    string
	hosts  []*registry.AgentHost
	skills []discovery.Skill
}

// errSkillsListed 是 selectSkillsWithSelector 在命令以非交互方式运行
// 且未提供技能名时返回的哨兵错误。此时选择器会将可用技能打印到
// stdout (便于通过管道传给 grep 等)，调用方退出且不执行安装。
var errSkillsListed = errors.New("skills listed")

func selectSkillsWithSelector(opts *InstallOptions, skills []discovery.Skill, canPrompt bool, sel skillSelector) ([]discovery.Skill, error) {
	checkCollisions := func(ss []discovery.Skill) error {
		if err := collisionError(ss); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "提示: 使用完整名称逐一安装: gh skill install %s namespace/skill-name\n", sel.sourceHint)
			return err
		}
		return nil
	}

	if opts.All {
		if err := checkCollisions(skills); err != nil {
			return nil, err
		}
		return skills, nil
	}

	if opts.SkillName != "" {
		return sel.matchByName(opts, skills)
	}

	if !canPrompt {
		if err := listAvailableSkills(opts, skills, sel); err != nil {
			return nil, err
		}
		return nil, errSkillsListed
	}

	if sel.fetchDescriptions != nil {
		sel.fetchDescriptions()
	}

	labelWidth := max(opts.IO.TerminalWidth()-multiSelectLabelMargin, 1)

	selected, err := opts.Prompter.MultiSelectWithSearch(
		"选择要安装的技能:",
		"筛选技能",
		nil,
		[]string{allSkillsKey},
		skillSearchFunc(skills, labelWidth),
	)
	if err != nil {
		return nil, err
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("必须至少选择一个技能")
	}

	for _, s := range selected {
		if s == allSkillsKey {
			if err := checkCollisions(skills); err != nil {
				return nil, err
			}
			return skills, nil
		}
	}

	result, err := matchSelectedSkills(skills, selected)
	if err != nil {
		return nil, err
	}
	return result, checkCollisions(result)
}

// listAvailableSkills 将发现的技能以表格形式打印给非交互调用方，
// 展示与交互式选择器相同的信息，便于浏览或通过管道传给 grep 等工具。
func listAvailableSkills(opts *InstallOptions, skills []discovery.Skill, sel skillSelector) error {
	if len(skills) == 0 {
		return fmt.Errorf("在 %s 中未发现任何技能", sel.sourceHint)
	}

	if sel.fetchDescriptions != nil {
		sel.fetchDescriptions()
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.ErrOut, "显示来自 %s 的 %s。指定技能名重新运行即可安装。\n\n",
			sel.sourceHint, text.Pluralize(len(skills), "skill"))
	}

	tw := opts.IO.TerminalWidth()
	descWidth := max(tw-40, 20)
	isTTY := opts.IO.IsStdoutTTY()

	table := tableprinter.New(opts.IO, tableprinter.WithHeader("SKILL", "DESCRIPTION"))
	for _, s := range skills {
		table.AddField(s.DisplayName())
		desc := s.Description
		if isTTY {
			desc = text.Truncate(descWidth, desc)
		}
		table.AddField(desc)
		table.EndRow()
	}
	return table.Render()
}

func matchSkillByName(opts *InstallOptions, skills []discovery.Skill) ([]discovery.Skill, error) {
	for _, s := range skills {
		if s.DisplayName() == opts.SkillName {
			return []discovery.Skill{s}, nil
		}
	}

	var matches []discovery.Skill
	for _, s := range skills {
		if s.Name == opts.SkillName {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("在 %s 中未找到技能 %q", ghrepo.FullName(opts.repo), opts.SkillName)
	case 1:
		return matches, nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.DisplayName()
		}
		return nil, fmt.Errorf(
			"技能名 %q 存在歧义，找到多个匹配项:\n  %s\n  请指定完整名称 (如 %s) 以消除歧义",
			opts.SkillName, strings.Join(names, "\n  "), names[0],
		)
	}
}

func matchLocalSkillByName(opts *InstallOptions, skills []discovery.Skill) ([]discovery.Skill, error) {
	for _, s := range skills {
		if s.DisplayName() == opts.SkillName || s.Name == opts.SkillName {
			return []discovery.Skill{s}, nil
		}
	}
	return nil, fmt.Errorf("在本地目录中未找到技能 %q", opts.SkillName)
}

// skillSearchFunc 为 MultiSelectWithSearch 返回一个搜索函数，
// 按名称和描述进行不区分大小写的子串匹配来筛选技能。
func skillSearchFunc(skills []discovery.Skill, labelWidth int) func(string) prompter.MultiSelectSearchResult {
	return func(query string) prompter.MultiSelectSearchResult {
		var matched []discovery.Skill
		if query == "" {
			matched = skills
		} else {
			q := strings.ToLower(query)
			for _, s := range skills {
				if strings.Contains(strings.ToLower(s.DisplayName()), q) ||
					strings.Contains(strings.ToLower(s.Description), q) {
					matched = append(matched, s)
				}
			}
		}

		more := 0
		if len(matched) > maxSearchResults {
			more = len(matched) - maxSearchResults
			matched = matched[:maxSearchResults]
		}

		keys := make([]string, len(matched))
		labels := make([]string, len(matched))
		for i, s := range matched {
			keys[i] = s.DisplayName()
			label := s.DisplayName()
			if s.Description != "" {
				label = fmt.Sprintf("%s - %s", label, text.RemoveExcessiveWhitespace(s.Description))
			}
			labels[i] = text.Truncate(labelWidth, label)
		}

		return prompter.MultiSelectSearchResult{
			Keys:        keys,
			Labels:      labels,
			MoreResults: more,
		}
	}
}

// matchSelectedSkills 将显示名映射回对应的技能结构。
func matchSelectedSkills(skills []discovery.Skill, selected []string) ([]discovery.Skill, error) {
	nameSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		nameSet[name] = struct{}{}
	}

	var result []discovery.Skill
	for _, s := range skills {
		if _, ok := nameSet[s.DisplayName()]; ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("未找到匹配的技能")
	}
	return result, nil
}

// collisionError 检查所选技能之间是否存在名称冲突。
func collisionError(ss []discovery.Skill) error {
	collisions := discovery.FindNameCollisions(ss)
	if len(collisions) == 0 {
		return nil
	}
	return fmt.Errorf("无法安装名称冲突的技能；它们会相互覆盖:\n  %s",
		discovery.FormatCollisions(collisions))
}

func resolveHosts(opts *InstallOptions, canPrompt bool) ([]*registry.AgentHost, error) {
	if opts.Agent != "" {
		h, err := registry.FindByID(opts.Agent)
		if err != nil {
			return nil, err
		}
		return []*registry.AgentHost{h}, nil
	}

	// --dir fixes the destination in resolveInstallDir regardless of the agent host,
	// which then only affects post-install hint output; the default is a safe placeholder.
	// --dir 会在 resolveInstallDir 中固定目标位置，不受 agent 宿主影响；
	// 此时宿主仅影响安装后的提示输出，默认值只是一个安全的占位。
	if opts.Dir != "" {
		h, err := registry.FindByID(registry.DefaultAgentID)
		if err != nil {
			return nil, err
		}
		return []*registry.AgentHost{h}, nil
	}

	if !canPrompt {
		h, err := registry.FindByID(registry.DefaultAgentID)
		if err != nil {
			return nil, err
		}
		return []*registry.AgentHost{h}, nil
	}

	fmt.Fprintln(opts.IO.ErrOut)
	labels := make([]string, len(registry.Agents))
	defaultLabel := ""
	for i, h := range registry.Agents {
		labels[i] = h.Name
		if h.ID == registry.DefaultAgentID {
			defaultLabel = labels[i]
		}
	}
	if defaultLabel == "" {
		defaultLabel = labels[0]
	}
	indices, err := opts.Prompter.MultiSelect("选择目标 agent:", []string{defaultLabel}, labels)
	if err != nil {
		return nil, err
	}

	if len(indices) == 0 {
		return nil, fmt.Errorf("必须至少选择一个目标 agent")
	}

	selected := make([]*registry.AgentHost, len(indices))
	for i, idx := range indices {
		selected[i] = &registry.Agents[idx]
	}
	return selected, nil
}

func resolveScope(opts *InstallOptions, canPrompt bool) (registry.Scope, error) {
	if opts.Dir != "" {
		return registry.Scope(opts.Scope), nil
	}

	if opts.ScopeChanged || !canPrompt {
		return registry.Scope(opts.Scope), nil
	}

	var repoName string
	if opts.Remotes != nil {
		if remotes, err := opts.Remotes(); err == nil && len(remotes) > 0 {
			repoName = ghrepo.FullName(remotes[0].Repo)
		}
	}
	idx, err := opts.Prompter.Select("安装作用域:", "", registry.ScopeLabels(repoName))
	if err != nil {
		return "", err
	}
	if idx == 0 {
		return registry.ScopeProject, nil
	}
	return registry.ScopeUser, nil
}

func buildInstallPlans(opts *InstallOptions, selectedSkills []discovery.Skill, selectedHosts []*registry.AgentHost, scope registry.Scope, gitRoot, homeDir string, canPrompt bool) ([]installPlan, error) {
	byDir := make(map[string]*installPlan)
	orderedDirs := make([]string, 0, len(selectedHosts))

	for _, host := range selectedHosts {
		targetDir, err := resolveInstallDir(opts, host, scope, gitRoot, homeDir)
		if err != nil {
			return nil, err
		}

		plan, ok := byDir[targetDir]
		if !ok {
			plan = &installPlan{dir: targetDir}
			byDir[targetDir] = plan
			orderedDirs = append(orderedDirs, targetDir)
		}
		plan.hosts = append(plan.hosts, host)
	}

	plans := make([]installPlan, 0, len(orderedDirs))
	for _, dir := range orderedDirs {
		plan := byDir[dir]
		installSkills, err := checkOverwrite(opts, selectedSkills, plan.dir, canPrompt)
		if err != nil {
			return nil, err
		}
		if len(installSkills) == 0 {
			fmt.Fprintf(opts.IO.ErrOut, "在 %s 中没有要为 %s 安装的技能。\n", friendlyDir(plan.dir), formatPlanHosts(plan.hosts))
			continue
		}
		plan.skills = installSkills
		plans = append(plans, *plan)
	}

	return plans, nil
}

func resolveInstallDir(opts *InstallOptions, host *registry.AgentHost, scope registry.Scope, gitRoot, homeDir string) (string, error) {
	if opts.Dir != "" {
		return opts.Dir, nil
	}
	return host.InstallDir(scope, gitRoot, homeDir)
}

func formatPlanHosts(hosts []*registry.AgentHost) string {
	names := make([]string, len(hosts))
	for i, host := range hosts {
		names[i] = host.Name
	}
	return strings.Join(names, ", ")
}

func checkOverwrite(opts *InstallOptions, skills []discovery.Skill, targetDir string, canPrompt bool) ([]discovery.Skill, error) {
	var existing, fresh []discovery.Skill
	for _, s := range skills {
		dir := filepath.Join(targetDir, s.Name)
		if _, err := os.Stat(dir); err == nil {
			existing = append(existing, s)
		} else {
			fresh = append(fresh, s)
		}
	}

	if len(existing) == 0 {
		return skills, nil
	}

	if opts.Force {
		return skills, nil
	}

	if !canPrompt {
		names := make([]string, len(existing))
		for i, s := range existing {
			names[i] = s.DisplayName()
		}
		return nil, fmt.Errorf("技能已安装: %s (使用 --force 覆盖)", strings.Join(names, ", "))
	}

	var confirmed []discovery.Skill
	for _, s := range existing {
		prompt := existingSkillPrompt(targetDir, s)
		ok, err := opts.Prompter.Confirm(prompt, false)
		if err != nil {
			return nil, err
		}
		if ok {
			confirmed = append(confirmed, s)
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "跳过 %s\n", s.DisplayName())
		}
	}

	return append(fresh, confirmed...), nil
}

func existingSkillPrompt(targetDir string, incoming discovery.Skill) string {
	skillFile := filepath.Join(targetDir, incoming.Name, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Sprintf("技能 %q 已存在。是否覆盖？", incoming.DisplayName())
	}

	result, err := frontmatter.Parse(string(data))
	if err != nil || result.Metadata.Meta == nil {
		return fmt.Sprintf("技能 %q 已存在。是否覆盖？", incoming.DisplayName())
	}

	repoInfo, _, err := source.ParseMetadataRepo(result.Metadata.Meta)
	ref, _ := result.Metadata.Meta["github-ref"].(string)
	if err != nil {
		return fmt.Sprintf("技能 %q 已存在。是否覆盖？", incoming.DisplayName())
	}

	if repoInfo != nil {
		sourceName := ghrepo.FullName(repoInfo)
		if ref != "" {
			sourceName += "@" + ref
		}
		return fmt.Sprintf("技能 %q 已从 %s 安装。是否覆盖？", incoming.DisplayName(), sourceName)
	}

	return fmt.Sprintf("技能 %q 已存在。是否覆盖？", incoming.DisplayName())
}

const installProgressLabel = "正在下载技能文件"

func installProgress(io *iostreams.IOStreams, total int) func(done, total int) {
	if total <= 0 {
		return nil
	}
	return func(done, total int) {
		if done == 0 {
			io.StartProgressIndicatorWithLabel(installProgressLabel)
		} else if done >= total {
			io.StopProgressIndicator()
		}
	}
}

func friendlyDir(dir string) string {
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return filepath.Base(dir)
			}
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "~/" + rel
		}
	}
	return dir
}

// printFileTree 以文本树形式渲染每个技能目录在磁盘上的内容。
func printFileTree(w io.Writer, cs *iostreams.ColorScheme, dir string, skillNames []string) {
	if len(skillNames) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, name := range skillNames {
		skillDir := filepath.Join(dir, filepath.FromSlash(name))
		fmt.Fprintf(w, "  %s\n", cs.Bold(name+"/"))
		printTreeDir(w, cs, skillDir, "  ")
	}
}

func printTreeDir(w io.Writer, cs *iostreams.ColorScheme, dir, indent string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(w, "%s%s\n", indent, cs.Muted("(无法读取目录)"))
		return
	}
	for i, entry := range entries {
		isLast := i == len(entries)-1
		connector := "├── "
		childIndent := "│   "
		if isLast {
			connector = "└── "
			childIndent = "    "
		}
		name := entry.Name()
		if entry.IsDir() {
			fmt.Fprintf(w, "%s%s%s\n", indent, cs.Muted(connector), cs.Bold(name+"/"))
			printTreeDir(w, cs, filepath.Join(dir, name), indent+cs.Muted(childIndent))
		} else {
			fmt.Fprintf(w, "%s%s%s\n", indent, cs.Muted(connector), name)
		}
	}
}

// printPreInstallDisclaimer 打印警告: 已安装的技能未经校验，
// 使用前应先检查其内容。
func printPreInstallDisclaimer(w io.Writer, cs *iostreams.ColorScheme) {
	fmt.Fprintf(w, "\n%s 技能未经 GitHub 校验，可能包含提示注入、隐藏指令或恶意脚本。使用前请务必检查技能内容。\n\n", cs.WarningIcon())
}

// printReviewHint reminds the user to review installed skills and suggests preview commands.
// When sha is non-empty the suggested commands include @SHA so the user previews
// exactly the version that was installed. When allowHiddenDirs is true, the
// suggested commands include --allow-hidden-dirs so previewing hidden-dir
// skills works without an extra manual step.
func printReviewHint(w io.Writer, repo, sha string, skillNames []string, allowHiddenDirs bool) {
	if len(skillNames) == 0 {
		return
	}
	fmt.Fprintln(w)
	if repo == "" {
		fmt.Fprintln(w, "  使用前请检查已安装的文件。")
		return
	}
	fmt.Fprintln(w, "  使用前请检查已安装的内容: ")
	fmt.Fprintln(w)
	hiddenFlag := ""
	if allowHiddenDirs {
		hiddenFlag = " --allow-hidden-dirs"
	}
	for _, name := range skillNames {
		if sha != "" {
			fmt.Fprintf(w, "    gh skill preview %s %s@%s%s\n", repo, name, sha, hiddenFlag)
		} else {
			fmt.Fprintf(w, "    gh skill preview %s %s%s\n", repo, name, hiddenFlag)
		}
	}
	fmt.Fprintln(w)
}

// printHostHints 为安装目标宿主打印特定于 agent 的安装后指引。
// 大多数 agent 无需额外步骤；目前仅用于 Kiro CLI，它要求将技能
// 注册为自定义 agent 上的资源。示例中的路径派生自实际安装目录，
// 因此与所选作用域或 --dir 一致。
func printHostHints(w io.Writer, cs *iostreams.ColorScheme, hosts []*registry.AgentHost, installed []string, installDir, gitRoot string) {
	if len(installed) == 0 {
		return
	}
	for _, h := range hosts {
		if h.ID == "kiro-cli" {
			fmt.Fprintln(w)
			fmt.Fprint(w, heredoc.Docf(`
				%s Kiro CLI: 通过将技能添加到 .kiro/agents/<agent>.json 的
				  "resources" 字段，将这些技能注册到自定义 agent 上，例如:

				    {
				      "resources": ["skill://%s/**/SKILL.md"]
				    }
			`, cs.WarningIcon(), kiroResourcePath(installDir, gitRoot)))
			fmt.Fprintln(w)
			return
		}
	}
}

// kiroResourcePath 返回一个以斜杠分隔、适合用于 Kiro agent 配置中
// "resources" 字段的路径。当安装目录位于当前 git 仓库内时，路径会被
// 转换为相对于仓库根目录的形式，以适配项目作用域的 agent 配置；
// 否则使用绝对安装路径 (例如 --scope user 或 --dir 时)。
func kiroResourcePath(installDir, gitRoot string) string {
	if gitRoot != "" && installDir != "" {
		if rel, err := filepath.Rel(gitRoot, installDir); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(installDir)
}

// filterHiddenDirSkills 应用 --allow-hidden-dirs 标志的逻辑。设置该标志时，
// 返回所有技能并给出警告；否则排除隐藏目录中的技能，若没有剩余的
// 标准技能则返回错误。
func filterHiddenDirSkills(opts *InstallOptions, allSkills []discovery.Skill) ([]discovery.Skill, error) {
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
	if len(r.Standard) == 0 && r.HiddenCount > 0 {
		return nil, fmt.Errorf(
			"未找到标准技能，但存在 %d 个位于隐藏目录中的技能\n"+
				"  使用 --allow-hidden-dirs 可包含它们",
			r.HiddenCount,
		)
	}

	return r.Standard, nil
}

// checkUpstreamProvenance 通过 contents API 获取技能的 SKILL.md，
// 检查其中是否包含指向其他仓库的 github-repo 元数据，若是则说明
// 该技能是从上游来源重新发布的。交互模式下会询问用户是从重新
// 发布者安装还是重定向到上游；非交互模式始终从重新发布者安装。
// 返回 (要重定向到的仓库，是否检测到上游，错误)。
func checkUpstreamProvenance(opts *InstallOptions, client *api.Client, hostname string, skill discovery.Skill, commitSHA string) (ghrepo.Interface, bool, error) {
	u, err := safeurl.JoinPath("repos", opts.repo.RepoOwner(), opts.repo.RepoName(), "contents", skill.Path+"/SKILL.md")
	if err != nil {
		return nil, false, err
	}
	u.SetQuery("ref", commitSHA)
	var fileResp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := client.REST(hostname, "GET", u.String(), nil, &fileResp); err != nil {
		return nil, false, nil //nolint:nilerr // best-effort check; failing to fetch is not fatal
	}
	if fileResp.Encoding != "base64" {
		return nil, false, nil
	}
	decoded, decodeErr := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(fileResp.Content)))
	if decodeErr != nil {
		return nil, false, nil //nolint:nilerr // best-effort; decode failure is not fatal
	}
	content := string(decoded)

	result, parseErr := frontmatter.Parse(content)
	if parseErr != nil || result.Metadata.Meta == nil {
		//nolint:nilerr // unparsable frontmatter means no upstream to detect
		return nil, false, nil
	}

	existingRepo, _ := result.Metadata.Meta["github-repo"].(string)
	if existingRepo == "" {
		return nil, false, nil
	}

	currentRepoURL := source.BuildRepoURL(hostname, opts.repo.RepoOwner(), opts.repo.RepoName())
	if existingRepo == currentRepoURL {
		return nil, false, nil
	}

	upstreamRepo, parseErr := source.ParseRepoURL(existingRepo)
	if parseErr != nil {
		//nolint:nilerr // invalid repo URL means we can't redirect; install normally
		return nil, false, nil
	}

	cs := opts.IO.ColorScheme()
	upstreamLabel := ghrepo.FullName(upstreamRepo)
	repoSource := ghrepo.FullName(opts.repo)

	fmt.Fprintf(opts.IO.ErrOut, "%s 该技能最初发布于 %s\n", cs.WarningIcon(), upstreamLabel)

	if opts.Upstream {
		fmt.Fprintf(opts.IO.ErrOut, "正在将安装重定向到 %s...\n", upstreamLabel)
		return upstreamRepo, true, nil
	}

	if !opts.IO.CanPrompt() {
		fmt.Fprintf(opts.IO.ErrOut, "  正在从 %s 安装 (使用 --upstream 或交互模式可选择上游)\n", repoSource)
		return nil, true, nil
	}

	choices := []string{
		fmt.Sprintf("%s (重新发布者，推荐)", repoSource),
		fmt.Sprintf("%s (上游)", upstreamLabel),
	}
	idx, err := opts.Prompter.Select("安装来源:", "", choices)
	if err != nil {
		return nil, true, err
	}

	if idx == 1 {
		fmt.Fprintf(opts.IO.ErrOut, "正在将安装重定向到 %s...\n", upstreamLabel)
		return upstreamRepo, true, nil
	}

	return nil, true, nil
}
