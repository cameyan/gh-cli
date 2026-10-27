package root

import (
	"fmt"
	"os"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	accessibilityCmd "github.com/cli/cli/v2/pkg/cmd/accessibility"
	actionsCmd "github.com/cli/cli/v2/pkg/cmd/actions"
	agentTaskCmd "github.com/cli/cli/v2/pkg/cmd/agent-task"
	aliasCmd "github.com/cli/cli/v2/pkg/cmd/alias"
	"github.com/cli/cli/v2/pkg/cmd/alias/shared"
	apiCmd "github.com/cli/cli/v2/pkg/cmd/api"
	attestationCmd "github.com/cli/cli/v2/pkg/cmd/attestation"
	authCmd "github.com/cli/cli/v2/pkg/cmd/auth"
	browseCmd "github.com/cli/cli/v2/pkg/cmd/browse"
	cacheCmd "github.com/cli/cli/v2/pkg/cmd/cache"
	codespaceCmd "github.com/cli/cli/v2/pkg/cmd/codespace"
	completionCmd "github.com/cli/cli/v2/pkg/cmd/completion"
	configCmd "github.com/cli/cli/v2/pkg/cmd/config"
	copilotCmd "github.com/cli/cli/v2/pkg/cmd/copilot"
	discussionCmd "github.com/cli/cli/v2/pkg/cmd/discussion"
	extensionCmd "github.com/cli/cli/v2/pkg/cmd/extension"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	gistCmd "github.com/cli/cli/v2/pkg/cmd/gist"
	gpgKeyCmd "github.com/cli/cli/v2/pkg/cmd/gpg-key"
	issueCmd "github.com/cli/cli/v2/pkg/cmd/issue"
	labelCmd "github.com/cli/cli/v2/pkg/cmd/label"
	licensesCmd "github.com/cli/cli/v2/pkg/cmd/licenses"
	orgCmd "github.com/cli/cli/v2/pkg/cmd/org"
	prCmd "github.com/cli/cli/v2/pkg/cmd/pr"
	previewCmd "github.com/cli/cli/v2/pkg/cmd/preview"
	projectCmd "github.com/cli/cli/v2/pkg/cmd/project"
	releaseCmd "github.com/cli/cli/v2/pkg/cmd/release"
	repoCmd "github.com/cli/cli/v2/pkg/cmd/repo"
	creditsCmd "github.com/cli/cli/v2/pkg/cmd/repo/credits"
	rulesetCmd "github.com/cli/cli/v2/pkg/cmd/ruleset"
	runCmd "github.com/cli/cli/v2/pkg/cmd/run"
	searchCmd "github.com/cli/cli/v2/pkg/cmd/search"
	secretCmd "github.com/cli/cli/v2/pkg/cmd/secret"
	sendTelemetryCmd "github.com/cli/cli/v2/pkg/cmd/send-telemetry"
	skillsCmd "github.com/cli/cli/v2/pkg/cmd/skills"
	sshKeyCmd "github.com/cli/cli/v2/pkg/cmd/ssh-key"
	statusCmd "github.com/cli/cli/v2/pkg/cmd/status"
	variableCmd "github.com/cli/cli/v2/pkg/cmd/variable"
	versionCmd "github.com/cli/cli/v2/pkg/cmd/version"
	workflowCmd "github.com/cli/cli/v2/pkg/cmd/workflow"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/extensions"
	"github.com/google/shlex"
	"github.com/spf13/cobra"
)

type AuthError struct {
	err error
}

func (ae *AuthError) Error() string {
	return ae.err.Error()
}

