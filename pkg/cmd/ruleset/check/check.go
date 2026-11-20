package check

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/ruleset/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type CheckOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	Config     func() (gh.Config, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Git        *git.Client
	Browser    browser.Browser

	Branch  string
	Default bool
	WebMode bool
}

func NewCmdCheck(f *cmdutil.Factory, runF func(*CheckOptions) error) *cobra.Command {
	opts := &CheckOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Browser:    f.Browser,
		Git:        f.GitClient,
	}
	cmd := &cobra.Command{
		Use:   "check [<branch>]",
		Short: "查看适用于给定分支的规则",
		Long: heredoc.Docf(`
			查看适用于给定分支的 GitHub 规则信息。

			提供的分支名称无需实际存在；系统将显示适用于该名称分支的规则。无论规则配置在何处，所有规则都将被返回。

			如果未提供分支名称，则将使用当前分支。

			可以使用 %[1]s--default%[1]s 标志来查看适用于仓库默认分支的规则。
		`, "`"),
		Example: heredoc.Doc(`
			# 查看适用于当前分支的所有规则
			$ gh ruleset check

			# 查看适用于另一个仓库中名为 "my-branch" 的分支的所有规则
			$ gh ruleset check my-branch --repo owner/repo

			# 查看适用于另一个仓库中默认分支的所有规则
			$ gh ruleset check --default --repo owner/repo

			# 查看在另一个仓库或其任一父级中配置的规则集
			$ gh ruleset view 23 --repo owner/repo

			# 查看组织级别的规则集
			$ gh ruleset view 23 --org my-org
		`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if len(args) > 0 {
				opts.Branch = args[0]
			}

			if err := cmdutil.MutuallyExclusive(
				"仅需指定 `--default` 或一个分支名称其中之一",
				opts.Branch != "",
				opts.Default,
			); err != nil {
				return err
			}

			if runF != nil {
				return runF(opts)
			}

			return checkRun(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Default, "default", false, "检查默认分支上的规则")
	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在网页浏览器中打开分支规则页面")

	return cmd
}

func checkRun(opts *CheckOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	client := api.NewClientFromHTTP(httpClient)

	repoI, err := opts.BaseRepo()
	if err != nil {
		return fmt.Errorf("无法确定要使用的仓库: %w", err)
	}

	git := opts.Git

	if opts.Default {
		repo, err := api.GitHubRepo(client, repoI)
		if err != nil {
			return fmt.Errorf("无法获取仓库信息: %w", err)
		}
		opts.Branch = repo.DefaultBranchRef.Name
	}

	if opts.Branch == "" {
		opts.Branch, err = git.CurrentBranch(context.Background())
		if err != nil {
			return fmt.Errorf("无法确定当前分支: %w", err)
		}
	}

	if opts.WebMode {
		// the query string parameter may have % signs in it, so it must be carefully used with Printf functions
		queryString := fmt.Sprintf("?ref=%s", url.QueryEscape("refs/heads/"+opts.Branch))
		rawUrl := ghrepo.GenerateRepoURL(repoI, "rules")

		if opts.IO.IsStdoutTTY() {
			fmt.Fprintf(opts.IO.Out, "正在浏览器中打开 %s\n", text.DisplayURL(rawUrl))
		}

		return opts.Browser.Browse(rawUrl + queryString)
	}

	var rules []shared.RulesetRule

	endpoint, err := safeurl.JoinPath("repos", repoI.RepoOwner(), repoI.RepoName(), "rules", "branches", opts.Branch)
	if err != nil {
		return err
	}

	if err = client.REST(repoI.RepoHost(), "GET", endpoint.String(), nil, &rules); err != nil {
		return fmt.Errorf("获取 %s 失败: %w", endpoint.String(), err)
	}

	w := opts.IO.Out

	fmt.Fprintf(w, "%[1]d 条规则适用于仓库 %[3]s/%[4]s 中的分支 %[2]s\n", len(rules), opts.Branch, repoI.RepoOwner(), repoI.RepoName())

	if len(rules) > 0 {
		fmt.Fprint(w, "\n")
		fmt.Fprint(w, shared.ParseRulesForDisplay(rules))
	}

	return nil
}
