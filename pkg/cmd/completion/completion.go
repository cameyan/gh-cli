package completion

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

func NewCmdCompletion(io *iostreams.IOStreams) *cobra.Command {
	var shellType string

	cmd := &cobra.Command{
		Use:   "completion -s <shell>",
		Short: "生成 shell 补全脚本",
		Long: heredoc.Docf(`
			为 GitHub CLI 命令生成 shell 补全脚本。

			通过包管理器安装 GitHub CLI 时，可能无需额外的 shell 配置即可获得补全支持。
			对于 Homebrew，参见 <https://docs.brew.sh/Shell-Completion>

			如果需要手动设置补全，请按照以下说明操作。配置文件的确切位置可能因系统而异。
			在测试补全是否生效之前，请确保已重启 shell。

			### bash

			首先，确保使用包管理器安装 %[1]sbash-completion%[1]s。

			然后，将以下内容添加到你的 %[1]s~/.bash_profile%[1]s:

				eval "$(gh completion -s bash)"

			### zsh

			生成 %[1]s_gh%[1]s 补全脚本，并将其放到 %[1]s$fpath%[1]s 中的某个位置:

				gh completion -s zsh > /usr/local/share/zsh/site-functions/_gh

			确保 %[1]s~/.zshrc%[1]s 中包含以下内容:

				autoload -U compinit
				compinit -i

			建议使用 Zsh 5.7 或更高版本。

			### fish

			生成 %[1]sgh.fish%[1]s 补全脚本:

				gh completion -s fish > ~/.config/fish/completions/gh.fish

			### PowerShell

			使用以下命令打开你的 profile 脚本:

				mkdir -Path (Split-Path -Parent $profile) -ErrorAction SilentlyContinue
				notepad $profile

			添加以下内容并保存文件:

				Invoke-Expression -Command $(gh completion -s powershell | Out-String)
		`, "`"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if shellType == "" {
				if io.IsStdoutTTY() {
					return cmdutil.FlagErrorf("错误: `--shell` 的值是必需的")
				}
				shellType = "bash"
			}

			w := io.Out
			rootCmd := cmd.Parent()

			switch shellType {
			case "bash":
				return rootCmd.GenBashCompletionV2(w, true)
			case "zsh":
				return rootCmd.GenZshCompletion(w)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(w)
			case "fish":
				return rootCmd.GenFishCompletion(w, true)
			default:
				return fmt.Errorf("不支持的 shell 类型 %q", shellType)
			}
		},
		DisableFlagsInUseLine: true,
	}

	cmdutil.DisableAuthCheck(cmd)
	cmdutil.StringEnumFlag(cmd, &shellType, "shell", "s", "", []string{"bash", "zsh", "fish", "powershell"}, "Shell 类型")
	cmdutil.DisableTelemetry(cmd)

	return cmd
}
