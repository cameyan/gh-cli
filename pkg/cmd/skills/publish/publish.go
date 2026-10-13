package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/skills/discovery"
	"github.com/cli/cli/v2/internal/skills/frontmatter"
	"github.com/cli/cli/v2/internal/skills/registry"
	"github.com/cli/cli/v2/internal/skills/source"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// PublishOptions 保存 publish 命令的所有依赖项与用户提供的标志。
type PublishOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	Prompter   prompter.Prompter
	GitClient  *git.Client

	Dir    string
	Fix    bool
	DryRun bool
	Tag    string

	host string // resolved from config in production
}

// publishDiagnostic 是单条校验发现。
type publishDiagnostic struct {
	skill    string // empty for repo-level issues
	severity string // "error", "warning", "fixed", or "info"
	message  string
}

// repoTopicsResponse is the response from the repo topics API.
type repoTopicsResponse struct {
	Names []string `json:"names"`
}

// tagEntry is a single tag from the tags list API.
type tagEntry struct {
	Name string `json:"name"`
}

// rulesetsResponse is a single ruleset from the rulesets API.
type rulesetsResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
}

// securityAnalysis represents the security_and_analysis field from the repo API.
type securityAnalysis struct {
	AdvancedSecurity             *securityFeature `json:"advanced_security"`
	SecretScanning               *securityFeature `json:"secret_scanning"`
	SecretScanningPushProtection *securityFeature `json:"secret_scanning_push_protection"`
}

type securityFeature struct {
	Status string `json:"status"`
}

// repoSecurityResponse is the subset of repo API we need for security checks.
type repoSecurityResponse struct {
	SecurityAndAnalysis *securityAnalysis `json:"security_and_analysis"`
}

