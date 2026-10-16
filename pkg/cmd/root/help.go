package root

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/google/shlex"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func rootUsageFunc(w io.Writer, command *cobra.Command) error {
	fmt.Fprintf(w, "用法(Usage):  %s", command.UseLine())

	var subcommands []*cobra.Command
	for _, c := range command.Commands() {
		if !c.IsAvailableCommand() {
			continue
		}
		subcommands = append(subcommands, c)
	}

	if len(subcommands) > 0 {
		fmt.Fprint(w, "\n\n可用命令:\n")
		for _, c := range subcommands {
			fmt.Fprintf(w, "  %s\n", c.Name())
		}
		return nil
	}

	flagUsages := command.LocalFlags().FlagUsages()
	if flagUsages != "" {
		fmt.Fprintln(w, "\n\nFlag:")
		fmt.Fprint(w, text.Indent(dedent(flagUsages), "  "))
	}
	return nil
}

func rootFlagErrorFunc(cmd *cobra.Command, err error) error {
	if err == pflag.ErrHelp {
		return err
	}
	return cmdutil.FlagErrorWrap(err)
}

var hasFailed bool

// HasFailed 向主进程发出应以非零状态退出的信号
func HasFailed() bool {
	return hasFailed
}

// 在子命令名被输错时显示有用的错误信息。
// 这与 Cobra 对根命令的行为一致，但 Cobra 令人困惑地
// 没有把这种行为应用到嵌套命令上。
func nestedSuggestFunc(w io.Writer, command *cobra.Command, arg string) {
	fmt.Fprintf(w, "%q 的未知命令 %q\n", command.CommandPath(), arg)

	var candidates []string
	if arg == "help" {
		candidates = []string{"--help"}
	} else {
		if command.SuggestionsMinimumDistance <= 0 {
			command.SuggestionsMinimumDistance = 2
		}
		candidates = command.SuggestionsFor(arg)
	}

	if len(candidates) > 0 {
		fmt.Fprint(w, "\n您是想输入这个吗?\n")
		for _, c := range candidates {
			fmt.Fprintf(w, "\t%s\n", c)
		}
	}

	fmt.Fprint(w, "\n")
	_ = rootUsageFunc(w, command)
}

func isRootCmd(command *cobra.Command) bool {
	return command != nil && !command.HasParent()
}

func rootHelpFunc(f *cmdutil.Factory, command *cobra.Command, _ []string) {
	flags := command.Flags()

	if isRootCmd(command) {
		if versionVal, err := flags.GetBool("version"); err == nil && versionVal {
			fmt.Fprint(f.IOStreams.Out, command.Annotations["versionInfo"])
			return
		} else if err != nil {
			fmt.Fprintln(f.IOStreams.ErrOut, err)
			hasFailed = true
			return
		}
	}

	cs := f.IOStreams.ColorScheme()

	if help, _ := flags.GetBool("help"); !help && !command.Runnable() && len(flags.Args()) > 0 {
		nestedSuggestFunc(f.IOStreams.ErrOut, command, flags.Args()[0])
		hasFailed = true
		return
	}

	WriteHelp(f.IOStreams.Out, cs, command)
}

