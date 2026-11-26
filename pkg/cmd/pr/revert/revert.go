package revert

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type RevertOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Finder shared.PRFinder

	SelectorArg string

	Body    string
	BodySet bool
	Title   string
	IsDraft bool
}

func NewCmdRevert(f *cmdutil.Factory, runF func(*RevertOptions) error) *cobra.Command {
	opts := &RevertOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	var bodyFile string

	cmd := &cobra.Command{
		Use:   "revert {<number> | <url> | <branch>}",
		Short: "回退(revert)一个拉取请求(PR)",
		Args:  cmdutil.ExactArgs(1, "无法回退拉取请求(PR): 需要编号(number)、URL 或分支(branch)"),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			bodyProvided := cmd.Flags().Changed("body")
			bodyFileProvided := bodyFile != ""

			if err := cmdutil.MutuallyExclusive(
				"只能指定 `--body` 或 `--body-file` 之一",
				bodyProvided,
				bodyFileProvided,
			); err != nil {
				return err
			}

			if bodyProvided || bodyFileProvided {
				opts.BodySet = true
				if bodyFileProvided {
					b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
					if err != nil {
						return err
					}
					opts.Body = string(b)
				}
			}

			if runF != nil {
				return runF(opts)
			}
			return revertRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.IsDraft, "draft", "d", false, "将回退(revert)拉取请求(PR)标记为草稿(draft)")
	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "回退(revert)拉取请求(PR)的标题")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "回退(revert)拉取请求(PR)的正文")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "从 `file` 读取正文文本 (使用 \"-\" 从标准输入读取)")
	return cmd
}

func revertRun(opts *RevertOptions) error {
	cs := opts.IO.ColorScheme()

	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"id", "number", "state", "title"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}
	if pr.State != "MERGED" {
		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d (%s) 尚未合并，无法回退(revert)\n", cs.FailureIcon(), ghrepo.FullName(baseRepo), pr.Number, pr.Title)
		return cmdutil.SilentError
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	params := githubv4.RevertPullRequestInput{
		PullRequestID: pr.ID,
		Draft:         new(githubv4.Boolean(opts.IsDraft)),
	}
	// 仅在 opts.BodySet 为 true 时设置 Body 字段，以避免覆盖
	// GitHub 默认生成的回退(revert)正文。
	if opts.BodySet {
		params.Body = new(githubv4.String(opts.Body))
	}
	// 仅在 opts.Title 非空时设置 Title 字段，以避免覆盖
	// GitHub 默认生成的回退(revert)标题。
	if opts.Title != "" {
		params.Title = new(githubv4.String(opts.Title))
	}

	revertPR, err := api.PullRequestRevert(apiClient, baseRepo, params)
	if err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "%s %s\n", cs.FailureIcon(), err)
		return fmt.Errorf("API 调用失败: %w", err)
	}

	if revertPR != nil {
		fmt.Fprintln(opts.IO.Out, revertPR.URL)
	}
	return nil
}
