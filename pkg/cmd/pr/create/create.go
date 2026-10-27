package create

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/attachments"
	"github.com/cli/cli/v2/internal/browser"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/spf13/cobra"
)

type CreateOptions struct {
	// 该结构体存储用户输入与工厂函数
	Detector         fd.Detector
	HttpClient       func() (*http.Client, error)
	GitClient        *git.Client
	Config           func() (gh.Config, error)
	IO               *iostreams.IOStreams
	Remotes          func() (ghContext.Remotes, error)
	Branch           func() (string, error)
	Browser          browser.Browser
	Prompter         shared.Prompt
	Finder           shared.PRFinder
	TitledEditSurvey func(string, string) (string, string, error)

	TitleProvided bool
	BodyProvided  bool

	RootDirOverride string
	RepoOverride    string

	Autofill    bool
	FillVerbose bool
	FillFirst   bool
	EditorMode  bool
	WebMode     bool
	RecoverFile string

	IsDraft    bool
	Title      string
	Body       string
	BaseBranch string
	HeadBranch string

	Reviewers []string
	Assignees []string
	Labels    []string
	Projects  []string
	Milestone string

	MaintainerCanModify bool
	Template            string

	DryRun bool

	BodyDir     string
	AttachFlag  *attachments.Flag
	AttachEvent *attachments.TelemetryEvent
	Assets      []attachments.UserAsset
}

// creationRefs 是一个接口，提供通过 API 创建拉取请求(PR)所需的信息。
// 向上转型(upcasting)为具体实现可提供其他操作(fork 与 push)的更多上下文。
type creationRefs interface {
	// QualifiedHeadRef 返回源分支(head ref)的字符串形式，取决于源 ref 与基础 ref
	// 是否位于同一仓库。若为同一仓库，仅返回分支名。若为不同仓库，
	// 则以 <owner>:<branch> 的形式返回所有者与分支名。
	QualifiedHeadRef() string
	// UnqualifiedHeadRef 返回仅含分支名的源分支(head ref)。
	UnqualifiedHeadRef() string
	//BaseRef 返回基础分支(base branch)名。
	BaseRef() string

	// 虽然从 api.Repository 真正需要的只有仓库 ID，但改动这一点
	// 需要修改 API 函数签名，而引入此 refs 类型的重构已经足够大了。
	BaseRepo() *api.Repository
}

type baseRefs struct {
	baseRepo       *api.Repository
	baseBranchName string
}

func (r baseRefs) BaseRef() string {
	return r.baseBranchName
}

func (r baseRefs) BaseRepo() *api.Repository {
	return r.baseRepo
}

// skipPushRefs 向 handlePush 表明无需 push。
type skipPushRefs struct {
	baseRefs

	qualifiedHeadRef shared.QualifiedHeadRef
}

func (r skipPushRefs) QualifiedHeadRef() string {
	return r.qualifiedHeadRef.String()
}

func (r skipPushRefs) UnqualifiedHeadRef() string {
	return r.qualifiedHeadRef.BranchName()
}

// pushableRefs 向 handlePush 表明需要 push，
// 并提供该 push 应去向何处(HeadRepo)的更多信息。
type pushableRefs struct {
	baseRefs

	headRepo       ghrepo.Interface
	headBranchName string
}

func (r pushableRefs) QualifiedHeadRef() string {
	if ghrepo.IsSame(r.headRepo, r.baseRepo) {
		return r.headBranchName
	}
	return fmt.Sprintf("%s:%s", r.headRepo.RepoOwner(), r.headBranchName)
}

func (r pushableRefs) UnqualifiedHeadRef() string {
	return r.headBranchName
}

func (r pushableRefs) HeadRepo() ghrepo.Interface {
	return r.headRepo
}

// forkableRefs 向 handlePush 表明在 push 之前需要先 fork。
// 预期是在 fork 之后将其转换为 pushableRefs。我们本可以走非常面向对象(OOP)的路线，
// 在该结构体上提供一个返回 pushableRefs 的 Fork 方法，但那样就需要嵌入一个 API client，
// 而让它保持为一个简单的数据结构看起来更好。
type forkableRefs struct {
	baseRefs

	qualifiedHeadRef shared.QualifiedHeadRef
}

func (r forkableRefs) QualifiedHeadRef() string {
	return r.qualifiedHeadRef.String()
}

func (r forkableRefs) UnqualifiedHeadRef() string {
	return r.qualifiedHeadRef.BranchName()
}

// isSameRef 检查源分支(head)与基础分支(base)的 ref 是否指向同一仓库中的同一 ref。
// 对于跨仓库的 PR(例如来自 fork)，限定形式的源 ref 会包含所有者前缀(owner:branch)，
// 因此即使分支名相同，它们指向的也是不同的仓库。
func isSameRef(refs creationRefs) bool {
	if strings.Contains(refs.QualifiedHeadRef(), ":") {
		return false
	}
	return refs.UnqualifiedHeadRef() == refs.BaseRef()
}

// CreateContext 存储创建过程的上下文数据，用于积累创建拉取请求(PR)所需的足够数据。
type CreateContext struct {
	ResolvedRemotes *ghContext.ResolvedRemotes
	PRRefs          creationRefs
	// 在 PRRefs 存在的情况下，BaseTrackingBranch 或许是个稍微有些泄漏的抽象，
	// 但为引入该结构体已做了大量重构，为不必做更多设计而付出的这点代价是值得的。
	BaseTrackingBranch string
	Client             *api.Client
	GitClient          *git.Client
}