// WriteHelp 将 command 的完整帮助文本渲染到 w。这与 `gh <command> --help`
// 产生的输出相同，被单独暴露出来，以便错误报告等调用方可以将帮助渲染到
// 自己选择的流，而不是始终写入 stdout。
//
// 用户定义的别名(alias)会被解析为其展开后的命令，使调用方看到目标的 flag
// 与示例，而不是别名 stub。这与 NewCmdRoot 中为别名配置的用法与帮助函数保持一致。
func WriteHelp(w io.Writer, cs *iostreams.ColorScheme, command *cobra.Command) {
	command = resolveAliasTarget(command)

	type helpEntry struct {
		Title string
		Body  string
	}

	longText := command.Long
	if longText == "" {
		longText = command.Short
	}
	if longText != "" && command.LocalFlags().Lookup("jq") != nil {
		longText = strings.TrimRight(longText, "\n") +
			"\n\n有关输出格式化 flag 的更多信息，请参见 `gh help formatting`。"
	}

	helpEntries := []helpEntry{}
	if longText != "" {
		helpEntries = append(helpEntries, helpEntry{"", longText})
	}
	helpEntries = append(helpEntries, helpEntry{"用法(USAGE)", command.UseLine()})

	if len(command.Aliases) > 0 {
		helpEntries = append(helpEntries, helpEntry{"别名(ALIASES)", strings.Join(BuildAliasList(command, command.Aliases), ", ") + "\n"})
	}

	// 为非扩展命令静态计算的填充宽度，
	// 最长的是 `gh accessibility`，为 13 个字符 + 1 个空格。
	//
	// 今后应考虑新颖的计算方式 [AF]
	namePadding := 14

	for _, g := range GroupedCommands(command) {
		var names []string
		for _, c := range g.Commands {
			names = append(names, rpad(c.Name()+":", namePadding)+c.Short)
		}
		helpEntries = append(helpEntries, helpEntry{
			Title: strings.ToUpper(g.Title),
			Body:  strings.Join(names, "\n"),
		})
	}

	if isRootCmd(command) {
		var helpTopics []string
		if c := findCommand(command, "accessibility"); c != nil {
			helpTopics = append(helpTopics, rpad(c.Name()+":", namePadding)+c.Short)
		}
		if c := findCommand(command, "actions"); c != nil {
			helpTopics = append(helpTopics, rpad(c.Name()+":", namePadding)+c.Short)
		}
		for _, helpTopic := range HelpTopics {
			helpTopics = append(helpTopics, rpad(helpTopic.name+":", namePadding)+helpTopic.short)
		}
		sort.Strings(helpTopics)
		helpEntries = append(helpEntries, helpEntry{"帮助主题(HELP TOPICS)", strings.Join(helpTopics, "\n")})
	}

	flagUsages := command.LocalFlags().FlagUsages()
	if flagUsages != "" {
		helpEntries = append(helpEntries, helpEntry{"Flag(FLAGS)", dedent(flagUsages)})
	}
	inheritedFlagUsages := command.InheritedFlags().FlagUsages()
	if inheritedFlagUsages != "" {
		helpEntries = append(helpEntries, helpEntry{"继承的 Flag(INHERITED FLAGS)", dedent(inheritedFlagUsages)})
	}
	if _, ok := command.Annotations["help:json-fields"]; ok {
		fields := strings.Split(command.Annotations["help:json-fields"], ",")
		helpEntries = append(helpEntries, helpEntry{"JSON 字段(JSON FIELDS)", text.FormatSlice(fields, 80, 0, "", "", true)})
	}
	if _, ok := command.Annotations["help:arguments"]; ok {
		helpEntries = append(helpEntries, helpEntry{"参数(ARGUMENTS)", command.Annotations["help:arguments"]})
	}
	if command.Example != "" {
		helpEntries = append(helpEntries, helpEntry{"示例(EXAMPLES)", command.Example})
	}
	if _, ok := command.Annotations["help:environment"]; ok {
		helpEntries = append(helpEntries, helpEntry{"环境变量(ENVIRONMENT VARIABLES)", command.Annotations["help:environment"]})
	}
	helpEntries = append(helpEntries, helpEntry{"了解更多(LEARN MORE)", heredoc.Docf(`
		使用 %[1]sgh <command> <subcommand> --help%[1]s 获取某个命令的更多信息。
		在 https://cli.github.com/manual 阅读手册
		使用 %[1]sgh help exit-codes%[1]s 了解退出码(exit code)
		使用 %[1]sgh help accessibility%[1]s 了解无障碍体验
	`, "`)"})

	for _, e := range helpEntries {
		if e.Title != "" {
			// 若有标题，则为正文中的每一行添加缩进
			fmt.Fprintln(w, cs.Bold(e.Title))
			fmt.Fprintln(w, text.Indent(strings.Trim(e.Body, "\r\n"), "  "))
		} else {
			// 若没有标题，则按原样打印正文
			fmt.Fprintln(w, e.Body)
		}
		fmt.Fprintln(w)
	}
}

func authHelp() string {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return heredoc.Doc(`
			gh: 要在 GitHub Actions 工作流中使用 GitHub CLI，请设置 GH_TOKEN 环境变量。示例:
			  env:
			    GH_TOKEN: ${{ github.token }}
		`)
	}

	if os.Getenv("CI") != "" {
		return heredoc.Doc(`
			gh: 要在自动化(automation)中使用 GitHub CLI，请设置 GH_TOKEN 环境变量。
		`)
	}

	return heredoc.Doc(`
		要开始使用 GitHub CLI，请运行:  gh auth login
		或者，将 GH_TOKEN 环境变量设置为 GitHub API 身份验证 token。
	`)
}

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

