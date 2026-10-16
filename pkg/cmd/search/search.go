package search

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"

	searchCodeCmd "github.com/cli/cli/v2/pkg/cmd/search/code"
	searchCommitsCmd "github.com/cli/cli/v2/pkg/cmd/search/commits"
	searchIssuesCmd "github.com/cli/cli/v2/pkg/cmd/search/issues"
	searchPrsCmd "github.com/cli/cli/v2/pkg/cmd/search/prs"
	searchReposCmd "github.com/cli/cli/v2/pkg/cmd/search/repos"
)

func NewCmdSearch(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <command>",
		Short: "搜索仓库、issue 和 PR",
		Long: heredoc.Docf(`
			在整个 GitHub 范围内搜索。

			排除匹配某个限定符的搜索结果

			在浏览器中，GitHub 搜索语法支持通过在限定符前加连字符来排除匹配该限定符的结果。
			例如，要搜索没有 "bug" 标签的 issue，可以使用 %[1]s-label:bug%[1]s 作为搜索限定符。

			%[1]sgh%[1]s 在 %[1]sgh search%[1]s 中同样支持该语法，但它需要额外的命令行参数，
			以避免以连字符开头的查询被解释为命令行选项。

			在类 Unix 系统上，可以使用 %[1]s--%[1]s 参数来表明其后的参数不是选项，而是查询字符串。例如:

			$ gh search issues -- "my-search-query -label:bug"

			在 PowerShell 上，必须同时使用 %[1]s--%[2]s%[1]s 参数和 %[1]s--%[1]s 参数才能达到同样效果。例如:

			$ gh --%[2]s search issues -- "my search query -label:bug"

			更多信息请参见:
			- GitHub 搜索语法:
			  <https://docs.github.com/en/search-github/getting-started-with-searching-on-github/understanding-the-search-syntax#exclude-results-that-match-a-qualifier>
			- PowerShell 的停止解析 flag %[1]s--%[2]s%[1]s:
			  <https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5#the-stop-parsing-token>
			- 类 Unix 系统的 %[1]s--%[1]s 参数:
			  <https://www.gnu.org/software/bash/manual/bash.html#Shell-Builtin-Commands-1>
		`, "`", "%"),
	}

	cmd.AddCommand(searchCodeCmd.NewCmdCode(f, nil))
	cmd.AddCommand(searchCommitsCmd.NewCmdCommits(f, nil))
	cmd.AddCommand(searchIssuesCmd.NewCmdIssues(f, nil))
	cmd.AddCommand(searchPrsCmd.NewCmdPrs(f, nil))
	cmd.AddCommand(searchReposCmd.NewCmdRepos(f, nil))

	return cmd
}