// NewCmdPublish 创建 "skills publish" 命令。
func NewCmdPublish(f *cmdutil.Factory, runF func(*PublishOptions) error) *cobra.Command {
	opts := &PublishOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
		GitClient:  f.GitClient,
	}

	cmd := &cobra.Command{
		Use:   "publish [<directory>] [flags]",
		Short: "校验技能并发布到 GitHub 仓库 (预览版)",
		Long: heredoc.Docf(`
			依据 Agent Skills 规范校验本地仓库中的技能，
			并通过创建 GitHub release 发布它们。

			技能的发现方式与 install 相同:

			  - %[1]sskills/*/SKILL.md%[1]s
			  - %[1]sskills/{scope}/*/SKILL.md%[1]s
			  - %[1]s*/SKILL.md%[1]s (根目录)
			  - %[1]splugins/{scope}/skills/*/SKILL.md%[1]s

			校验项包括:

			  - 技能名称符合 agentskills.io 严格命名规则
			  - 每个技能名称与其目录名一致
			  - 必需的 frontmatter 字段 (name、description) 存在
			  - allowed-tools 是字符串而非数组
			  - 安装元数据 (%[1]smetadata.github-*%[1]s) 若存在则会被剥离

			校验通过后，publish 将交互式引导你完成:

			  - 为仓库添加 %[1]sagent-skills%[1]s 主题
			  - 选择版本标签 (推荐 semver)
			  - 创建带自动生成说明的 GitHub release

			使用 %[1]s--dry-run%[1]s 仅校验而不发布。
			使用 %[1]s--tag%[1]s 以指定标签非交互发布。
			使用 %[1]s--fix%[1]s 自动剥离已提交文件中的安装元数据而不发布。
			检查并提交变更后再次运行 publish。
		`, "`"),
		Example: heredoc.Doc(`
			# 交互式校验并发布
			$ gh skill publish

			# 以指定标签发布 (非交互)
			$ gh skill publish --tag v1.0.0

			# 仅校验 (不发布)
			$ gh skill publish --dry-run

			# 剥离安装元数据而不发布
			$ gh skill publish --fix
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Dir = args[0]
			}
			if err := cmdutil.MutuallyExclusive("只能指定 --fix 或 --dry-run 之一", opts.Fix, opts.DryRun); err != nil {
				return err
			}
			if runF != nil {
				return runF(opts)
			}
			return publishRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Fix, "fix", false, "在不发布的前提下尽可能自动修复问题 (如剥离安装元数据)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "仅校验而不发布")
	cmd.Flags().StringVar(&opts.Tag, "tag", "", "release 的版本标签 (如 v1.0.0)")

	return cmd
}

func publishRun(opts *PublishOptions) error {
	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("无法确定工作目录: %w", err)
		}
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("无法解析路径: %w", err)
	}

	canPrompt := opts.IO.CanPrompt()

	// Client initialization is deferred until after local validation so that
	// simple errors (missing skills/, bad SKILL.md, etc.) are reported
	// without requiring an HTTP client.
	var client *api.Client
	host := opts.host

	var diagnostics []publishDiagnostic

	skills, err := discovery.DiscoverLocalSkills(dir)
	if err != nil {
		return err
	}

	for _, skill := range skills {
		dirName := path.Base(skill.Path)
		skillPath := filepath.Join(dir, filepath.FromSlash(skill.Path), "SKILL.md")
		content, err := os.ReadFile(skillPath)
		if err != nil {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "error",
				message:  "缺少 SKILL.md 文件",
			})
			continue
		}

		result, err := frontmatter.Parse(string(content))
		if err != nil {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "error",
				message:  fmt.Sprintf("frontmatter YAML 无效: %s", err),
			})
			continue
		}

		// Validate name field exists
		if result.Metadata.Name == "" {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "error",
				message:  "缺少必需字段: name",
			})
		} else {
			// Validate name matches directory
			if result.Metadata.Name != dirName {
				diagnostics = append(diagnostics, publishDiagnostic{
					skill:    skill.DisplayName(),
					severity: "error",
					message:  fmt.Sprintf("name %q 与目录名 %q 不一致", result.Metadata.Name, dirName),
				})
			}

			// Validate name is spec-compliant
			if !discovery.IsSpecCompliant(result.Metadata.Name) {
				diagnostics = append(diagnostics, publishDiagnostic{
					skill:    skill.DisplayName(),
					severity: "error",
					message:  fmt.Sprintf("name %q 不符合 agentskills.io 命名规范 (小写字母数字加连字符)", result.Metadata.Name),
				})
			}
		}

		// Validate description field exists
		if result.Metadata.Description == "" {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "error",
				message:  "缺少必需字段: description",
			})
		} else if len(result.Metadata.Description) > 1024 {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "warning",
				message:  fmt.Sprintf("description 为 %d 个字符 (建议上限: 1024)", len(result.Metadata.Description)),
			})
		}

		// Validate allowed-tools is string, not array
		if raw, ok := result.RawYAML["allowed-tools"]; ok {
			if _, isSlice := raw.([]any); isSlice {
				diagnostics = append(diagnostics, publishDiagnostic{
					skill:    skill.DisplayName(),
					severity: "error",
					message:  "allowed-tools 必须是字符串 (以空格分隔)，不能是数组",
				})
			}
		}

		// Check for install metadata that should be stripped
		if meta, ok := result.RawYAML["metadata"].(map[string]any); ok {
			githubKeys := findGitHubMetadataKeys(meta)
			if len(githubKeys) > 0 {
				if opts.Fix {
					fixed, fixErr := stripGitHubMetadata(string(content))
					if fixErr != nil {
						diagnostics = append(diagnostics, publishDiagnostic{
							skill:    skill.DisplayName(),
							severity: "error",
							message:  fmt.Sprintf("无法剥离安装元数据: %s", fixErr),
						})
					} else if writeErr := os.WriteFile(skillPath, []byte(fixed), 0o644); writeErr != nil {
						diagnostics = append(diagnostics, publishDiagnostic{
							skill:    skill.DisplayName(),
							severity: "error",
							message:  fmt.Sprintf("无法写入修复后的 SKILL.md: %s", writeErr),
						})
					} else {
						diagnostics = append(diagnostics, publishDiagnostic{
							skill:    skill.DisplayName(),
							severity: "fixed",
							message:  fmt.Sprintf("已剥离安装元数据: %s", strings.Join(githubKeys, ", ")),
						})
					}
				} else {
					diagnostics = append(diagnostics, publishDiagnostic{
						skill:    skill.DisplayName(),
						severity: "error",
						message:  fmt.Sprintf("包含必须剥离的安装元数据: %s (使用 --fix)", strings.Join(githubKeys, ", ")),
					})
				}
			}
		}

		// Recommended: license field
		if result.Metadata.License == "" {
			if _, ok := result.RawYAML["license"]; !ok {
				diagnostics = append(diagnostics, publishDiagnostic{
					skill:    skill.DisplayName(),
					severity: "warning",
					message:  "缺少建议字段: license",
				})
			}
		}

		// Recommended: body length
		bodyLines := strings.Count(result.Body, "\n") + 1
		if bodyLines > 500 {
			diagnostics = append(diagnostics, publishDiagnostic{
				skill:    skill.DisplayName(),
				severity: "warning",
				message:  fmt.Sprintf("技能正文为 %d 行 (建议上限: 500 行以保证上下文高效)", bodyLines),
			})
		}
	}

	// Check for installed skill directories that should be gitignored
	installedDirDiags := checkInstalledSkillDirs(opts.GitClient, dir)
	diagnostics = append(diagnostics, installedDirDiags...)

	// Remote repository checks (best-effort)
	repoInfo, remoteErr := detectGitHubRemote(opts.GitClient, dir)
	if remoteErr != nil {
		return remoteErr
	}
	owner, repo := "", ""
	if repoInfo != nil {
		owner = repoInfo.Repo.RepoOwner()
		repo = repoInfo.Repo.RepoName()
	}

	hasTopic := false
	var existingTags []tagEntry
	if owner != "" && repo != "" {
		httpClient, err := opts.HttpClient()
		if err != nil {
			return err
		}
		client = api.NewClientFromHTTP(httpClient)

		if host == "" && repoInfo != nil {
			host = repoInfo.Repo.RepoHost()
		}
		if host == "" {
			cfg, err := opts.Config()
			if err != nil {
				return err
			}
			host, _ = cfg.Authentication().DefaultHost()
		}
		if err := source.ValidateSupportedHost(host); err != nil {
			return err
		}

		// Security and ruleset checks (advisory, always shown)
		var skillAbsDirs []string
		for _, skill := range skills {
			skillAbsDirs = append(skillAbsDirs, filepath.Join(dir, filepath.FromSlash(skill.Path)))
		}
		securityDiags := checkSecuritySettings(client, host, owner, repo, skillAbsDirs)
		diagnostics = append(diagnostics, securityDiags...)

		rulesetDiags := checkTagProtection(client, host, owner, repo)
		diagnostics = append(diagnostics, rulesetDiags...)

		// Check topic (needed for publish flow, not a blocking error)
		hasTopic = repoHasTopic(client, host, owner, repo)

		// Fetch existing tags (needed for version suggestion)
		existingTags = fetchTags(client, host, owner, repo)
	} else {
		diagnostics = append(diagnostics, detectMissingRepoDiagnostic(opts.GitClient, dir)...)
	}

	// Render diagnostics
	errors, warnings, fixes := 0, 0, 0
	for _, d := range diagnostics {
		switch d.severity {
		case "error":
			errors++
		case "warning":
			warnings++
		case "fixed":
			fixes++
		}
	}

	if canPrompt {
		renderDiagnosticsTTY(opts, len(skills), diagnostics, errors, warnings, fixes, owner, repo)
	} else {
		renderDiagnosticsPlain(opts, diagnostics, errors, warnings)
	}

	if errors > 0 {
		return fmt.Errorf("校验失败，共 %d 个错误", errors)
	}

	// --- Publish flow ---
	if opts.DryRun {
		fmt.Fprintf(opts.IO.ErrOut, "\n试运行完成。去掉 --dry-run 即可发布。\n")
		return nil
	}

	if opts.Fix {
		if fixes > 0 {
			fmt.Fprintf(opts.IO.ErrOut, "\n已修复 %d 个文件。检查并提交变更，然后运行 %s 发布。\n", fixes, "gh skill publish")
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "\n没有需要修复的问题。\n")
		}
		return nil
	}

	if owner == "" || repo == "" {
		fmt.Fprintf(opts.IO.ErrOut, "\n校验通过。配置 GitHub 远程仓库后即可发布。\n")
		return nil
	}

	if !canPrompt && opts.Tag == "" {
		fmt.Fprintf(opts.IO.ErrOut, "\n校验通过。使用 --tag 进行非交互发布。\n")
		return nil
	}

	fmt.Fprintf(opts.IO.ErrOut, "\n正在发布到 %s/%s...\n\n", owner, repo)

	return runPublishRelease(opts, client, host, owner, repo, dir, repoInfo.RemoteName, hasTopic, existingTags)
}

// repoHasTopic checks whether the repo has the agent-skills topic.
func repoHasTopic(client *api.Client, host, owner, repo string) bool {
	if client == nil {
		return false
	}
	apiPath, err := safeurl.JoinPath("repos", owner, repo, "topics")
	if err != nil {
		return false
	}
	var resp repoTopicsResponse
	if err := client.REST(host, "GET", apiPath.String(), nil, &resp); err != nil {
		return false
	}
	return slices.Contains(resp.Names, "agent-skills")
}

// fetchTags returns the most recent tags from the repo.
func fetchTags(client *api.Client, host, owner, repo string) []tagEntry {
	if client == nil {
		return nil
	}
	u, err := safeurl.JoinPath("repos", owner, repo, "tags")
	if err != nil {
		return nil
	}
	u.SetQuery("per_page", "10")
	var tags []tagEntry
	if err := client.REST(host, "GET", u.String(), nil, &tags); err != nil {
		return nil
	}
	return tags
}

// runPublishRelease 处理交互式发布流程: 主题、标签、release、不可变性。
func runPublishRelease(opts *PublishOptions, client *api.Client, host, owner, repo, dir, remoteName string, hasTopic bool, existingTags []tagEntry) error {
	cs := opts.IO.ColorScheme()
	canPrompt := opts.IO.CanPrompt()

	// Add topic if missing
	if !hasTopic {
		addTopic := true
		if canPrompt {
			var err error
			addTopic, err = opts.Prompter.Confirm(
				fmt.Sprintf("为 %s/%s 添加 \"agent-skills\" 主题？ (发现该仓库所必需)", owner, repo), true)
			if err != nil {
				return err
			}
		}
		if addTopic {
			if err := addAgentSkillsTopic(client, host, owner, repo); err != nil {
				fmt.Fprintf(opts.IO.ErrOut, "%s 无法添加主题: %v\n", cs.WarningIcon(), err)
				fmt.Fprintf(opts.IO.ErrOut, "  可手动添加: gh repo edit %s/%s --add-topic agent-skills\n", owner, repo)
			} else {
				fmt.Fprintf(opts.IO.Out, "%s 已添加 \"agent-skills\" 主题\n", cs.SuccessIcon())
			}
		}
	}

	// Push unpushed commits (like gh pr create)
	if err := ensurePushed(opts, dir, remoteName); err != nil {
		return err
	}

	// Determine tag
	tag := opts.Tag
	if tag == "" {
		suggested := "v1.0.0"
		if len(existingTags) > 0 {
			if next := suggestNextTag(existingTags[0].Name); next != "" {
				suggested = next
			}
		}

		if canPrompt {
			strategies := []string{
				fmt.Sprintf("Semver (推荐): %s", suggested),
				"自定义标签",
			}
			idx, err := opts.Prompter.Select("标签策略:", "", strategies)
			if err != nil {
				return err
			}

			if idx == 0 {
				tag = suggested
				edited, err := opts.Prompter.Input(fmt.Sprintf("版本标签 [%s]:", suggested), suggested)
				if err != nil {
					return err
				}
				if edited != "" {
					tag = edited
				}
			} else {
				custom, err := opts.Prompter.Input("标签:", "")
				if err != nil {
					return err
				}
				if custom == "" {
					return fmt.Errorf("标签不能为空")
				}
				tag = custom
			}
		} else {
			return fmt.Errorf("非交互发布必须提供 --tag")
		}
	}

	// Validate tag doesn't already exist
	for _, t := range existingTags {
		if t.Name == tag {
			return fmt.Errorf("标签 %s 已存在；请选择其他版本", tag)
		}
	}

	// Offer to enable immutable releases
	immutableEnabled := checkImmutableReleases(client, host, owner, repo)
	if !immutableEnabled && canPrompt {
		enableImmutable, err := opts.Prompter.Confirm(
			"启用不可变 release？ (防止已发布的 release 被篡改)", true)
		if err != nil {
			return err
		}
		if enableImmutable {
			if err := enableImmutableReleases(client, host, owner, repo); err != nil {
				fmt.Fprintf(opts.IO.ErrOut, "%s 无法启用不可变 release: %v\n", cs.WarningIcon(), err)
				fmt.Fprintf(opts.IO.ErrOut, "  可在 Settings > General > Releases 中手动启用\n")
			} else {
				fmt.Fprintf(opts.IO.Out, "%s 已启用不可变 release\n", cs.SuccessIcon())
			}
		}
	}

	// Inform if not on default branch
	var currentBranch string
	if opts.GitClient != nil {
		branchGitClient := opts.GitClient.Copy()
		branchGitClient.RepoDir = dir
		if b, err := branchGitClient.CurrentBranch(context.Background()); err == nil {
			currentBranch = b
		}
	}
	defaultBranch := detectDefaultBranch(client, host, owner, repo)
	if currentBranch != "" && defaultBranch != "" && currentBranch != defaultBranch {
		fmt.Fprintf(opts.IO.ErrOut, "%s 正在从分支 %q 发布 (默认分支为 %q)\n", cs.WarningIcon(), currentBranch, defaultBranch)
	}

	// Confirm and create release
	if canPrompt {
		confirmed, err := opts.Prompter.Confirm(
			fmt.Sprintf("使用自动生成的说明创建 release %s？", tag), true)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintf(opts.IO.ErrOut, "发布已取消。\n")
			return cmdutil.CancelError
		}
	}

	// Create release via REST API
	releaseBody := map[string]any{
		"tag_name":               tag,
		"generate_release_notes": true,
	}
	if currentBranch != "" {
		releaseBody["target_commitish"] = currentBranch
	}
	releaseJSON, err := json.Marshal(releaseBody)
	if err != nil {
		return fmt.Errorf("无法序列化 release 请求: %w", err)
	}

	releasePath, err := safeurl.JoinPath("repos", owner, repo, "releases")
	if err != nil {
		return err
	}
	var releaseResp struct {
		HTMLURL string `json:"html_url"`
	}
	if err := client.REST(host, "POST", releasePath.String(), bytes.NewReader(releaseJSON), &releaseResp); err != nil {
		return fmt.Errorf("创建 release 失败: %w", err)
	}

	fmt.Fprintf(opts.IO.Out, "%s 已发布 %s\n", cs.SuccessIcon(), tag)
	fmt.Fprintf(opts.IO.Out, "%s 安装命令: gh skill install %s/%s\n", cs.SuccessIcon(), owner, repo)
	fmt.Fprintf(opts.IO.Out, "%s 固定版本:   gh skill install %s/%s <skill> --pin %s\n", cs.SuccessIcon(), owner, repo, tag)

	return nil
}

// ensurePushed 检查当前分支是否有未推送的提交并自动推送，
// 与 gh pr create 的行为保持一致。
func ensurePushed(opts *PublishOptions, dir, remoteName string) error {
	if opts.GitClient == nil {
		return nil
	}

	cs := opts.IO.ColorScheme()
	gitClient := opts.GitClient.Copy()
	gitClient.RepoDir = dir

	ctx := context.Background()
	currentBranch, err := gitClient.CurrentBranch(ctx)
	if err != nil {
		return nil //nolint:nilerr // not on a branch (detached HEAD); skip push check
	}

	// Count commits ahead of the push target (remote tracking branch).
	// If the branch has no upstream, rev-list will fail; we treat that as
	// "everything is unpushed" and push the whole branch.
	// 统计领先于推送目标 (远程跟踪分支) 的提交数。
	// 若分支没有上游，rev-list 会失败；此时视为"全部未推送"，
	// 推送整个分支。
	unpushed := 0
	revCmd, err := gitClient.Command(ctx, "rev-list", "--count", "@{push}..HEAD")
	if err != nil {
		return fmt.Errorf("无法检查未推送的提交: %w", err)
	}
	out, revErr := revCmd.Output()
	if revErr != nil {
		// @{push} not resolvable; branch has never been pushed
		// @{push} 无法解析；该分支从未推送过
		unpushed = -1
	} else {
		n, parseErr := strconv.Atoi(strings.TrimSpace(string(out)))
		if parseErr != nil {
			return fmt.Errorf("无法解析未推送提交数: %w", parseErr)
		}
		unpushed = n
	}

	if unpushed == 0 {
		return nil
	}

	ref := fmt.Sprintf("HEAD:refs/heads/%s", currentBranch)
	fmt.Fprintf(opts.IO.ErrOut, "正在推送 %s 到 %s...\n", currentBranch, remoteName)
	if err := gitClient.Push(ctx, remoteName, ref); err != nil {
		return fmt.Errorf("推送分支 %s 失败: %w", currentBranch, err)
	}
	fmt.Fprintf(opts.IO.ErrOut, "%s 已推送 %s 到 %s\n", cs.SuccessIcon(), currentBranch, remoteName)

	return nil
}

// detectDefaultBranch 通过 API 返回远程仓库的默认分支。
func detectDefaultBranch(client *api.Client, host, owner, repo string) string {
	if client == nil {
		return ""
	}
	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	apiPath, err := safeurl.JoinPath("repos", owner, repo)
	if err != nil {
		return ""
	}
	if err := client.REST(host, "GET", apiPath.String(), nil, &result); err != nil {
		return ""
	}
	return result.DefaultBranch
}

// addAgentSkillsTopic 为仓库添加 "agent-skills" 主题，保留已有主题。
func addAgentSkillsTopic(client *api.Client, host, owner, repo string) error {
	apiPath, err := safeurl.JoinPath("repos", owner, repo, "topics")
	if err != nil {
		return err
	}

	// Fetch existing topics
	var resp repoTopicsResponse
	if err := client.REST(host, "GET", apiPath.String(), nil, &resp); err != nil {
		return fmt.Errorf("无法获取已有主题: %w", err)
	}

	// Deduplicate: only add if not already present
	if slices.Contains(resp.Names, "agent-skills") {
		return nil
	}

	topics := append(resp.Names, "agent-skills")
	topicsJSON, err := json.Marshal(map[string][]string{"names": topics})
	if err != nil {
		return fmt.Errorf("无法序列化主题: %w", err)
	}
	return client.REST(host, "PUT", apiPath.String(), bytes.NewReader(topicsJSON), nil)
}

// checkImmutableReleases 检查仓库是否启用了不可变 release。
func checkImmutableReleases(client *api.Client, host, owner, repo string) bool {
	if client == nil {
		return false
	}
	apiPath, err := safeurl.JoinPath("repos", owner, repo, "immutable-releases")
	if err != nil {
		return false
	}
	var resp struct {
		Enabled bool `json:"enabled"`
	}
	if err := client.REST(host, "GET", apiPath.String(), nil, &resp); err != nil {
		return false
	}
	return resp.Enabled
}

// enableImmutableReleases 为仓库启用不可变 release。
func enableImmutableReleases(client *api.Client, host, owner, repo string) error {
	apiPath, err := safeurl.JoinPath("repos", owner, repo, "immutable-releases")
	if err != nil {
		return err
	}
	body := bytes.NewReader([]byte(`{"enabled":true}`))
	return client.REST(host, "PATCH", apiPath.String(), body, nil)
}

// checkTagProtection 检查是否启用了标签保护规则集。
func checkTagProtection(client *api.Client, host, owner, repo string) []publishDiagnostic {
	if client == nil {
		return nil
	}
	apiPath, err := safeurl.JoinPath("repos", owner, repo, "rulesets")
	if err != nil {
		return nil
	}
	var rulesets []rulesetsResponse
	if err := client.REST(host, "GET", apiPath.String(), nil, &rulesets); err != nil {
		return nil
	}

	for _, rs := range rulesets {
		if rs.Target == "tag" && rs.Enforcement == "active" {
			return nil
		}
	}

	return []publishDiagnostic{{
		severity: "warning",
		message:  "未找到生效的标签保护规则集。建议保护标签以确保 release 不可变 (Settings > Rules > Rulesets)",
	}}
}

// checkSecuritySettings 检查是否启用了推荐的安全功能。
func checkSecuritySettings(client *api.Client, host, owner, repo string, skillDirs []string) []publishDiagnostic {
	if client == nil {
		return nil
	}
	apiPath, err := safeurl.JoinPath("repos", owner, repo)
	if err != nil {
		return nil
	}
	var resp repoSecurityResponse
	if err := client.REST(host, "GET", apiPath.String(), nil, &resp); err != nil {
		return nil
	}

	if resp.SecurityAndAnalysis == nil {
		return nil
	}

	var diagnostics []publishDiagnostic
	sa := resp.SecurityAndAnalysis

	if sa.SecretScanning == nil || sa.SecretScanning.Status != "enabled" {
		diagnostics = append(diagnostics, publishDiagnostic{
			severity: "warning",
			message:  "未启用 secret scanning。建议启用以防止凭据意外泄露 (gh repo edit --enable-secret-scanning)",
		})
	}

	if sa.SecretScanningPushProtection == nil || sa.SecretScanningPushProtection.Status != "enabled" {
		diagnostics = append(diagnostics, publishDiagnostic{
			severity: "warning",
			message:  "未启用 secret scanning 推送保护。该功能可阻止包含密钥的推送 (gh repo edit --enable-secret-scanning-push-protection)",
		})
	}

	hasCode, hasManifests := detectCodeAndManifests(skillDirs)

	if hasCode {
		if u, err := safeurl.JoinPath("repos", owner, repo, "code-scanning", "alerts"); err == nil {
			u.SetQuery("per_page", "1")
			u.SetQuery("state", "open")
			if err := client.REST(host, "GET", u.String(), nil, new([]any)); err != nil {
				diagnostics = append(diagnostics, publishDiagnostic{
					severity: "info",
					message:  "技能包含代码文件，但似乎未配置 code scanning (Settings > Code security > Code scanning)",
				})
			}
		}
	}

	if hasManifests {
		if dependabotPath, err := safeurl.JoinPath("repos", owner, repo, "vulnerability-alerts"); err == nil {
			if err := client.REST(host, "GET", dependabotPath.String(), nil, nil); err != nil {
				diagnostics = append(diagnostics, publishDiagnostic{
					severity: "info",
					message:  "技能包含依赖清单，但似乎未启用 Dependabot 警报 (Settings > Code security > Dependabot)",
				})
			}
		}
	}

	return diagnostics
}

// codeExtensions are file extensions that indicate code is present.
var codeExtensions = map[string]bool{
	".go": true, ".py": true, ".js": true, ".ts": true, ".rb": true,
	".rs": true, ".java": true, ".cs": true, ".sh": true, ".bash": true,
	".zsh": true, ".ps1": true, ".swift": true, ".kt": true, ".c": true,
	".cpp": true, ".h": true, ".php": true, ".pl": true, ".lua": true,
}

// manifestFiles are dependency manifest filenames.
var manifestFiles = map[string]bool{
	"package.json": true, "package-lock.json": true, "yarn.lock": true,
	"go.mod": true, "go.sum": true, "Cargo.toml": true, "Cargo.lock": true,
	"requirements.txt": true, "Pipfile": true, "Pipfile.lock": true,
	"pyproject.toml": true, "poetry.lock": true, "Gemfile": true,
	"Gemfile.lock": true, "pom.xml": true, "build.gradle": true,
	"composer.json": true, "composer.lock": true,
}

// detectCodeAndManifests 遍历技能目录，查找代码文件与依赖清单。
func detectCodeAndManifests(skillDirs []string) (hasCode, hasManifests bool) {
	for _, dir := range skillDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			ext := filepath.Ext(info.Name())
			if codeExtensions[ext] {
				hasCode = true
			}
			if manifestFiles[info.Name()] {
				hasManifests = true
			}
			if hasCode && hasManifests {
				// Stop walking this skill directory early; the outer loop
				// continues to process remaining skill directories.
				return filepath.SkipAll
			}
			return nil
		})
		if hasCode && hasManifests {
			return
		}
	}
	return
}

// checkInstalledSkillDirs 当仓库中存在 agent 宿主技能目录且未被
// gitignore 时给出警告。
func checkInstalledSkillDirs(gitClient *git.Client, repoDir string) []publishDiagnostic {
	var diagnostics []publishDiagnostic

	for _, relPath := range registry.UniqueProjectDirs() {
		// Skip non-hidden project dirs (such as "skills") to avoid
		// flagging the canonical authoring layout used when publishing.
		if !strings.HasPrefix(relPath, ".") {
			continue
		}
		absPath := filepath.Join(repoDir, relPath)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			continue
		}

		if gitClient != nil {
			ignoreGitClient := gitClient.Copy()
			ignoreGitClient.RepoDir = repoDir
			ignored, err := ignoreGitClient.IsIgnored(context.Background(), relPath)
			if ignored {
				continue
			}
			if err != nil {
				diagnostics = append(diagnostics, publishDiagnostic{
					severity: "warning",
					message:  fmt.Sprintf("%s/ 可能包含未 gitignore 的已安装技能 (无法验证: %v)", relPath, err),
				})
				continue
			}
		}

		diagnostics = append(diagnostics, publishDiagnostic{
			severity: "warning",
			message: fmt.Sprintf(
				"%s/ 包含已安装的技能，应加入 .gitignore 以避免发布其他作者的内容",
				relPath),
		})
	}

	return diagnostics
}

// semverPattern matches v-prefixed semver tags (e.g. v1.2.3).
var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// suggestNextTag increments the patch version of a semver tag.
func suggestNextTag(latest string) string {
	m := semverPattern.FindStringSubmatch(latest)
	if m == nil {
		return ""
	}

	prefix := ""
	if strings.HasPrefix(latest, "v") {
		prefix = "v"
	}

	major, minor := m[1], m[2]
	patch := 0
	fmt.Sscanf(m[3], "%d", &patch)

	return fmt.Sprintf("%s%s.%s.%d", prefix, major, minor, patch+1)
}

// gitHubRemote holds a detected GitHub remote and its local name.
type gitHubRemote struct {
	Repo       ghrepo.Interface
	RemoteName string
}

// detectGitHubRemote 尝试从给定目录的 git 远程中检测 GitHub owner/repo。
// 按 gitClient.Remotes 返回的顺序尝试 (upstream > github > origin > 其余)，
// 因此第一个指向 GitHub 的远程生效。
func detectGitHubRemote(gitClient *git.Client, dir string) (*gitHubRemote, error) {
	if gitClient == nil {
		return nil, nil
	}

	dirClient := gitClient.Copy()
	dirClient.RepoDir = dir

	remotes, err := dirClient.Remotes(context.Background())
	if err != nil {
		return nil, nil //nolint:nilerr // failing to list remotes is not an error; it just means no repo detected
	}
	for _, r := range remotes {
		if url, err := dirClient.RemoteURL(context.Background(), r.Name); err == nil {
			repo, parseErr := parseGitHubURL(url)
			if parseErr != nil {
				return nil, parseErr
			}
			if repo != nil {
				return &gitHubRemote{Repo: repo, RemoteName: r.Name}, nil
			}
		}
	}
	return nil, nil
}

// parseGitHubURL 从 GitHub 远程 URL 中提取 owner/repo。
// 仅识别 github.com 和 GHEC 数据驻留 (*.ghe.com) URL。
func parseGitHubURL(rawURL string) (ghrepo.Interface, error) {
	u, err := git.ParseURL(rawURL)
	if err != nil {
		return nil, nil //nolint:nilerr // unparsable URL means it's not a GitHub remote
	}
	r, err := ghrepo.FromURL(u)
	if err != nil {
		return nil, nil //nolint:nilerr // URL didn't match GitHub repo format
	}
	if err := source.ValidateSupportedHost(r.RepoHost()); err != nil {
		return nil, nil //nolint:nilerr // non-GitHub host is silently ignored
	}
	return r, nil
}

// detectMissingRepoDiagnostic 说明跳过远程检查的原因。
func detectMissingRepoDiagnostic(gitClient *git.Client, dir string) []publishDiagnostic {
	if gitClient == nil {
		return nil
	}

	dirGitClient := gitClient.Copy()
	dirGitClient.RepoDir = dir
	if _, err := dirGitClient.GitDir(context.Background()); err != nil {
		return []publishDiagnostic{{
			severity: "warning",
			message:  "不是 git 仓库。请先初始化: git init && gh repo create",
		}}
	}

	remotes, err := dirGitClient.Remotes(context.Background())
	if err != nil || len(remotes) == 0 {
		return []publishDiagnostic{{
			severity: "warning",
			message:  "未找到 git 远程。可使用以下命令创建 GitHub 仓库: gh repo create",
		}}
	}

	var urls []string
	for _, r := range remotes {
		if url, err := dirGitClient.RemoteURL(context.Background(), r.Name); err == nil {
			urls = append(urls, url)
		}
	}
	return []publishDiagnostic{{
		severity: "warning",
		message:  fmt.Sprintf("远程 %q 不是 GitHub 仓库。技能必须托管在 GitHub 上才能被发现", strings.Join(urls, ", ")),
	}}
}

func renderDiagnosticsTTY(opts *PublishOptions, skillCount int, diagnostics []publishDiagnostic, errors, warnings, fixes int, owner, repo string) {
	cs := opts.IO.ColorScheme()

	// Separate info messages from errors/warnings for cleaner output
	var infos, issues []publishDiagnostic
	for _, d := range diagnostics {
		if d.severity == "info" {
			infos = append(infos, d)
		} else {
			issues = append(issues, d)
		}
	}

	if len(issues) == 0 && fixes == 0 {
		fmt.Fprintf(opts.IO.Out, "%s 已成功校验 %d 个技能\n", cs.SuccessIcon(), skillCount)
	} else {
		for _, d := range issues {
			var prefix string
			switch d.severity {
			case "error":
				prefix = cs.FailureIcon()
			case "warning":
				prefix = cs.WarningIcon()
			case "fixed":
				prefix = cs.SuccessIcon()
			default:
				prefix = cs.FailureIcon()
			}
			if d.skill != "" {
				fmt.Fprintf(opts.IO.Out, "%s %s: %s\n", prefix, cs.Bold(d.skill), d.message)
			} else {
				fmt.Fprintf(opts.IO.Out, "%s %s\n", prefix, d.message)
			}
		}

		fmt.Fprintln(opts.IO.Out)
		if fixes > 0 {
			fmt.Fprintf(opts.IO.Out, "已修复 %d 个问题\n", fixes)
		}
		if errors > 0 {
			fmt.Fprintf(opts.IO.Out, "%s，%s\n",
				cs.Red(fmt.Sprintf("%d 个错误", errors)),
				cs.Yellow(fmt.Sprintf("%d 个警告", warnings)))
		} else {
			fmt.Fprintf(opts.IO.Out, "%s\n", cs.Yellow(fmt.Sprintf("%d 个警告", warnings)))
		}
	}

	// Always show info messages
	for _, d := range infos {
		fmt.Fprintf(opts.IO.ErrOut, "\n%s\n", d.message)
	}

	if errors == 0 && !opts.Fix {
		if owner != "" && repo != "" {
			fmt.Fprintf(opts.IO.ErrOut, "\n%s 仓库: %s/%s\n", cs.Green("可以发布了！"), owner, repo)
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "\n%s 请确保仓库带有 \"agent-skills\" 主题。\n", cs.Green("可以发布了！"))
		}
	}
}

func renderDiagnosticsPlain(opts *PublishOptions, diagnostics []publishDiagnostic, errors, warnings int) {
	for _, d := range diagnostics {
		if d.severity == "info" {
			continue
		}
		fmt.Fprintf(opts.IO.Out, "%s\t%s\t%s\n", d.severity, d.skill, d.message)
	}
	if errors == 0 && warnings == 0 {
		fmt.Fprintf(opts.IO.Out, "ok\n")
	}
}

// findGitHubMetadataKeys 返回带 "github-" 前缀的元数据键。
func findGitHubMetadataKeys(meta map[string]any) []string {
	var keys []string
	for k := range meta {
		if strings.HasPrefix(k, "github-") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// stripGitHubMetadata 从 metadata 映射中移除 github-* 键并重新序列化。
func stripGitHubMetadata(content string) (string, error) {
	result, err := frontmatter.Parse(content)
	if err != nil {
		return "", err
	}

	meta, ok := result.RawYAML["metadata"].(map[string]any)
	if !ok {
		return content, nil
	}

	for k := range meta {
		if strings.HasPrefix(k, "github-") {
			delete(meta, k)
		}
	}

	if len(meta) == 0 {
		delete(result.RawYAML, "metadata")
	} else {
		result.RawYAML["metadata"] = meta
	}

	return frontmatter.Serialize(result.RawYAML, result.Body)
}
