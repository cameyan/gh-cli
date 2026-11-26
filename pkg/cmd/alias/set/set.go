package set

import (
	"fmt"
	"io"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/alias/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type SetOptions struct {
	Config func() (gh.Config, error)
	IO     *iostreams.IOStreams

	Name              string
	Expansion         string
	IsShell           bool
	OverwriteExisting bool

	validAliasName      func(string) bool
	validAliasExpansion func(string) bool
}

func NewCmdSet(f *cmdutil.Factory, runF func(*SetOptions) error) *cobra.Command {
	opts := &SetOptions{
		IO:     f.IOStreams,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "set <alias> <expansion>",
		Short: "为 gh 命令创建快捷方式",
		Long: heredoc.Docf(`
			定义一个单词，当调用时将扩展为完整的 gh 命令。

			扩展可以指定额外的参数和标志。如果扩展包含位置占位符，如 %[1]s$1%[1]s。则别名之后的额外参数
			将被适当地插入。否则，额外参数将被附加到扩展后的命令中。

			使用 %[1]s-%[1]s 作为扩展参数，可以从标准输入读取扩展字符串。这在定义扩展时有助于避免引号问题。

			如果扩展以 %[1]s!%[1]s 开头，或者如果指定了 %[1]s--shell%[1]s，那么该扩展就是一个 shell 表达式，
			当调用别名时，将通过 %[1]ssh%[1]s 解释器进行求值。这允许通过管道和重定向来链接多个命令。
		`, "`"),
		Example: heredoc.Doc(`
			# 注意: Windows上的命令提示符需要使用双引号来包裹参数
			$ gh alias set pv 'pr view'
			$ gh pv -w 123  #=> gh pr view -w 123

			$ gh alias set bugs 'issue list --label=bugs'
			$ gh bugs

			$ gh alias set homework 'issue list --assignee @me'
			$ gh homework

			$ gh alias set 'issue mine' 'issue list --mention @me'
			$ gh issue mine

			$ gh alias set epicsBy 'issue list --author="$1" --label="epic"'
			$ gh epicsBy vilmibm  #=> gh issue list --author="vilmibm" --label="epic"

			$ gh alias set --shell igrep 'gh issue list --label="$1" | grep "$2"'
			$ gh igrep epic foo  #=> gh issue list --label="epic" | grep "foo"
		`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Name = args[0]
			opts.Expansion = args[1]

			opts.validAliasName = shared.ValidAliasNameFunc(cmd)
			opts.validAliasExpansion = shared.ValidAliasExpansionFunc(cmd)

			if runF != nil {
				return runF(opts)
			}

			return setRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.IsShell, "shell", "s", false, "声明一个别名，以便通过 shell 解释器传递")
	cmd.Flags().BoolVar(&opts.OverwriteExisting, "clobber", false, "覆盖同名的现有别名")

	return cmd
}

func setRun(opts *SetOptions) error {
	cs := opts.IO.ColorScheme()
	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	aliasCfg := cfg.Aliases()

	expansion, err := getExpansion(opts)
	if err != nil {
		return fmt.Errorf("不理解扩张: %w", err)
	}

	if opts.IsShell && !strings.HasPrefix(expansion, "!") {
		expansion = "!" + expansion
	}

	isTerminal := opts.IO.IsStdoutTTY()
	if isTerminal {
		fmt.Fprintf(opts.IO.ErrOut, "- 为 %s 创建别名: %s\n", cs.Bold(opts.Name), cs.Bold(expansion))
	}

	var existingAlias bool
	if _, err := aliasCfg.Get(opts.Name); err == nil {
		existingAlias = true
	}

	if !opts.validAliasName(opts.Name) {
		if !existingAlias {
			return fmt.Errorf("%s 无法创建别名 %s: 已经是 gh 命令或扩展",
				cs.FailureIcon(),
				cs.Bold(opts.Name))
		}

		if existingAlias && !opts.OverwriteExisting {
			return fmt.Errorf("%s 无法创建别名 %s: 名称已被占用，请使用 --clobber 标志覆盖它",
				cs.FailureIcon(),
				cs.Bold(opts.Name),
			)
		}
	}

	if !opts.validAliasExpansion(expansion) {
		return fmt.Errorf("%s 无法创建别名 %s: 该扩展名不对应任何 gh 命令、扩展或别名",
			cs.FailureIcon(),
			cs.Bold(opts.Name))
	}

	aliasCfg.Add(opts.Name, expansion)

	err = cfg.Write()
	if err != nil {
		return err
	}

	successMsg := fmt.Sprintf("%s 添加别名 %s", cs.SuccessIcon(), cs.Bold(opts.Name))
	if existingAlias && opts.OverwriteExisting {
		successMsg = fmt.Sprintf("%s 修改了别名 %s",
			cs.WarningIcon(),
			cs.Bold(opts.Name))
	}

	if isTerminal {
		fmt.Fprintln(opts.IO.ErrOut, successMsg)
	}

	return nil
}

func getExpansion(opts *SetOptions) (string, error) {
	if opts.Expansion == "-" {
		stdin, err := io.ReadAll(opts.IO.In)
		if err != nil {
			return "", fmt.Errorf("无法从标准输入读取数据: %w", err)
		}

		return string(stdin), nil
	}

	return opts.Expansion, nil
}
