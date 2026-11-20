package list

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/ruleset/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser

	Limit          int
	IncludeParents bool
	WebMode        bool
	Organization   string
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
	}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出仓库或组织的规则集",
		Long: heredoc.Docf(`
			列出仓库或组织的 GitHub 规则集。

			如果未提供任何选项，则会列出当前仓库的规则集。您可以通过使用 %[1]s--repo%[1]s 标志来查询其他仓库的规则集。
			您也可以使用 %[1]s--org%[1]s 标志来列出为指定组织配置的规则集。

			使用 %[1]s--parents%[1]s 标志来控制是否应返回在更高级别配置且同样适用于所提供仓库或组织的规则集。
			默认值为 %[1]strue%[1]s。

			您的访问令牌必须具有 %[1]sadmin:org%[1]s 权限范围才能使用 %[1]s--org%[1]s 标志，该权限可以通过运行
			%[1]sgh auth refresh -s admin:org%[1]s 来授予。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出当前仓库中的规则集
			$ gh ruleset list

			# 列出不同仓库中的规则集，包括在更高层级配置的规则集
			$ gh ruleset list --repo owner/repo --parents

			# 列出组织中的规则集
			$ gh ruleset list --org org-name
		`),
		Aliases: []string{"ls"},
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && opts.Organization != "" {
				return cmdutil.FlagErrorf("仅需指定 --repo 或 --org 其中之一")
			}

			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效限制值: %v", opts.Limit)
			}

			if runF != nil {
				return runF(opts)
			}

			return listRun(opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 30, "列出的最大规则集数量上限")
	cmd.Flags().StringVarP(&opts.Organization, "org", "o", "", "为指定组织列出全组织范围的规则集")
	cmd.Flags().BoolVarP(&opts.IncludeParents, "parents", "p", true, "是否包含同样适用的更高层级配置的规则集")
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在浏览器中打开规则集列表")

	return cmd
}

func listRun(opts *ListOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	var repoI ghrepo.Interface

	// only one of the repo or org context is necessary
	if opts.Organization == "" {
		var repoErr error
		repoI, repoErr = opts.BaseRepo()
		if repoErr != nil {
			return repoErr
		}
	}

	hostname, _ := ghauth.DefaultHost()

	if opts.WebMode {
		var rulesetURL string
		if opts.Organization != "" {
			rulesetURL = fmt.Sprintf("%sorganizations/%s/settings/rules", ghinstance.HostPrefix(hostname), opts.Organization)
		} else {
			rulesetURL = ghrepo.GenerateRepoURL(repoI, "rules")
		}

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s\n", text.DisplayURL(rulesetURL))
		}

		return opts.Browser.Browse(rulesetURL)
	}

	var result *shared.RulesetList

	if opts.Organization != "" {
		result, err = shared.ListOrgRulesets(httpClient, opts.Organization, opts.Limit, hostname, opts.IncludeParents)
	} else {
		result, err = shared.ListRepoRulesets(httpClient, repoI, opts.Limit, opts.IncludeParents)
	}

	if err != nil {
		return err
	}

	if result.TotalCount == 0 {
		return shared.NoRulesetsFoundError(opts.Organization, repoI, opts.IncludeParents)
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}

	cs := opts.IO.ColorScheme()

	if opts.IO.IsStdoutTTY() {
		parentsMsg := ""
		if opts.IncludeParents {
			parentsMsg = " and its parents"
		}

		inMsg := fmt.Sprintf("%s%s", shared.EntityName(opts.Organization, repoI), parentsMsg)
		fmt.Fprintf(opts.IO.Out, "\n正在显示 %[3]s 的 %[1]d/%[2]d 个规则集\n\n", len(result.Rulesets), result.TotalCount, inMsg)
	}

	tp := tableprinter.New(opts.IO, tableprinter.WithHeader("ID", "名称", "源", "状态", "规则"))

	for _, rs := range result.Rulesets {
		tp.AddField(strconv.FormatInt(rs.DatabaseId, 10), tableprinter.WithColor(cs.Cyan))
		tp.AddField(rs.Name, tableprinter.WithColor(cs.Bold))
		tp.AddField(shared.RulesetSource(rs))
		tp.AddField(strings.ToLower(rs.Enforcement))
		tp.AddField(strconv.Itoa(rs.Rules.TotalCount))
		tp.EndRow()
	}

	return tp.Render()
}
