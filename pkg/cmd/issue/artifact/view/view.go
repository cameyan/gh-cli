package view

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/issue/artifact/client"
	"github.com/cli/cli/v2/pkg/cmd/issue/artifact/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/markdown"
	"github.com/spf13/cobra"
)

// ViewOptions holds the flags and dependencies of gh issue artifact view.
type ViewOptions struct {
	IO       *iostreams.IOStreams
	BaseRepo func() (ghrepo.Interface, error)
	Client   func() (client.ArtifactClient, error)
	Browser  browser.Browser
	Exporter cmdutil.Exporter
	Now      func() time.Time

	IssueNumber    int
	ArtifactNumber int
	// Version is the version to show from the edit history. 0 shows the
	// current version.
	Version int
	WebMode bool
}

// NewCmdView creates the gh issue artifact view command.
func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:      f.IOStreams,
		Browser: f.Browser,
		Now:     time.Now,
	}

	cmd := &cobra.Command{
		Use:   "view {<issue> <artifact-number> | <artifact-url>}",
		Short: "查看一个 issue 产物 (预览版)",
		Long: heredoc.Docf(`
			显示 issue 产物的名称、类型、作者、内容和编辑历史。一个链接显示其 URL。

			产物的名称由其 issue 和编号或其 URL 命名。%[1]s<issue>%[1]s 是 issue 编号或 URL。

			默认情况下，显示当前版本。使用 %[1]s--version%[1]s 可从编辑历史中显示历史版本。

			使用 %[1]s--web%[1]s，可在 GitHub 上打开文档页面或链接的 URL。
		`, "`"),
		Example: heredoc.Doc(`
			# 查看 issue 142 中的产物 2
			$ gh issue artifact view 142 2

			# 查看历史版本
			$ gh issue artifact view 142 2 --version 5

			# 通过 URL 查看产物
			$ gh issue artifact view https://github.com/monalisa/monas-cafe/issues/142/artifacts/3

			# 在浏览器中打开产物链接
			$ gh issue artifact view 142 3 --web

			# 打印产物的内容
			$ gh issue artifact view 142 2 --json body --jq .body
		`),
		Args: cmdutil.RangeArgs(1, 2, shared.MissingArtifactMessage),
		RunE: func(cmd *cobra.Command, args []string) error {
			versionSet := cmd.Flags().Changed("version")
			if err := cmdutil.MutuallyExclusive("仅指定 `--version` 或 `--json` 其中之一即可", versionSet, opts.Exporter != nil); err != nil {
				return err
			}
			if err := cmdutil.MutuallyExclusive("仅指定 `--version` 或 `--web` 其中之一即可", versionSet, opts.WebMode); err != nil {
				return err
			}
			if versionSet && opts.Version < 1 {
				return cmdutil.FlagErrorf("无效版本: %v", opts.Version)
			}

			var err error
			opts.IssueNumber, opts.BaseRepo, opts.ArtifactNumber, err = shared.ParseArtifactArg(args, f.BaseRepo)
			if err != nil {
				return cmdutil.FlagErrorWrap(err)
			}
			opts.Client = shared.ClientFunc(f)

			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.WebMode, "web", "w", false, "在 GitHub 上打开文档的页面，或链接的 URL")
	cmd.Flags().IntVar(&opts.Version, "version", 0, "显示编辑历史中带有此 `编号` 的版本")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, client.ArtifactWithVersionsFields)

	return cmd
}

func viewRun(opts *ViewOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}
	if err := shared.CheckHost(repo.RepoHost()); err != nil {
		return err
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}
	if err := shared.CheckIssue(c, repo, opts.IssueNumber); err != nil {
		return err
	}

	artifact, err := c.Get(repo, opts.IssueNumber, opts.ArtifactNumber)
	if err != nil {
		return err
	}
	if err := shared.CheckType(artifact.Artifact); err != nil {
		return err
	}
	viewing, err := shared.FindVersion(artifact, opts.Version)
	if err != nil {
		return err
	}

	if opts.WebMode {
		return openInBrowser(opts, artifact.Artifact)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, artifact)
	}

	if !opts.IO.IsStdoutTTY() {
		printRawArtifact(opts.IO.Out, artifact, viewing)
		return nil
	}

	opts.IO.DetectTerminalTheme()
	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	return printHumanArtifact(opts, repo, artifact, viewing)
}

// openInBrowser opens a document's page on GitHub, or a link's URL. The page
// comes from the API, so the message shows it like gh issue view --web does.
// A link's URL is anything its author saved, so only an http(s) URL opens,
// and the message shows the whole URL, where text.DisplayURL would hide its
// port, query and fragment.
func openInBrowser(opts *ViewOptions, a client.Artifact) error {
	target, shown := a.URL, text.DisplayURL(a.URL)
	if a.Type == client.TypeLink {
		u, ok := shared.HTTPURL(a.Body)
		if !ok {
			return fmt.Errorf("产物 %d 未链接到 http(s) URL，`--web` 仅支持打开 http(s) URL", a.Number)
		}
		target, shown = u, u
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", shown)
	}
	return opts.Browser.Browse(target)
}