func NewCmdCreate(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:               f.IOStreams,
		HttpClient:       f.HttpClient,
		GitClient:        f.GitClient,
		Config:           f.Config,
		Remotes:          f.Remotes,
		Branch:           f.Branch,
		Browser:          f.Browser,
		Prompter:         f.Prompter,
		TitledEditSurvey: shared.TitledEditSurvey(&shared.UserEditor{Config: f.Config, IO: f.IOStreams}),
	}

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建拉取请求(PR)",
		Long: heredoc.Docf(`
			在 GitHub 上创建拉取请求(PR)。

			成功后，将打印所创建拉取请求(PR)的 URL。

			当当前分支尚未完整推送到 git 远端时，会提示询问将分支推送到何处，
			并提供 fork 基础仓库的选项。以此方式创建的任何 fork 只会包含上游仓库的默认分支。
			使用 %[1]s--head%[1]s 可显式跳过任何 fork 或 push 行为。

			%[1]s--head%[1]s 支持 %[1]s<user>:<branch>%[1]s 语法，用于选择 %[1]s<user>%[1]s 拥有的源仓库(head repo)。
			目前不支持将组织作为 %[1]s<user>%[1]s 使用。
			更多信息请参见 <https://github.com/cli/cli/issues/10093>

			还会提示输入拉取请求(PR)的标题和正文。使用 %[1]s--title%[1]s 和
			%[1]s--body%[1]s 可跳过此步骤，或使用 %[1]s--fill%[1]s 从 git 提交中自动填充这些值。
			需要注意，如果 %[1]s--title%[1]s 和/或 %[1]s--body%[1]s 与 %[1]s--fill%[1]s 同时提供，
			%[1]s--title%[1]s 和/或 %[1]s--body%[1]s 指定的值将优先生效，并覆盖任何自动填充的内容。

			可使用 %[1]s--base%[1]s flag 指定所创建 PR 的基础分支(base branch)。若未提供，
			将使用 %[1]sgh-merge-base%[1]s git 分支(branch)配置的值。若未配置，则使用仓库的默认分支。
			运行 %[1]sgit config branch.{current}.gh-merge-base {base}%[1]s 可将当前分支配置为使用指定的合并基点(merge base)。

			在拉取请求(PR)的正文中引用某个 issue，即可将该 issue 关联到该 PR。
			如果正文提及 %[1]sFixes #123%[1]s 或 %[1]sCloses #123%[1]s，当该拉取请求(PR)被合并时，
			被引用的 issue 将自动关闭。

			Use %[1]s--attach%[1]s to upload an image or video. The attachment is appended to the
			body. If the body references an attached file, such as %[1]s![alt](./login.png)%[1]s, that
			reference is rewritten to point at the uploaded asset instead. Paths in a
			%[1]s--body-file%[1]s are relative to its directory, falling back to the working directory.
			You can attach up to 50 files per command.

			图片的替代文本(alt text)为 %[1]s#%[1]s 之后的路径内容，如
			%[1]s--attach './login.png#The login error state'%[1]s。未提供时使用文件名。
			正文中已有的引用会保留其中书写的替代文本。视频以播放器形式呈现且没有替代文本，
			因此无法为其指定。

			如果部分附件上传成功而其他失败，拉取请求(PR)仍会携带成功上传的那些创建。
			随后该命令以非零状态退出，但新拉取请求(PR)的 URL 仍会打印到标准输出。

			默认情况下，对基础仓库具有写权限的用户可以向拉取请求(PR)的源分支(head branch)推送新提交。
			使用 %[1]s--no-maintainer-edit%[1]s 可禁用此能力。

			将拉取请求(PR)添加到项目(project)需要以 %[1]sproject%[1]s 权限范围(scope)授权。
			如需授权，请运行 %[1]sgh auth refresh -s project%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			$ gh pr create --title "The bug is fixed" --body "Everything works again"
			$ gh pr create --reviewer monalisa,hubot  --reviewer myorg/team-name
			$ gh pr create --project "Roadmap"
			$ gh pr create --base develop --head monalisa:feature
			$ gh pr create --template "pull_request_template.md"
			$ gh pr create --attach './login.png#The login error state'
			$ gh pr create --attach ./before.png --attach ./after.png
		`),
		Args:    cmdutil.NoArgsQuoteReminder,
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			opts.TitleProvided = cmd.Flags().Changed("title")
			opts.RepoOverride, _ = cmd.Flags().GetString("repo")
			// 变通方法: 由于该命令的实现方式，我们需要手动检查 GH_REPO。
			// 命令应使用标准的 BaseRepoOverride 功能来处理此行为。
			if opts.RepoOverride == "" {
				opts.RepoOverride = os.Getenv("GH_REPO")
			}

			noMaintainerEdit, _ := cmd.Flags().GetBool("no-maintainer-edit")
			opts.MaintainerCanModify = !noMaintainerEdit

			if !opts.IO.CanPrompt() && opts.RecoverFile != "" {
				return cmdutil.FlagErrorf("`--recover` 仅在交互式运行时支持")
			}

			if opts.IsDraft && opts.WebMode {
				return cmdutil.FlagErrorf("`--draft` flag 不支持与 `--web` 同时使用")
			}

			if len(opts.Reviewers) > 0 && opts.WebMode {
				return cmdutil.FlagErrorf("`--reviewer` flag 不支持与 `--web` 同时使用")
			}

			if cmd.Flags().Changed("no-maintainer-edit") && opts.WebMode {
				return cmdutil.FlagErrorf("`--no-maintainer-edit` flag 不支持与 `--web` 同时使用")
			}

			if opts.Autofill && opts.FillFirst {
				return cmdutil.FlagErrorf("`--fill` 不支持与 `--fill-first` 同时使用")
			}

			if opts.FillVerbose && opts.FillFirst {
				return cmdutil.FlagErrorf("`--fill-verbose` 不支持与 `--fill-first` 同时使用")
			}

			if opts.FillVerbose && opts.Autofill {
				return cmdutil.FlagErrorf("`--fill-verbose` 不支持与 `--fill` 同时使用")
			}

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--editor` 或 `--web` 之一",
				opts.EditorMode,
				opts.WebMode,
			); err != nil {
				return err
			}

			var err error
			opts.EditorMode, err = shared.InitEditorMode(f, opts.EditorMode, opts.WebMode, opts.IO.CanPrompt())
			if err != nil {
				return err
			}

			opts.BodyProvided = cmd.Flags().Changed("body")
			if bodyFile != "" {
				opts.Body, opts.BodyDir, err = cmdutil.ReadMarkdownBodyFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.BodyProvided = true
			}

			if opts.Template != "" && opts.BodyProvided {
				return cmdutil.FlagErrorf("`--template` 不支持与 `--body` 或 `--body-file` 同时使用")
			}

			if !opts.IO.CanPrompt() && !opts.WebMode && !(opts.FillVerbose || opts.Autofill || opts.FillFirst) && (!opts.TitleProvided || !opts.BodyProvided) {
				return cmdutil.FlagErrorf("非交互式运行时必须提供 `--title` 和 `--body` (或 `--fill` 或 `fill-first` 或 `--fillverbose`)")
			}

			if opts.DryRun && opts.WebMode {
				return cmdutil.FlagErrorf("`--dry-run` 不支持与 `--web` 同时使用")
			}

			if err := cmdutil.MutuallyExclusive(
				"`--attach` 不支持与 `--web` 同时使用",
				opts.AttachFlag.Changed(),
				opts.WebMode,
			); err != nil {
				return err
			}

			if err := cmdutil.MutuallyExclusive(
				"`--attach` 不支持与 `--dry-run` 同时使用",
				opts.AttachFlag.Changed(),
				opts.DryRun,
			); err != nil {
				return err
			}

			opts.AttachEvent = attachments.BeginTelemetry(telemetry, cmd.CommandPath(), opts.AttachFlag.Count())
			opts.Assets, err = opts.AttachFlag.UserAssets()
			if err != nil {
				return err
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
	}

	fl := cmd.Flags()
	fl.BoolVarP(&opts.IsDraft, "draft", "d", false, "将拉取请求(PR)标记为草稿(draft)")
	fl.StringVarP(&opts.Title, "title", "t", "", "拉取请求(PR)的标题")
	fl.StringVarP(&opts.Body, "body", "b", "", "拉取请求(PR)的正文")
	fl.StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")
	fl.StringVarP(&opts.BaseBranch, "base", "B", "", "希望将代码合并入的 `branch`")
	fl.StringVarP(&opts.HeadBranch, "head", "H", "", "包含拉取请求(PR)提交的 `branch` (默认 [当前分支])")
	fl.BoolVarP(&opts.EditorMode, "editor", "e", false, "跳过提示，打开文本编辑器撰写标题与正文。第一行为标题，其余文本为正文。")
	fl.BoolVarP(&opts.WebMode, "web", "w", false, "打开 web 浏览器创建拉取请求(PR)")
	fl.BoolVarP(&opts.FillVerbose, "fill-verbose", "", false, "使用提交的提交信息+正文(msg+body)作为描述")
	fl.BoolVarP(&opts.Autofill, "fill", "f", false, "使用提交信息作为标题和正文")
	fl.BoolVar(&opts.FillFirst, "fill-first", false, "使用首个提交的信息作为标题和正文")
	fl.StringSliceVarP(&opts.Reviewers, "reviewer", "r", nil, "按 `handle` 请求个人或团队进行审核(review)")
	fl.StringSliceVarP(&opts.Assignees, "assignee", "a", nil, "按 `login` 指派人员。使用 \"@me\" 指派给自己。")
	fl.StringSliceVarP(&opts.Labels, "label", "l", nil, "按 `name` 添加标签(label)")
	fl.StringSliceVarP(&opts.Projects, "project", "p", nil, "按 `title` 将拉取请求(PR)添加到项目(project)")
	fl.StringVarP(&opts.Milestone, "milestone", "m", "", "按 `name` 将拉取请求(PR)添加到里程碑(milestone)")
	fl.Bool("no-maintainer-edit", false, "禁用维护者(maintainer)修改拉取请求(PR)的能力")
	fl.StringVar(&opts.RecoverFile, "recover", "", "从一次失败的 create 运行中恢复输入")
	fl.StringVarP(&opts.Template, "template", "T", "", "用作起始正文的模板 `file`")
	fl.BoolVar(&opts.DryRun, "dry-run", false, "打印详细信息而不创建 PR。仍可能推送 git 变更。")
	opts.AttachFlag = attachments.AddFlag(cmd)

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "base", "head")

	_ = cmd.RegisterFlagCompletionFunc("reviewer", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		results, err := requestableReviewersForCompletion(opts)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return results, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

func createRun(opts *CreateOptions) error {
	ctx, err := NewCreateContext(opts)
	if err != nil {
		return err
	}

	if isSameRef(ctx.PRRefs) {
		return fmt.Errorf("源分支(head branch) %q 与基础分支(base branch) %q 相同，无法创建拉取请求(PR)", ctx.PRRefs.UnqualifiedHeadRef(), ctx.PRRefs.BaseRef())
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	// TODO projectsV1Deprecation
	// 移除此部分，因为我们应当不再需要检测
	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, ctx.PRRefs.BaseRepo().RepoHost())
	}

	projectsV1Support := opts.Detector.ProjectsV1()

	client := ctx.Client

	// 检测 ApiActorsSupported 特性，以判断能否使用基于搜索的审核者(reviewer)选择
	// (github.com)，还是需要使用基于遗留 ID 的选择(GHES)
	issueFeatures, err := opts.Detector.IssueFeatures()
	if err != nil {
		return err
	}
	var reviewerSearchFunc func(string) prompter.MultiSelectSearchResult
	var assigneeSearchFunc func(string) prompter.MultiSelectSearchResult
	if issueFeatures.ApiActorsSupported {
		reviewerSearchFunc = func(query string) prompter.MultiSelectSearchResult {
			candidates, moreResults, err := api.SuggestedReviewerActorsForRepo(client, ctx.PRRefs.BaseRepo(), query)
			if err != nil {
				return prompter.MultiSelectSearchResult{Err: err}
			}
			keys := make([]string, len(candidates))
			labels := make([]string, len(candidates))
			for i, c := range candidates {
				keys[i] = c.Login()
				labels[i] = c.DisplayName()
			}
			return prompter.MultiSelectSearchResult{Keys: keys, Labels: labels, MoreResults: moreResults}
		}
		assigneeSearchFunc = shared.RepoAssigneeSearchFunc(client, ctx.PRRefs.BaseRepo())
	}

	state, err := NewIssueState(*ctx, *opts, issueFeatures.ApiActorsSupported)
	if err != nil {
		return err
	}

	// TODO ApiActorsSupported
	if issueFeatures.ApiActorsSupported {
		state.ApiActorsSupported = true
	}

	var openURL string

	if opts.WebMode {
		if !(opts.Autofill || opts.FillFirst) {
			state.Title = opts.Title
			state.Body = opts.Body
		}
		if opts.Template != "" {
			state.Template = opts.Template
		}
		err = handlePush(*opts, *ctx)
		if err != nil {
			return err
		}
		openURL, err = generateCompareURL(*ctx, *state, projectsV1Support)
		if err != nil {
			return err
		}
		if !shared.ValidURL(openURL) {
			err = fmt.Errorf("无法在浏览器中打开: 超出了最大 URL 长度")
			return err
		}
		return previewPR(*opts, openURL)
	}

	if opts.TitleProvided {
		state.Title = opts.Title
	}

	if opts.BodyProvided {
		state.Body = opts.Body
	}

	existingPR, _, err := opts.Finder.Find(shared.FindOptions{
		Selector:   ctx.PRRefs.QualifiedHeadRef(),
		BaseBranch: ctx.PRRefs.BaseRef(),
		States:     []string{"OPEN"},
		Fields:     []string{"url"},
	})
	var notFound *shared.NotFoundError
	if err != nil && !errors.As(err, &notFound) {
		return fmt.Errorf("检查已有拉取请求(PR)出错: %w", err)
	}
	if err == nil {
		return fmt.Errorf("从分支(branch) %q 到分支 %q 的拉取请求(PR)已存在:\n%s",
			ctx.PRRefs.QualifiedHeadRef(), ctx.PRRefs.BaseRef(), existingPR.URL)
	}

	// 在提示之前构建，使无法上传的运行可以尽早停止。
	var uploader *attachments.Uploader
	if len(opts.Assets) > 0 {
		baseRepo := ctx.PRRefs.BaseRepo()
		uploader, err = attachments.NewUploader(httpClient, baseRepo.RepoHost(), baseRepo.DatabaseID, baseRepo.ViewerPermission)
		if err != nil {
			return err
		}
	}

	message := "\n正在创建从 %s 到 %s (位于 %s)的拉取请求(PR)\n\n"
	if state.Draft {
		message = "\n正在创建从 %s 到 %s (位于 %s)的草稿(draft)拉取请求(PR)\n\n"
	}
	if opts.DryRun {
		message = "\n正在试运行(dry run)从 %s 到 %s (位于 %s)的拉取请求(PR)\n\n"
	}

	cs := opts.IO.ColorScheme()

	if opts.IO.CanPrompt() {
		fmt.Fprintf(opts.IO.ErrOut, message,
			cs.Cyan(ctx.PRRefs.QualifiedHeadRef()),
			cs.Cyan(ctx.PRRefs.BaseRef()),
			ghrepo.FullName(ctx.PRRefs.BaseRepo()))
	}

	if !opts.EditorMode && (opts.FillVerbose || opts.Autofill || opts.FillFirst || (opts.TitleProvided && opts.BodyProvided)) {
		err = handlePush(*opts, *ctx)
		if err != nil {
			return err
		}
		return submitPR(*opts, *ctx, *state, projectsV1Support, uploader)
	}

	if opts.RecoverFile != "" {
		err = shared.FillFromJSON(opts.IO, opts.RecoverFile, state)
		if err != nil {
			return fmt.Errorf("恢复输入失败: %w", err)
		}
	}

	action := shared.SubmitAction
	if opts.IsDraft {
		action = shared.SubmitDraftAction
	}

	tpl := shared.NewTemplateManager(client.HTTP(), ctx.PRRefs.BaseRepo(), opts.Prompter, opts.RootDirOverride, opts.RepoOverride == "", true)

	if opts.EditorMode {
		if opts.Template != "" {
			var template shared.Template
			template, err = tpl.Select(opts.Template)
			if err != nil {
				return err
			}
			if state.Title == "" {
				state.Title = template.Title()
			}
			state.Body = string(template.Body())
		}

		state.Title, state.Body, err = opts.TitledEditSurvey(state.Title, state.Body)
		if err != nil {
			return err
		}
		if state.Title == "" {
			err = fmt.Errorf("标题不能为空")
			return err
		}
	} else {

		if !opts.TitleProvided {
			err = shared.TitleSurvey(opts.Prompter, opts.IO, state)
			if err != nil {
				return err
			}
		}

		defer shared.PreserveInput(opts.IO, state, &err)()

		if !opts.BodyProvided {
			templateContent := ""
			if opts.RecoverFile == "" {
				var template shared.Template

				if opts.Template != "" {
					template, err = tpl.Select(opts.Template)
					if err != nil {
						return err
					}
				} else {
					template, err = tpl.Choose()
					if err != nil {
						return err
					}
				}

				if template != nil {
					templateContent = string(template.Body())
				}
			}

			err = shared.BodySurvey(opts.Prompter, state, templateContent)
			if err != nil {
				return err
			}
		}

		openURL, err = generateCompareURL(*ctx, *state, projectsV1Support)
		if err != nil {
			return err
		}

		// 预览会打开浏览器，无法携带上传。
		allowPreview := !state.HasMetadata() && shared.ValidURL(openURL) && !opts.DryRun && len(opts.Assets) == 0
		allowMetadata := ctx.PRRefs.BaseRepo().ViewerCanTriage()
		action, err = shared.ConfirmPRSubmission(opts.Prompter, allowPreview, allowMetadata, state.Draft)
		if err != nil {
			return fmt.Errorf("无法确认: %w", err)
		}

		if action == shared.MetadataAction {
			fetcher := &shared.MetadataFetcher{
				IO:        opts.IO,
				APIClient: client,
				Repo:      ctx.PRRefs.BaseRepo(),
				State:     state,
			}
			err = shared.MetadataSurvey(opts.Prompter, opts.IO, ctx.PRRefs.BaseRepo(), fetcher, state, projectsV1Support, reviewerSearchFunc, assigneeSearchFunc)
			if err != nil {
				return err
			}

			action, err = shared.ConfirmPRSubmission(opts.Prompter, !state.HasMetadata() && !opts.DryRun && len(opts.Assets) == 0, false, state.Draft)
			if err != nil {
				return err
			}
		}
	}

	if action == shared.CancelAction {
		fmt.Fprintln(opts.IO.ErrOut, "正在丢弃。")
		err = cmdutil.CancelError
		return err
	}

	err = handlePush(*opts, *ctx)
	if err != nil {
		return err
	}

	if action == shared.PreviewAction {
		return previewPR(*opts, openURL)
	}

	if action == shared.SubmitDraftAction {
		state.Draft = true
		return submitPR(*opts, *ctx, *state, projectsV1Support, uploader)
	}

	if action == shared.SubmitAction {
		return submitPR(*opts, *ctx, *state, projectsV1Support, uploader)
	}

	err = errors.New("预期应取消、预览或提交")
	return err
}

var regexPattern = regexp.MustCompile(`(?m)^`)

func initDefaultTitleBody(ctx CreateContext, state *shared.IssueMetadataState, useFirstCommit bool, addBody bool) error {
	commits, err := ctx.GitClient.Commits(context.Background(), ctx.BaseTrackingBranch, ctx.PRRefs.UnqualifiedHeadRef())
	if err != nil {
		return err
	}

	if len(commits) == 1 || useFirstCommit {
		state.Title = commits[len(commits)-1].Title
		state.Body = commits[len(commits)-1].Body
	} else {
		state.Title = humanize(ctx.PRRefs.UnqualifiedHeadRef())
		var body strings.Builder
		for i, commit := range slices.Backward(commits) {
			fmt.Fprintf(&body, "- **%s**\n", commit.Title)
			if addBody {
				x := regexPattern.ReplaceAllString(commit.Body, "  ")
				fmt.Fprintf(&body, "%s", x)

				if i > 0 {
					fmt.Fprintln(&body)
					fmt.Fprintln(&body)
				}
			}
		}
		state.Body = body.String()
	}

	return nil
}

func NewIssueState(ctx CreateContext, opts CreateOptions, apiActorsSupported bool) (*shared.IssueMetadataState, error) {
	var milestoneTitles []string
	if opts.Milestone != "" {
		milestoneTitles = []string{opts.Milestone}
	}

	assigneeReplacer := shared.NewSpecialAssigneeReplacer(ctx.Client, ctx.PRRefs.BaseRepo().RepoHost(), apiActorsSupported, !opts.WebMode)
	assignees, err := assigneeReplacer.ReplaceSlice(opts.Assignees)
	if err != nil {
		return nil, err
	}

	copilotReplacer := shared.NewCopilotReviewerReplacer()
	reviewers := copilotReplacer.ReplaceSlice(opts.Reviewers)

	state := &shared.IssueMetadataState{
		Type:          shared.PRMetadata,
		Reviewers:     reviewers,
		Assignees:     assignees,
		Labels:        opts.Labels,
		ProjectTitles: opts.Projects,
		Milestones:    milestoneTitles,
		Draft:         opts.IsDraft,
	}

	if opts.FillVerbose || opts.Autofill || opts.FillFirst || !opts.TitleProvided || !opts.BodyProvided {
		err := initDefaultTitleBody(ctx, state, opts.FillFirst, opts.FillVerbose)
		if err != nil && (opts.FillVerbose || opts.Autofill || opts.FillFirst) {
			return nil, fmt.Errorf("could not compute title or body defaults: %w", err)
		}
	}

	return state, nil
}

func NewCreateContext(opts *CreateOptions) (*CreateContext, error) {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return nil, err
	}
	client := api.NewClientFromHTTP(httpClient)

	remotes, err := getRemotes(opts)
	if err != nil {
		return nil, err
	}

	resolvedRemotes, err := ghContext.ResolveRemotesToRepos(remotes, client, opts.RepoOverride)
	if err != nil {
		return nil, err
	}

	var baseRepo *api.Repository
	if br, err := resolvedRemotes.BaseRepo(opts.IO); err == nil {
		if r, ok := br.(*api.Repository); ok {
			baseRepo = r
		} else {
			// TODO: 如果 `repoContext.HeadRepos()` 反正都会请求 RepoNetwork，
			// 可以考虑搭那趟便车，而不是执行单独的查询
			baseRepo, err = api.GitHubRepo(client, br)
			if err != nil {
				return nil, err
			}
		}
	} else {
		return nil, err
	}

	// 该闭包提供了一种便捷方式，用于实例化一个除 refs 之外一切齐备的 CreateContext。
	// 这可能表明 CreateContext 有必要做些重构，但为引入 PRRefs 所做的重构已经足够大了。
	var newCreateContext = func(refs creationRefs) *CreateContext {
		baseTrackingBranch := refs.BaseRef()

		// baseTrackingBranch 稍后用于类似 `git commit upstream/main feature` 的命令，
		// 以便生成显示这两个 ref 之间提交的 PR 信息。我不太确定如果没有远端会发生什么，
		// 而这在使用 `gh pr create --repo owner/repo-that-is-not-a-remote` 这样的命令时似乎是有可能的。
		// 那种情况下，结果可能是一团糟？无论如何，旧代码就是这么做的，所以我不想在
		// 本已很大的重构中改变它。
		baseRemote, _ := resolvedRemotes.RemoteForRepo(baseRepo)
		if baseRemote != nil {
			baseTrackingBranch = fmt.Sprintf("%s/%s", baseRemote.Name, baseTrackingBranch)
		}

		return &CreateContext{
			ResolvedRemotes:    resolvedRemotes,
			Client:             client,
			GitClient:          opts.GitClient,
			PRRefs:             refs,
			BaseTrackingBranch: baseTrackingBranch,
		}
	}

	// 如果用户提供了源分支(head branch)，我们将直接使用它，而不对 git 做任何探究。
	// 该值可以是 <branch> 或 <user>:<branch> 形式。前者表示 PR 的基础仓库和源仓库相同。
	// 后者我们不知道源仓库(head repo)是什么(虽然可以通过 API 查询)，但幸运的是我们无需这样做，
	// 因为在创建拉取请求(PR)时 API 会替我们解析。这是可行的，因为用户在其命名空间下
	// 只能有一个 fork，而且组织根本无法使用这种 ref 格式。
	//
	// 注意: 以这种方式提供源分支意味着我们不应推送该分支，
	// 我们也通过返回的类型来表明这一点。
	if opts.HeadBranch != "" {
		qualifiedHeadRef, err := shared.ParseQualifiedHeadRef(opts.HeadBranch)
		if err != nil {
			return nil, err
		}

		branchConfig, err := opts.GitClient.ReadBranchConfig(context.Background(), qualifiedHeadRef.BranchName())
		if err != nil {
			return nil, err
		}

		baseBranch := opts.BaseBranch
		if baseBranch == "" {
			baseBranch = branchConfig.MergeBase
		}
		if baseBranch == "" {
			baseBranch = baseRepo.DefaultBranchRef.Name
		}

		return newCreateContext(skipPushRefs{
			qualifiedHeadRef: qualifiedHeadRef,
			baseRepo:         baseRepo,
			baseBranchName:   baseBranch,
		}), nil
	}

	if ucc, err := opts.GitClient.UncommittedChangeCount(context.Background()); err == nil && ucc > 0 {
		fmt.Fprintf(opts.IO.ErrOut, "Warning: %s\n", text.Pluralize(ucc, "uncommitted change"))
	}

	// 如果用户未提供源分支(head branch)，那就要来真的了。我们将探究 git，
	// 并尝试创建可推送的 refs。
	currentBranch, err := opts.Branch()
	if err != nil {
		return nil, fmt.Errorf("无法确定当前分支(branch): %w", err)
	}

	branchConfig, err := opts.GitClient.ReadBranchConfig(context.Background(), currentBranch)
	if err != nil {
		return nil, err
	}

	baseBranch := opts.BaseBranch
	if baseBranch == "" {
		baseBranch = branchConfig.MergeBase
	}
	if baseBranch == "" {
		baseBranch = baseRepo.DefaultBranchRef.Name
	}

	// 首先根据手头的 git 信息，尝试确定默认的源仓库(head repo)与远端分支(branch)名。
	defaultPRHead, err := shared.TryDetermineDefaultPRHead(
		// 已经请求过分支(branch)配置，因此缓存它
		shared.CachedBranchConfigGitConfigClient{
			CachedBranchConfig: branchConfig,
			GitConfigClient:    opts.GitClient,
		},
		shared.NewRemoteToRepoResolver(opts.Remotes),
		currentBranch,
	)
	if err != nil {
		return nil, err
	}

	// 从现在起 baseRefs 将始终相同。如果能让它不可变就好了!
	baseRefs := baseRefs{
		baseRepo:       baseRepo,
		baseBranchName: baseBranch,
	}

	// 如果我们能够确定源仓库(head repo)，那么检查远端跟踪(remote tracking) ref 是否与 HEAD 的
	// SHA 匹配。若匹配则无需 push，否则需要询问用户要推送到哪里。
	if headRepo, present := defaultPRHead.Repo.Value(); present {
		// 我们可能找不到远端，因为 git 分支(branch)配置中可能是 URL 而非远端名称。
		// 理想情况下，RemoteForRepo 应返回一个可供比较的哨兵(sentinel)错误，
		// 但引入这段代码的重构已经足够大了。
		headRemote, _ := resolvedRemotes.RemoteForRepo(headRepo)
		if headRemote != nil {
			resolvedRefs, _ := opts.GitClient.ShowRefs(
				context.Background(),
				[]string{
					"HEAD",
					fmt.Sprintf("refs/remotes/%s/%s", headRemote.Name, defaultPRHead.BranchName),
				},
			)

			// 返回两个 ref 意味着可以将 HEAD 与远端跟踪(remote tracking)分支(branch)比较。
			// 若有一个匹配的 ref，则可以跳过推送(push)。
			refsMatch := len(resolvedRefs) == 2 && resolvedRefs[0].Hash == resolvedRefs[1].Hash
			if refsMatch {
				qualifiedHeadRef := shared.NewQualifiedHeadRefWithoutOwner(defaultPRHead.BranchName)
				if headRepo.RepoOwner() != baseRepo.RepoOwner() {
					qualifiedHeadRef = shared.NewQualifiedHeadRef(headRepo.RepoOwner(), defaultPRHead.BranchName)
				}

				return newCreateContext(skipPushRefs{
					qualifiedHeadRef: qualifiedHeadRef,
					baseRefs:         baseRefs,
				}), nil
			}
		}
	}

	// 如果我们尚未确定 git 指示的仓库拥有正确的 ref，那么查看其他远端，
	// 看它们中是否有与 HEAD 相同 SHA 的。此时你可能会问:
	// "为什么不在上面用一个 ShowRefs 命令收集所有 SHA，供两种情况共用?"
	// ...
	// 这是因为下面的代码有一个我从旧代码移植过来的缺陷，以便保留既有行为，
	// 并限制本已很大的重构的范围。原代码的意图是遍历所有返回的 ref。
	// 然而，事实证明我们的 ShowRefs 实现并不能正确做到这一点。
	// 由于它提供了 --verify flag，git 会返回各 ref 的 SHA，直到遇到一个不存在的 ref，
	// 然后中止。
	//
	// 设想你有 "upstream" 和 "origin" 两个远端，并且已将分支(branch) "feature" 推送到 "origin"。
	// 由于远端顺序总是固定为 "upstream"、"github"、"origin"，之后其余的以不稳定方式排序，
	// 我们永远拿不到 origin 的 SHA，因为 refs/remotes/upstream/feature 不存在。
	//
	// 此外，仔细想想，这段代码有点急于求成。如果 "origin" 和 "colleague" 远端上有相同的 SHA，
	// 总是会给出 origin。如果是 "colleague-a" 和 "colleague-b"，由于 "origin" 之后的远端顺序
	// 是不稳定排序，不同调用之间无法保证顺序。
	//
	// 尽管如此，这一行为已经存在很久很久了，我不想在这次以重构为主的改动中引入其他行为变更。
	refsToLookup := []string{"HEAD"}
	for _, remote := range remotes {
		refsToLookup = append(refsToLookup, fmt.Sprintf("refs/remotes/%s/%s", remote.Name, currentBranch))
	}

	// 在此情况下忽略该错误是允许的，因为我们可能同时得到 refs 和一个错误(参见上文的 --verify flag)。
	// 理想情况下应有一个类型化的错误，让我们能够区分执行错误与某些 ref 不存在。
	// 不过，这对于本已很大的重构来说有些力不从心。
	refs, _ := opts.GitClient.ShowRefs(context.Background(), refsToLookup)
	if len(refs) > 1 {
		headRef := refs[0]
		var firstMatchingRef o.Option[git.RemoteTrackingRef]
		// 遍历所有 ref，尝试找到与 HEAD 的 SHA 匹配的那个。
		for _, r := range refs[1:] {
			if r.Hash == headRef.Hash {
				remoteTrackingRef, err := git.ParseRemoteTrackingRef(r.Name)
				if err != nil {
					return nil, err
				}

				firstMatchingRef = o.Some(remoteTrackingRef)
				break
			}
		}

		// 如果找到了匹配的 ref，那么无需推送(push)。
		if ref, present := firstMatchingRef.Value(); present {
			remote, err := remotes.FindByName(ref.Remote)
			if err != nil {
				return nil, err
			}

			qualifiedHeadRef := shared.NewQualifiedHeadRefWithoutOwner(ref.Branch)
			if baseRepo.RepoOwner() != remote.RepoOwner() {
				qualifiedHeadRef = shared.NewQualifiedHeadRef(remote.RepoOwner(), ref.Branch)
			}

			return newCreateContext(skipPushRefs{
				qualifiedHeadRef: qualifiedHeadRef,
				baseRefs:         baseRefs,
			}), nil
		}
	}

	// 如果到现在还没有确定仓库，且无法交互提示，那就到此为止了。
	if !opts.IO.CanPrompt() {
		fmt.Fprintln(opts.IO.ErrOut, "已中止: 必须先将当前分支(branch)推送到远端，或使用 --head flag")
		return nil, cmdutil.SilentError
	}

	// 否则，好耶，可以交互提示了!

	// 首先，我们查看自己的远端，判断是否有可以推送(push)的仓库。
	pushableRepos, err := resolvedRemotes.HeadRepos()
	if err != nil {
		return nil, err
	}

	// 如果找不到任何可推送的仓库，则查找基础仓库的 fork。
	if len(pushableRepos) == 0 {
		pushableRepos, err = api.RepoFindForks(client, baseRepo, 3)
		if err != nil {
			return nil, err
		}
	}

	currentLogin, err := api.CurrentLoginName(client, baseRepo.RepoHost())
	if err != nil {
		return nil, err
	}

	hasOwnFork := false
	var pushOptions []string
	for _, r := range pushableRepos {
		pushOptions = append(pushOptions, ghrepo.FullName(r))
		if r.RepoOwner() == currentLogin {
			hasOwnFork = true
		}
	}

	if !hasOwnFork {
		pushOptions = append(pushOptions, fmt.Sprintf("创建 %s 的 fork", ghrepo.FullName(baseRepo)))
	}
	pushOptions = append(pushOptions, "跳过推送该分支(branch)")
	pushOptions = append(pushOptions, "取消")

	selectedOption, err := opts.Prompter.Select(fmt.Sprintf("应将 '%s' 分支推送到哪里?", currentBranch), "", pushOptions)
	if err != nil {
		return nil, err
	}

	if selectedOption < len(pushableRepos) {
		// 已选择要推送(push)到的仓库。
		return newCreateContext(pushableRefs{
			headRepo:       pushableRepos[selectedOption],
			headBranchName: currentBranch,
			baseRefs:       baseRefs,
		}), nil
	} else if pushOptions[selectedOption] == "跳过推送该分支(branch)" {
		// 我们将完全跳过推送(push)，也就是说，使用已经推送的任何 SHA。
		// 目前尚不完全清楚用户期望此处 HEAD 使用哪个仓库，或许应通过某种 UX 方式加以明确，
		// 但据我所知，在旧实现中这始终意味着"使用基础仓库(base repo)"。
		return newCreateContext(skipPushRefs{
			qualifiedHeadRef: shared.NewQualifiedHeadRefWithoutOwner(currentBranch),
			baseRefs:         baseRefs,
		}), nil
	} else if pushOptions[selectedOption] == "取消" {
		return nil, cmdutil.CancelError
	} else {
		// 应创建一个 fork。
		return newCreateContext(forkableRefs{
			qualifiedHeadRef: shared.NewQualifiedHeadRef(currentLogin, currentBranch),
			baseRefs:         baseRefs,
		}), nil
	}
}

func getRemotes(opts *CreateOptions) (ghContext.Remotes, error) {
	// TODO: 考虑改为从 GitClient 获取远端(remotes)
	remotes, err := opts.Remotes()
	if err != nil {
		// 给定了仓库覆盖(repo override)值时，获取 git 远端(remotes)出错可忽略，
		// 以支持在 git 仓库之外使用该命令。
		if opts.RepoOverride == "" {
			return nil, err
		}
	}
	return remotes, nil
}

func submitPR(opts CreateOptions, ctx CreateContext, state shared.IssueMetadataState, projectV1Support gh.ProjectsV1Support, uploader *attachments.Uploader) error {
	client := ctx.Client

	params := map[string]any{
		"title":               state.Title,
		"body":                state.Body,
		"draft":               state.Draft,
		"baseRefName":         ctx.PRRefs.BaseRef(),
		"headRefName":         ctx.PRRefs.QualifiedHeadRef(),
		"maintainerCanModify": opts.MaintainerCanModify,
	}

	if params["title"] == "" {
		return errors.New("拉取请求(PR)标题不能为空")
	}

	err := shared.AddMetadataToIssueParams(client, ctx.PRRefs.BaseRepo(), params, &state, projectV1Support)
	if err != nil {
		return err
	}

	if opts.DryRun {
		if opts.IO.IsStdoutTTY() {
			return renderPullRequestTTY(opts.IO, params, &state)
		} else {
			return renderPullRequestPlain(opts.IO.Out, params, &state)
		}
	}

	var uploadErr error
	if uploader != nil {
		body, uploadResult, err := uploader.UploadAndAttach(context.Background(), state.Body, opts.BodyDir, opts.Assets)
		opts.AttachEvent.RecordOperations(uploadResult)
		// 如果没有上传任何文件，而正文失去了围绕它编写的文件引用，
		// 那就不是调用方要求创建的内容。此时分支已经推送，因此消息说明
		// 未创建什么，而不是声称本次运行没有任何效果。
		if err != nil && uploadResult.Uploaded == 0 {
			return fmt.Errorf("%w\n未创建任何拉取请求(PR)", err)
		}
		uploadErr = err
		params["body"] = body
	}

	opts.IO.StartProgressIndicator()
	pr, err := api.CreatePullRequest(client, ctx.PRRefs.BaseRepo(), params)
	opts.IO.StopProgressIndicator()
	if pr != nil {
		fmt.Fprintln(opts.IO.Out, pr.URL)
	}
	if err != nil {
		if pr != nil {
			err = fmt.Errorf("拉取请求(PR)更新失败: %w", err)
		} else {
			err = fmt.Errorf("拉取请求(PR)创建失败: %w", err)
		}
		// 上传失败与创建失败的补救措施不同，且创建失败会让已上传的资产(asset)
		// 变成无引用的孤儿，因此该错误放在前面读。
		return errors.Join(uploadErr, err)
	}
	return uploadErr
}

func renderPullRequestPlain(w io.Writer, params map[string]any, state *shared.IssueMetadataState) error {
	fmt.Fprint(w, "本将创建包含以下内容的拉取请求(PR):\n")
	fmt.Fprintf(w, "title:\t%s\n", params["title"])
	fmt.Fprintf(w, "draft:\t%t\n", params["draft"])
	fmt.Fprintf(w, "base:\t%s\n", params["baseRefName"])
	fmt.Fprintf(w, "head:\t%s\n", params["headRefName"])
	if len(state.Labels) != 0 {
		fmt.Fprintf(w, "labels:\t%v\n", strings.Join(state.Labels, ", "))
	}
	if len(state.Reviewers) != 0 {
		fmt.Fprintf(w, "reviewers:\t%v\n", strings.Join(state.Reviewers, ", "))
	}
	if len(state.Assignees) != 0 {
		fmt.Fprintf(w, "assignees:\t%v\n", strings.Join(state.Assignees, ", "))
	}
	if len(state.Milestones) != 0 {
		fmt.Fprintf(w, "milestones:\t%v\n", strings.Join(state.Milestones, ", "))
	}
	if len(state.ProjectTitles) != 0 {
		fmt.Fprintf(w, "projects:\t%v\n", strings.Join(state.ProjectTitles, ", "))
	}
	fmt.Fprintf(w, "maintainerCanModify:\t%t\n", params["maintainerCanModify"])
	fmt.Fprint(w, "body:\n")
	if len(params["body"].(string)) != 0 {
		fmt.Fprintln(w, params["body"])
	}
	return nil
}

func renderPullRequestTTY(io *iostreams.IOStreams, params map[string]any, state *shared.IssueMetadataState) error {
	cs := io.ColorScheme()
	out := io.Out

	fmt.Fprint(out, "本将创建包含以下内容的拉取请求(PR):\n")
	fmt.Fprintf(out, "%s: %s\n", cs.Bold("标题"), params["title"].(string))
	fmt.Fprintf(out, "%s: %t\n", cs.Bold("草稿(draft)"), params["draft"])
	fmt.Fprintf(out, "%s: %s\n", cs.Bold("基础分支(base)"), params["baseRefName"])
	fmt.Fprintf(out, "%s: %s\n", cs.Bold("源分支(head)"), params["headRefName"])
	if len(state.Labels) != 0 {
		fmt.Fprintf(out, "%s: %s\n", cs.Bold("标签(labels)"), strings.Join(state.Labels, ", "))
	}
	if len(state.Reviewers) != 0 {
		fmt.Fprintf(out, "%s: %s\n", cs.Bold("审核者(reviewers)"), strings.Join(state.Reviewers, ", "))
	}
	if len(state.Assignees) != 0 {
		fmt.Fprintf(out, "%s: %s\n", cs.Bold("被指派人(assignees)"), strings.Join(state.Assignees, ", "))
	}
	if len(state.Milestones) != 0 {
		fmt.Fprintf(out, "%s: %s\n", cs.Bold("里程碑(milestones)"), strings.Join(state.Milestones, ", "))
	}
	if len(state.ProjectTitles) != 0 {
		fmt.Fprintf(out, "%s: %s\n", cs.Bold("项目(projects)"), strings.Join(state.ProjectTitles, ", "))
	}
	fmt.Fprintf(out, "%s: %t\n", cs.Bold("维护者可修改(MaintainerCanModify)"), params["maintainerCanModify"])

	fmt.Fprintf(out, "%s\n", cs.Bold("正文:"))
	// 正文
	var md string
	var err error
	if len(params["body"].(string)) == 0 {
		md = fmt.Sprintf("%s\n", cs.Muted("未提供描述"))
	} else {
		md, err = markdown.Render(params["body"].(string),
			markdown.WithTheme(io.TerminalTheme()),
			markdown.WithWrap(io.TerminalWidth()))
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%s", md)

	return nil
}

func previewPR(opts CreateOptions, openURL string) error {
	if opts.IO.IsStdinTTY() && opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s。\n", text.DisplayURL(openURL))
	}
	return opts.Browser.Browse(openURL)
}

func handlePush(opts CreateOptions, ctx CreateContext) error {
	refs := ctx.PRRefs
	forkableRefs, requiresFork := refs.(forkableRefs)
	if requiresFork {
		opts.IO.StartProgressIndicator()
		forkedRepo, err := api.ForkRepo(ctx.Client, forkableRefs.BaseRepo(), "", "", true)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return fmt.Errorf("fork 仓库出错: %w", err)
		}

		refs = pushableRefs{
			headRepo:       forkedRepo,
			headBranchName: forkableRefs.qualifiedHeadRef.BranchName(),
			baseRepo:       forkableRefs.baseRepo,
			baseBranchName: forkableRefs.baseBranchName,
		}
	}

	// 我们可能在 fork 时向上转型(upcast)为 pushableRefs，也可能本来就传入了一个实例。
	// 但如果两者都不是，那么无需再做任何事。
	pushableRefs, ok := refs.(pushableRefs)
	if !ok {
		return nil
	}

	// 源仓库(head repo)的已有远端会缺失(并返回错误)的情况有两种:
	// 1. 源仓库刚由自动 fork 创建;
	// 2. 通过查询 API 发现了一个已有的 fork。
	// 无论哪种情况，我们都要将源仓库添加为新的 git 远端(remote)，以便向其推送(push)。
	// 我们会尝试将源仓库添加为 "origin" 远端，若该名称不可用则回退到 "fork" 远端。
	// 此外，如果基础仓库正是 "origin" 远端，我们会将其重命名为 "upstream"。
	headRemote, _ := ctx.ResolvedRemotes.RemoteForRepo(pushableRefs.HeadRepo())
	if headRemote == nil {
		cfg, err := opts.Config()
		if err != nil {
			return err
		}

		remotes, err := opts.Remotes()
		if err != nil {
			return err
		}

		cloneProtocol := cfg.GitProtocol(pushableRefs.HeadRepo().RepoHost()).Value
		headRepoURL := ghrepo.FormatRemoteURL(pushableRefs.HeadRepo(), cloneProtocol)
		gitClient := ctx.GitClient
		origin, _ := remotes.FindByName("origin")
		upstreamName := "upstream"
		upstream, _ := remotes.FindByName(upstreamName)
		remoteName := "origin"

		if origin != nil {
			remoteName = "fork"
		}

		if origin != nil && upstream == nil && ghrepo.IsSame(origin, pushableRefs.BaseRepo()) {
			renameCmd, err := gitClient.Command(context.Background(), "remote", "rename", "origin", upstreamName)
			if err != nil {
				return err
			}
			if _, err = renameCmd.Output(); err != nil {
				return fmt.Errorf("重命名 origin 远端(remote)出错: %w", err)
			}
			remoteName = "origin"
			fmt.Fprintf(opts.IO.ErrOut, "已将 %s 远端更改为 %q\n", ghrepo.FullName(pushableRefs.BaseRepo()), upstreamName)
		}

		gitRemote, err := gitClient.AddRemote(context.Background(), remoteName, headRepoURL, []string{})
		if err != nil {
			return fmt.Errorf("添加远端(remote)出错: %w", err)
		}

		fmt.Fprintf(opts.IO.ErrOut, "已将 %s 添加为远端(remote) %q\n", ghrepo.FullName(pushableRefs.HeadRepo()), remoteName)

		// 仅当 `gh pr create` 创建了该远端时，才将 `upstream` 远端标记为默认。
		if requiresFork {
			err := gitClient.SetRemoteResolution(context.Background(), upstreamName, "base")
			if err != nil {
				return fmt.Errorf("设置 upstream 为默认失败: %w", err)
			}

			if opts.IO.IsStdoutTTY() {
				cs := opts.IO.ColorScheme()
				fmt.Fprintf(opts.IO.ErrOut, "%s 仓库 %s 已设为默认仓库。要了解默认仓库的更多信息，请运行: gh repo set-default --help\n", cs.WarningIcon(), cs.Bold(ghrepo.FullName(pushableRefs.HeadRepo())))
			}
		}

		headRemote = &ghContext.Remote{
			Remote: gitRemote,
			Repo:   pushableRefs.HeadRepo(),
		}
	}

	// 若该分支(branch)尚未推送到任何地方，则自动推送它
	pushBranch := func() error {
		w := NewRegexpWriter(opts.IO.ErrOut, gitPushRegexp, "")
		defer w.Flush()
		ref := fmt.Sprintf("HEAD:refs/heads/%s", ctx.PRRefs.UnqualifiedHeadRef())
		bo := backoff.NewConstantBackOff(2 * time.Second)
		root := context.Background()
		return backoff.Retry(func() error {
			if err := ctx.GitClient.Push(root, headRemote.Name, ref, git.WithStderr(w)); err != nil {
				// 仅在已 fork 该仓库时重试，否则首次推送(push)就应成功。
				if requiresFork {
					fmt.Fprintf(opts.IO.ErrOut, "等待 2 秒后重试...\n")
					return err
				}
				return backoff.Permanent(err)
			}
			return nil
		}, backoff.WithContext(backoff.WithMaxRetries(bo, 3), root))
	}

	return pushBranch()
}

func generateCompareURL(ctx CreateContext, state shared.IssueMetadataState, projectsV1Support gh.ProjectsV1Support) (string, error) {
	u := ghrepo.GenerateRepoURL(
		ctx.PRRefs.BaseRepo(),
		"compare/%s...%s?expand=1",
		url.PathEscape(ctx.PRRefs.BaseRef()), url.PathEscape(ctx.PRRefs.QualifiedHeadRef()))
	url, err := shared.WithPrAndIssueQueryParams(ctx.Client, ctx.PRRefs.BaseRepo(), u, state, projectsV1Support)
	if err != nil {
		return "", err
	}
	return url, nil
}

// Humanize 返回字符串 s 的副本，将其中所有的 '-' 和 '_' 替换为空格。
func humanize(s string) string {
	replace := "_-"
	h := func(r rune) rune {
		if strings.ContainsRune(replace, r) {
			return ' '
		}
		return r
	}
	return strings.Map(h, s)
}

func requestableReviewersForCompletion(opts *CreateOptions) ([]string, error) {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return nil, err
	}

	remotes, err := getRemotes(opts)
	if err != nil {
		return nil, err
	}
	repoContext, err := ghContext.ResolveRemotesToRepos(remotes, api.NewClientFromHTTP(httpClient), opts.RepoOverride)
	if err != nil {
		return nil, err
	}
	baseRepo, err := repoContext.BaseRepo(opts.IO)
	if err != nil {
		return nil, err
	}

	return shared.RequestableReviewersForCompletion(httpClient, baseRepo)
}

var gitPushRegexp = regexp.MustCompile("^remote: (Create a pull request.*by visiting|[[:space:]]*https://.*/pull/new/).*\n?$")
