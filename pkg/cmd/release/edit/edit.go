package edit

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type EditOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)

	TagName            string
	Target             string
	Name               *string
	Body               *string
	DiscussionCategory *string
	Draft              *bool
	Prerelease         *bool
	IsLatest           *bool
	VerifyTag          bool
}

func NewCmdEdit(f *cmdutil.Factory, runF func(*EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	var notesFile string

	cmd := &cobra.Command{
		DisableFlagsInUseLine: true,

		Use:   "edit <tag>",
		Short: "编辑发布(release)",
		Example: heredoc.Doc(`
			# 将之前为草稿的发布(release)正式发布
			$ gh release edit v1.0 --draft=false

			# 从文件内容更新发布说明
			$ gh release edit v1.0 --notes-file /path/to/release_notes.md
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			if cmd.Flags().NFlag() == 0 {
				return cmdutil.FlagErrorf("请使用 flag 指定要编辑的属性")
			}

			if notesFile != "" {
				b, err := cmdutil.ReadFile(notesFile, opts.IO.In)
				if err != nil {
					return err
				}
				body := string(b)
				opts.Body = &body
			}

			if runF != nil {
				return runF(opts)
			}
			return editRun(args[0], opts)
		},
	}

	cmdutil.NilBoolFlag(cmd, &opts.Draft, "draft", "", "将发布保存为草稿(draft)而不是正式发布")
	cmdutil.NilBoolFlag(cmd, &opts.Prerelease, "prerelease", "", "将发布标记为预发布(pre-release)")
	cmdutil.NilBoolFlag(cmd, &opts.IsLatest, "latest", "", "显式将该发布标记为\"最新(Latest)\"")
	cmdutil.NilStringFlag(cmd, &opts.Body, "notes", "n", "发布说明(release notes)")
	cmdutil.NilStringFlag(cmd, &opts.Name, "title", "t", "发布标题")
	cmdutil.NilStringFlag(cmd, &opts.DiscussionCategory, "discussion-category", "", "发布草稿时在指定分类下发起讨论(discussion)")
	cmd.Flags().StringVar(&opts.Target, "target", "", "目标 `branch` 或完整提交 SHA (默认 [main 分支])")
	cmd.Flags().StringVar(&opts.TagName, "tag", "", "标签(tag)名称")
	cmd.Flags().StringVarP(&notesFile, "notes-file", "F", "", "从 `file` 读取发布说明 (使用 \"-\" 从标准输入读取)")
	cmd.Flags().BoolVar(&opts.VerifyTag, "verify-tag", false, "若 git 标签(tag)在远端仓库中不存在则中止")

	_ = cmdutil.RegisterBranchCompletionFlags(f.GitClient, cmd, "target")

	return cmd
}

func editRun(tag string, opts *EditOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	release, err := shared.FetchRelease(context.Background(), httpClient, baseRepo, tag)
	if err != nil {
		return err
	}

	params := getParams(opts)

	// 如果不提供任何标签(tag)名，API 会将当前标签从发布(release)中移除
	if _, ok := params["tag_name"]; !ok {
		params["tag_name"] = release.TagName
	}

	if opts.VerifyTag && opts.TagName != "" {
		remoteTagPresent, err := remoteTagExists(httpClient, baseRepo, opts.TagName)
		if err != nil {
			return err
		}
		if !remoteTagPresent {
			return fmt.Errorf("标签(tag) %s 在仓库 %s 中不存在，因 --verify-tag flag 而中止",
				opts.TagName, ghrepo.FullName(baseRepo))
		}
	}

	editedRelease, err := editRelease(httpClient, baseRepo, release.DatabaseID, params)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.IO.Out, "%s\n", editedRelease.URL)

	return nil
}

func getParams(opts *EditOptions) map[string]any {
	params := map[string]any{}

	if opts.Body != nil {
		params["body"] = opts.Body
	}

	if opts.DiscussionCategory != nil {
		params["discussion_category_name"] = *opts.DiscussionCategory
	}

	if opts.Draft != nil {
		params["draft"] = *opts.Draft
	}

	if opts.Name != nil {
		params["name"] = opts.Name
	}

	if opts.Prerelease != nil {
		params["prerelease"] = *opts.Prerelease
	}

	if opts.TagName != "" {
		params["tag_name"] = opts.TagName
	}

	if opts.Target != "" {
		params["target_commitish"] = opts.Target
	}

	if opts.IsLatest != nil {
		// 有效取值: true/false/legacy
		params["make_latest"] = fmt.Sprintf("%v", *opts.IsLatest)
	}

	return params
}