// printRawArtifact prints key lines in a fixed order, then the body after a
// "--" line, like gh issue view. Null values print as empty fields, so every
// artifact has the same lines. An earlier version replaces the name, version,
// editor and body, while the creator and URL still describe the artifact.
func printRawArtifact(out io.Writer, a *client.ArtifactWithVersions, viewing *client.Version) {
	name, body := a.Name, a.Body
	updatedBy, updatedAt := a.UpdatedByActor, a.UpdatedAt
	var version string
	if len(a.Versions) > 0 {
		version = strconv.Itoa(a.Versions[0].Version)
	}
	if viewing != nil {
		name, body = viewing.Name, viewing.Body
		updatedBy, updatedAt = viewing.Actor, viewing.CreatedAt
		version = strconv.Itoa(viewing.Version)
	}

	fmt.Fprintf(out, "名称:\t%s\n", text.RemoveExcessiveWhitespace(name))
	fmt.Fprintf(out, "编号:\t%d\n", a.Number)
	fmt.Fprintf(out, "版本:\t%s\n", version)
	fmt.Fprintf(out, "类型:\t%s\n", a.Type)
	fmt.Fprintf(out, "创建者:\t%s\n", login(a.Creator))
	fmt.Fprintf(out, "创建日期:\t%s\n", timestamp(a.CreatedAt))
	fmt.Fprintf(out, "更新者:\t%s\n", login(updatedBy))
	fmt.Fprintf(out, "更新日期:\t%s\n", timestamp(updatedAt))
	fmt.Fprintf(out, "链接:\t%s\n", a.URL)
	fmt.Fprintln(out, "--")
	fmt.Fprintln(out, body)
}

func printHumanArtifact(opts *ViewOptions, repo ghrepo.Interface, a *client.ArtifactWithVersions, viewing *client.Version) error {
	out := opts.IO.Out
	cs := opts.IO.ColorScheme()
	now := opts.Now()

	name, body := a.Name, a.Body
	if viewing != nil {
		name, body = viewing.Name, viewing.Body
	}

	fmt.Fprintf(out, "%s %s#%d\n", cs.Bold(text.RemoveExcessiveWhitespace(name)), ghrepo.FullName(repo), opts.IssueNumber)
	fmt.Fprintln(out, cs.Muted(metadataLine(a.Artifact, viewing, now)))

	switch a.Type {
	case client.TypeLink:
		fmt.Fprintf(out, "\n  %s\n\n", strings.TrimSpace(body))
	case client.TypeEmbeddedHTML:
		// gh never renders HTML, so the source prints as stored, the way gh
		// gist view prints a file that isn't Markdown. The API client has
		// already turned any escape sequences into caret notation, so the
		// source can't control the terminal.
		fmt.Fprintf(out, "\n%s", body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out)
	default:
		md, err := markdown.Render(body,
			markdown.WithTheme(opts.IO.TerminalTheme()),
			markdown.WithWrap(opts.IO.TerminalWidth()))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s\n", md)
	}

	if len(a.Versions) > 0 {
		fmt.Fprintln(out, cs.Bold("编辑历史"))
		if err := printEditHistory(opts.IO, now, a.Versions, viewing); err != nil {
			return err
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, cs.Muted("在 GitHub 上查看此产物: "+a.URL))
	return nil
}

// metadataLine describes the artifact under its name: its number, type,
// creator and last editor, or for an earlier version, that version's number
// and who saved it. A part whose time is null is left out, and so is the
// update of an artifact that was never edited.
func metadataLine(a client.Artifact, viewing *client.Version, now time.Time) string {
	parts := []string{fmt.Sprintf("产物 %d", a.Number), a.Type}

	if viewing != nil {
		parts = append(parts, fmt.Sprintf("版本 %d", viewing.Version))
		if viewing.CreatedAt != nil {
			action := "updated"
			if viewing.Version == 1 {
				action = "created"
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", displayName(viewing.Actor), action, text.FuzzyAgo(now, *viewing.CreatedAt)))
		}
		return strings.Join(parts, " • ")
	}

	if a.CreatedAt != nil {
		parts = append(parts, fmt.Sprintf("%s 已创建 %s", displayName(a.Creator), text.FuzzyAgo(now, *a.CreatedAt)))
	}
	if a.UpdatedAt != nil && (a.CreatedAt == nil || !a.UpdatedAt.Equal(*a.CreatedAt)) {
		parts = append(parts, fmt.Sprintf("%s 已更新 %s", displayName(a.UpdatedByActor), text.FuzzyAgo(now, *a.UpdatedAt)))
	}
	return strings.Join(parts, " • ")
}

// printEditHistory lists every version the API returned, newest first, with
// who saved it and when. "Current" marks the newest version, and "Viewing"
// marks the earlier version shown.
func printEditHistory(ios *iostreams.IOStreams, now time.Time, versions []client.Version, viewing *client.Version) error {
	cs := ios.ColorScheme()
	// The "Edit history" heading stands in for column headers.
	tp := tableprinter.New(ios, tableprinter.NoHeader)
	for i, v := range versions {
		tp.AddField(strconv.Itoa(v.Version))
		if v.CreatedAt != nil {
			tp.AddField(displayName(v.Actor))
			tp.AddTimeField(now, *v.CreatedAt, cs.Muted)
		} else {
			tp.AddField(login(v.Actor))
			tp.AddField("")
		}
		switch {
		case i == 0:
			tp.AddField("Current", tableprinter.WithColor(cs.CyanBold))
		case viewing != nil && v.Version == viewing.Version:
			tp.AddField("Viewing", tableprinter.WithColor(cs.CyanBold))
		}
		tp.EndRow()
	}
	return tp.Render()
}

// displayName returns a user's login for the terminal. A null user shows as
// ghost, like a deleted user in gh pr view, so callers use it only when the
// user's action has a time.
func displayName(u *client.Actor) string {
	if u == nil {
		return "ghost"
	}
	return u.Login
}

// login returns a user's login, or an empty string for a null user.
func login(u *client.Actor) string {
	if u == nil {
		return ""
	}
	return u.Login
}

// timestamp returns t in RFC 3339 format, or an empty string for a null time.
func timestamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