type CommandGroup struct {
	Title    string
	Commands []*cobra.Command
}

func GroupedCommands(cmd *cobra.Command) []CommandGroup {
	var res []CommandGroup

	for _, g := range cmd.Groups() {
		var cmds []*cobra.Command
		for _, c := range cmd.Commands() {
			if c.GroupID == g.ID && c.IsAvailableCommand() {
				cmds = append(cmds, c)
			}
		}
		if len(cmds) > 0 {
			res = append(res, CommandGroup{
				Title:    g.Title,
				Commands: cmds,
			})
		}
	}

	var cmds []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.GroupID == "" && c.IsAvailableCommand() {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) > 0 {
		defaultGroupTitle := "其他命令"
		if len(cmd.Groups()) == 0 {
			defaultGroupTitle = "可用命令"
		}
		res = append(res, CommandGroup{
			Title:    defaultGroupTitle,
			Commands: cmds,
		})
	}

	return res
}

// rpad 在字符串右侧添加填充。
func rpad(s string, padding int) string {
	template := fmt.Sprintf("%%-%ds ", padding)
	return fmt.Sprintf(template, s)
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1

	for _, l := range lines {
		if len(l) == 0 {
			continue
		}

		indent := len(l) - len(strings.TrimLeft(l, " "))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return s
	}

	var buf bytes.Buffer
	for _, l := range lines {
		fmt.Fprintln(&buf, strings.TrimPrefix(l, strings.Repeat(" ", minIndent)))
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func BuildAliasList(cmd *cobra.Command, aliases []string) []string {
	if !cmd.HasParent() {
		return aliases
	}

	parentAliases := append(cmd.Parent().Aliases, cmd.Parent().Name())
	sort.Strings(parentAliases)

	var aliasesWithParentAliases []string
	// 例如 aliases = [ls]
	for _, alias := range aliases {
		// 例如 parentAliases = [codespaces, cs]
		for _, parentAlias := range parentAliases {
			// 例如 aliasesWithParentAliases = [codespaces list, codespaces ls, cs list, cs ls]
			aliasesWithParentAliases = append(aliasesWithParentAliases, fmt.Sprintf("%s %s", parentAlias, alias))
		}
	}

	return BuildAliasList(cmd.Parent(), aliasesWithParentAliases)
}

// aliasExpansionAnnotation 记录用户定义的别名(alias)展开后的命令行，
// 以便帮助渲染能将该别名解析到其目标。
const aliasExpansionAnnotation = "gh:alias-expansion"

// resolveAliasTarget 返回用户定义的别名(alias)展开后的命令；
// 若它不是别名或无法解析目标，则原样返回 command。
// shell 别名没有目标命令，因此总是原样返回。
func resolveAliasTarget(command *cobra.Command) *cobra.Command {
	expansion, ok := command.Annotations[aliasExpansionAnnotation]
	if !ok {
		return command
	}
	args, err := shlex.Split(expansion)
	if err != nil || len(args) == 0 {
		return command
	}
	target, _, err := command.Root().Find(args)
	if err != nil || target == nil {
		return command
	}
	return target
}
