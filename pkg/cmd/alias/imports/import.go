package imports

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmd/alias/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type ImportOptions struct {
	Config func() (gh.Config, error)
	IO     *iostreams.IOStreams

	Filename          string
	OverwriteExisting bool

	validAliasName      func(string) bool
	validAliasExpansion func(string) bool
}

func NewCmdImport(f *cmdutil.Factory, runF func(*ImportOptions) error) *cobra.Command {
	opts := &ImportOptions{
		IO:     f.IOStreams,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "import [<filename> | -]",
		Short: "从 YAML 文件导入别名",
		Long: heredoc.Docf(`
			从 YAML 文件的内容中导入别名。

			别名应定义为 YAML 中的映射，其中键表示别名，值表示相应的扩展内容。示例文件应如下所示:

			    bugs: issue list --label=bug
			    igrep: '!gh issue list --label="$1" | grep "$2"'
			    features: |-
			        issue list
			        --label=enhancement

			使用 %[1]s-%[1]s 从标准输入读取别名(YAML 格式)。

			%[1]sgh alias list%[1]s 的输出可用于生成包含您别名的 YAML 文件，您可以使用该文件将别名从一台
			机器导入到另一台机器。运行 %[1]sgh help alias list%[1]s 以了解更多信息。
		`, "`"),
		Example: heredoc.Doc(`
			# 从文件中导入别名
			$ gh alias import aliases.yml

			# 从标准输入导入别名
			$ gh alias import -
		`),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return cmdutil.FlagErrorf("参数过多")
			}
			if len(args) == 0 && opts.IO.IsStdinTTY() {
				return cmdutil.FlagErrorf("未传递文件名，且标准输入 STDIN 中无内容")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Filename = "-"
			if len(args) > 0 {
				opts.Filename = args[0]
			}

			opts.validAliasName = shared.ValidAliasNameFunc(cmd)
			opts.validAliasExpansion = shared.ValidAliasExpansionFunc(cmd)

			if runF != nil {
				return runF(opts)
			}

			return importRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.OverwriteExisting, "clobber", false, "覆盖同名的现有别名")

	return cmd
}

func importRun(opts *ImportOptions) error {
	cs := opts.IO.ColorScheme()
	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	aliasCfg := cfg.Aliases()

	b, err := cmdutil.ReadFile(opts.Filename, opts.IO.In)
	if err != nil {
		return err
	}

	aliasMap := map[string]string{}
	if err = yaml.Unmarshal(b, &aliasMap); err != nil {
		return err
	}

	isTerminal := opts.IO.IsStdoutTTY()
	if isTerminal {
		if opts.Filename == "-" {
			fmt.Fprintf(opts.IO.ErrOut, "- 从标准输入导入别名\n")
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "- 从文件 %q 导入别名\n", opts.Filename)
		}
	}

	var msg strings.Builder

	for _, alias := range getSortedKeys(aliasMap) {
		var existingAlias bool
		if _, err := aliasCfg.Get(alias); err == nil {
			existingAlias = true
		}

		if !opts.validAliasName(alias) {
			if !existingAlias {
				fmt.Fprintf(&msg, "%s 无法导入别名 %s: 已经是 gh 命令或扩展\n",
					cs.FailureIcon(),
					cs.Bold(alias),
				)
				continue
			}

			if existingAlias && !opts.OverwriteExisting {
				fmt.Fprintf(&msg, "%s 无法导入别名 %s: 名称已被占用\n",
					cs.FailureIcon(),
					cs.Bold(alias),
				)
				continue
			}
		}

		expansion := aliasMap[alias]

		if !opts.validAliasExpansion(expansion) {
			fmt.Fprintf(&msg, "%s 无法导入别名 %s: 该扩展名不对应 gh 命令、扩展或别名\n",
				cs.FailureIcon(),
				cs.Bold(alias),
			)
			continue
		}

		aliasCfg.Add(alias, expansion)

		if existingAlias && opts.OverwriteExisting {
			fmt.Fprintf(&msg, "%s 修改了别名 %s\n",
				cs.WarningIcon(),
				cs.Bold(alias),
			)
		} else {
			fmt.Fprintf(&msg, "%s 添加别名 %s\n",
				cs.SuccessIcon(),
				cs.Bold(alias),
			)
		}
	}

	if err := cfg.Write(); err != nil {
		return err
	}

	if isTerminal {
		fmt.Fprintln(opts.IO.ErrOut, msg.String())
	}

	return nil
}

func getSortedKeys(m map[string]string) []string {
	keys := []string{}
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