func NewCmdRoot(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, version, buildDate string) (*cobra.Command, error) {
	io := f.IOStreams
	cfg, err := f.Config()
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %s\n", err)
	}

	cmd := &cobra.Command{
		Use:   "gh <command> <subcommand> [flags]",
		Short: "GitHub CLI",
		Long:  `在命令行中与 GitHub 无缝协作`,
		Example: heredoc.Doc(`
			$ gh issue create
			$ gh repo clone cli/cli
			$ gh pr checkout 321
		`),
		Annotations: map[string]string{
			"versionInfo": versionCmd.Format(version, buildDate),
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// 要求用户在运行大多数命令之前已完成身份验证
			if cmdutil.IsAuthCheckEnabled(cmd) && !cmdutil.CheckAuth(cfg) {
				parent := cmd.Parent()
				if parent != nil && parent.Use == "codespace" {
					fmt.Fprintln(io.ErrOut, "要开始使用 GitHub CLI，请运行:  gh auth login -s codespace")
				} else {
					fmt.Fprint(io.ErrOut, authHelp())
				}
				return &AuthError{}
			}

			return nil
		},
	}

	// cmd.SetOut(f.IOStreams.Out)    // 由于 https://github.com/spf13/cobra/issues/1708 无法使用
	// cmd.SetErr(f.IOStreams.ErrOut) // 就让它默认使用 os.Stderr 吧

	cmd.PersistentFlags().Bool("help", false, "显示命令的帮助信息")

	// 除非设置了退出选项(opt-out)，否则覆盖 Cobra 的默认行为
	if os.Getenv("GH_COBRA") == "" {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true

		// 这个 --version flag 在 rootHelpFunc 中检查
		cmd.Flags().Bool("version", false, "显示 gh 版本")

		cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
			rootHelpFunc(f, c, args)
		})
		cmd.SetUsageFunc(func(c *cobra.Command) error {
			return rootUsageFunc(f.IOStreams.ErrOut, c)
		})
		cmd.SetFlagErrorFunc(rootFlagErrorFunc)
	}

	cmd.AddGroup(&cobra.Group{
		ID:    "core",
		Title: "核心命令",
	})
	cmd.AddGroup(&cobra.Group{
		ID:    "actions",
		Title: "GitHub Actions 命令",
	})
	cmd.AddGroup(&cobra.Group{
		ID:    "extension",
		Title: "扩展命令",
	})

	// Child commands
	cmd.AddCommand(versionCmd.NewCmdVersion(f, version, buildDate))
	cmd.AddCommand(accessibilityCmd.NewCmdAccessibility(f))
	cmd.AddCommand(actionsCmd.NewCmdActions(f))
	cmd.AddCommand(aliasCmd.NewCmdAlias(f))
	cmd.AddCommand(authCmd.NewCmdAuth(f, telemetry))
	cmd.AddCommand(attestationCmd.NewCmdAttestation(f))
	cmd.AddCommand(configCmd.NewCmdConfig(f))
	cmd.AddCommand(gistCmd.NewCmdGist(f))
	cmd.AddCommand(gpgKeyCmd.NewCmdGPGKey(f))
	cmd.AddCommand(completionCmd.NewCmdCompletion(f.IOStreams))
	cmd.AddCommand(extensionCmd.NewCmdExtension(f))
	cmd.AddCommand(searchCmd.NewCmdSearch(f))
	cmd.AddCommand(secretCmd.NewCmdSecret(f))
	cmd.AddCommand(variableCmd.NewCmdVariable(f))
	cmd.AddCommand(sshKeyCmd.NewCmdSSHKey(f))
	cmd.AddCommand(codespaceCmd.NewCmdCodespace(f))
	cmd.AddCommand(projectCmd.NewCmdProject(f))
	cmd.AddCommand(previewCmd.NewCmdPreview(f))
	cmd.AddCommand(skillsCmd.NewCmdSkills(f, telemetry))

	// 具有独立功能且没有子命令的根级命令
	cmd.AddCommand(copilotCmd.NewCmdCopilot(f, telemetry, nil))
	cmd.AddCommand(statusCmd.NewCmdStatus(f, nil))
	cmd.AddCommand(creditsCmd.NewCmdCredits(f, nil))
	cmd.AddCommand(licensesCmd.NewCmdLicenses(f))
	cmd.AddCommand(sendTelemetryCmd.NewCmdSendTelemetry(f))

	// 从这里往下是那些需要"智能" BaseRepo 解析器的命令
	repoResolvingCmdFactory := *f
	repoResolvingCmdFactory.BaseRepo = factory.SmartBaseRepoFunc(f)

	cmd.AddCommand(agentTaskCmd.NewCmdAgentTask(&repoResolvingCmdFactory))
	cmd.AddCommand(browseCmd.NewCmdBrowse(&repoResolvingCmdFactory, nil))
	cmd.AddCommand(discussionCmd.NewCmdDiscussion(&repoResolvingCmdFactory))
	cmd.AddCommand(prCmd.NewCmdPR(&repoResolvingCmdFactory, telemetry))
	cmd.AddCommand(orgCmd.NewCmdOrg(&repoResolvingCmdFactory))
	cmd.AddCommand(issueCmd.NewCmdIssue(&repoResolvingCmdFactory, telemetry))
	cmd.AddCommand(releaseCmd.NewCmdRelease(&repoResolvingCmdFactory))
	cmd.AddCommand(repoCmd.NewCmdRepo(&repoResolvingCmdFactory))
	cmd.AddCommand(rulesetCmd.NewCmdRuleset(&repoResolvingCmdFactory))
	cmd.AddCommand(runCmd.NewCmdRun(&repoResolvingCmdFactory))
	cmd.AddCommand(workflowCmd.NewCmdWorkflow(&repoResolvingCmdFactory))
	cmd.AddCommand(labelCmd.NewCmdLabel(&repoResolvingCmdFactory))
	cmd.AddCommand(cacheCmd.NewCmdCache(&repoResolvingCmdFactory))
	cmd.AddCommand(apiCmd.NewCmdApi(&repoResolvingCmdFactory, nil))

	// 帮助主题(help topic)
	var referenceCmd *cobra.Command
	for _, ht := range HelpTopics {
		helpTopicCmd := NewCmdHelpTopic(f.IOStreams, ht)
		cmd.AddCommand(helpTopicCmd)

		// 关于我们为何显式关注 reference 命令，请参见函数底部
		if ht.name == "reference" {
			referenceCmd = helpTopicCmd
		}
	}

	// 扩展(extension)
	em := f.ExtensionManager
	for _, e := range em.List() {
		extensionCmd := NewCmdExtension(io, em, e, nil)
		// 若会与核心命令冲突，则不注册该扩展命令。
		_, _, err := cmd.Find([]string{extensionCmd.Name()})
		if err == nil {
			continue
		}

		cmd.AddCommand(extensionCmd)
	}

	// 别名(alias)
	aliases := cfg.Aliases()
	validAliasName := shared.ValidAliasNameFunc(cmd)
	validAliasExpansion := shared.ValidAliasExpansionFunc(cmd)
	for k, v := range aliases.All() {
		aliasName := k
		aliasValue := v
		if validAliasName(aliasName) && validAliasExpansion(aliasValue) {
			split, _ := shlex.Split(aliasName)
			parentCmd, parentArgs, _ := cmd.Find(split)
			if !parentCmd.ContainsGroup("alias") {
				parentCmd.AddGroup(&cobra.Group{
					ID:    "alias",
					Title: "别名命令",
				})
			}
			if strings.HasPrefix(aliasValue, "!") {
				shellAliasCmd := NewCmdShellAlias(io, parentArgs[0], aliasValue)
				parentCmd.AddCommand(shellAliasCmd)
			} else {
				aliasCmd := NewCmdAlias(io, parentArgs[0], aliasValue)
				split, _ := shlex.Split(aliasValue)
				child, _, _ := cmd.Find(split)
				aliasCmd.SetUsageFunc(func(_ *cobra.Command) error {
					return rootUsageFunc(f.IOStreams.ErrOut, child)
				})
				aliasCmd.SetHelpFunc(func(_ *cobra.Command, args []string) {
					rootHelpFunc(f, child, args)
				})
				parentCmd.AddCommand(aliasCmd)
			}
		}
	}

	// 官方扩展(extension)的 stub: 在被调用时建议安装 GitHub 官方扩展的隐藏命令。
	// 在真实扩展与别名(alias)之后注册，以使二者优先于 stub。
	for i := range extensions.OfficialExtensions {
		ext := &extensions.OfficialExtensions[i]
		if _, _, err := cmd.Find([]string{ext.Name}); err == nil {
			continue
		}
		cmd.AddCommand(NewCmdOfficialExtensionStub(io, f.Prompter, em, ext))
	}

	cmdutil.DisableAuthCheck(cmd)
	cmdutil.RecordTelemetryForSubcommands(cmd, telemetry)

	// reference 命令产生分页的输出，显示所有其他命令的信息。
	// 因此，在注册完所有其他命令之后，我们在这里显式设置 Long 文本与 HelpFunc。
	// 我们曾试验过在调用 HelpFunc 时动态生成分页输出，但由于 doc 生成会使用 Long 文本，
	// 在这里显式声明该命令是特殊的会更简单。
	referenceCmd.Long = stringifyReference(cmd)
	referenceCmd.SetHelpFunc(longPager(f.IOStreams))
	return cmd, nil
}
