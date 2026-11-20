package view

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/ruleset/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Browser    browser.Browser
	Prompter   prompter.Prompter

	ID              string
	WebMode         bool
	IncludeParents  bool
	InteractiveMode bool
	Organization    string
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "view [<ruleset-id>]",
		Short: "查看规则集信息",
		Long: heredoc.Docf(`
			查看有关 GitHub 规则集的信息。

			如果没有提供 ID，将使用交互式提示来选择要查看的规则集。

			使用 %[1]s--parents%[1]s 标志来控制是否应返回在更高级别配置且同样适用于所提供仓库或组织的规则集。
			默认值为 %[1]strue%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 从适用于当前仓库的所有规则集中交互式选择要查看的规则集
			$ gh ruleset view

			# 仅从当前仓库中配置的规则集中交互式选择要查看的规则集
			$ gh ruleset view --parents=false

			# 查看在当前仓库或其任何父仓库中配置的规则集
			$ gh ruleset view 43

			# 查看在不同仓库或其任何父仓库中配置的规则集
			$ gh ruleset view 23 --repo owner/repo

			# 查看组织级别的规则集
			$ gh ruleset view 23 --org my-org
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && opts.Organization != "" {
				return cmdutil.FlagErrorf("仅需指定 --repo 或 --org 其中之一")
			}

			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				// a string is actually needed later on, so verify that it's numeric
				// but use the string anyway
				_, err := strconv.Atoi(args[0])
				if err != nil {
					return cmdutil.FlagErrorf("规则集 ID 值无效: %v 不是整数", args[0])
				}
				opts.ID = args[0]
			} else if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("在非交互式运行时必须提供规则集 ID")
			} else {
				opts.InteractiveMode = true
			}

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在浏览器中打开规则集")
	cmd.Flags().StringVarP(&opts.Organization, "org", "o", "", "如果提供的 ID 是组织级规则，则显示组织名称")
	cmd.Flags().BoolVarP(&opts.IncludeParents, "parents", "p", true, "是否包含同样适用的更高层级配置的规则集")

	return cmd
}

func viewRun(opts *ViewOptions) error {
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
	cs := opts.IO.ColorScheme()

	if opts.InteractiveMode {
		var rsList *shared.RulesetList
		limit := 30
		if opts.Organization != "" {
			rsList, err = shared.ListOrgRulesets(httpClient, opts.Organization, limit, hostname, opts.IncludeParents)
		} else {
			rsList, err = shared.ListRepoRulesets(httpClient, repoI, limit, opts.IncludeParents)
		}

		if err != nil {
			return err
		}

		if rsList.TotalCount == 0 {
			return shared.NoRulesetsFoundError(opts.Organization, repoI, opts.IncludeParents)
		}

		rs, err := selectRulesetID(rsList, opts.Prompter, cs)
		if err != nil {
			return err
		}

		if rs != nil {
			opts.ID = strconv.FormatInt(rs.DatabaseId, 10)

			// can't get a ruleset lower in the chain than what was queried, so no need to handle repos here
			if rs.Source.TypeName == "Organization" {
				opts.Organization = rs.Source.Owner
			}
		}
	}

	var rs *shared.RulesetREST
	if opts.Organization != "" {
		rs, err = viewOrgRuleset(httpClient, opts.Organization, opts.ID, hostname)
	} else {
		rs, err = viewRepoRuleset(httpClient, repoI, opts.ID)
	}

	if err != nil {
		return err
	}

	w := opts.IO.Out

	if opts.WebMode {
		if rs != nil {
			if opts.IO.IsStdoutTTY() {
				fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s\n", text.DisplayURL(rs.Links.Html.Href))
			}

			return opts.Browser.Browse(rs.Links.Html.Href)
		} else {
			fmt.Fprintf(w, "未找到规则集\n")
		}
	}

	fmt.Fprintf(w, "\n%s\n", cs.Bold(rs.Name))
	fmt.Fprintf(w, "ID: %s\n", cs.Cyan(strconv.FormatInt(rs.Id, 10)))
	fmt.Fprintf(w, "源: %s (%s)\n", rs.Source, rs.SourceType)

	fmt.Fprint(w, "Enforcement: ")
	switch rs.Enforcement {
	case "disabled":
		fmt.Fprintf(w, "%s\n", cs.Red("禁用"))
	case "evaluate":
		fmt.Fprintf(w, "%s\n", cs.Yellow("评估模式(非强制)"))
	case "active":
		fmt.Fprintf(w, "%s\n", cs.Green("活跃"))
	default:
		fmt.Fprintf(w, "%s\n", rs.Enforcement)
	}

	if rs.CurrentUserCanBypass != "" {
		fmt.Fprintf(w, "您可以绕过: %s\n", strings.ReplaceAll(rs.CurrentUserCanBypass, "_", " "))
	}

	fmt.Fprintf(w, "\n%s\n", cs.Bold("绕过列表"))
	if len(rs.BypassActors) == 0 {
		fmt.Fprintf(w, "此规则集无法绕过\n")
	} else {
		sort.Slice(rs.BypassActors, func(i, j int) bool {
			return rs.BypassActors[i].ActorId < rs.BypassActors[j].ActorId
		})

		for _, t := range rs.BypassActors {
			fmt.Fprintf(w, "- %s (ID: %d)，模式: %s\n", t.ActorType, t.ActorId, t.BypassMode)
		}
	}

	fmt.Fprintf(w, "\n%s\n", cs.Bold("Conditions"))
	if len(rs.Conditions) == 0 {
		fmt.Fprintf(w, "未配置条件\n")
	} else {
		// sort keys for consistent responses
		keys := make([]string, 0, len(rs.Conditions))
		for key := range rs.Conditions {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, name := range keys {
			condition := rs.Conditions[name]
			fmt.Fprintf(w, "- %s: ", name)

			// sort these keys too for consistency
			subkeys := make([]string, 0, len(condition))
			for subkey := range condition {
				subkeys = append(subkeys, subkey)
			}
			sort.Strings(subkeys)

			for _, n := range subkeys {
				fmt.Fprintf(w, "[%s: %v] ", n, condition[n])
			}

			fmt.Fprint(w, "\n")
		}
	}

	fmt.Fprintf(w, "\n%s\n", cs.Bold("规则"))
	if len(rs.Rules) == 0 {
		fmt.Fprintf(w, "未配置规则集\n")
	} else {
		fmt.Fprint(w, shared.ParseRulesForDisplay(rs.Rules))
	}

	return nil
}

func selectRulesetID(rsList *shared.RulesetList, p prompter.Prompter, cs *iostreams.ColorScheme) (*shared.RulesetGraphQL, error) {
	rulesets := make([]string, len(rsList.Rulesets))
	for i, rs := range rsList.Rulesets {
		s := fmt.Sprintf(
			"%s: %s | %s | contains %s | configured in %s",
			cs.Cyan(strconv.FormatInt(rs.DatabaseId, 10)),
			rs.Name,
			strings.ToLower(rs.Enforcement),
			text.Pluralize(rs.Rules.TotalCount, "rule"),
			shared.RulesetSource(rs),
		)
		rulesets[i] = s
	}

	r, err := p.Select("您想查看哪个规则集？", rulesets[0], rulesets)
	if err != nil {
		return nil, err
	}

	return &rsList.Rulesets[r], nil
}
