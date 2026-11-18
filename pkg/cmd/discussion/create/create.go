package create

import (
	"fmt"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/discussion/client"
	"github.com/cli/cli/v2/pkg/cmd/discussion/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

// CreateOptions 保存 discussion create 命令的配置。
type CreateOptions struct {
	IO       *iostreams.IOStreams
	BaseRepo func() (ghrepo.Interface, error)
	Client   func() (client.DiscussionClient, error)
	Prompter prompter.Prompter

	Title    string
	Body     string
	BodyFile string
	Category string
	Labels   []string
}

// NewCmdCreate 返回用于创建 GitHub 讨论的 cobra 命令。
func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:       f.IOStreams,
		Prompter: f.Prompter,
		Client:   shared.DiscussionClientFunc(f),
	}

	cmd := &cobra.Command{
		Use:   "create [flags]",
		Short: "创建新的讨论(预览版)",
		Long: heredoc.Docf(`
			在仓库中创建新的 GitHub 讨论。

			当提供 %[1]s--title%[1]s、%[1]s--body%[1]s(或 %[1]s--body-file%[1]s)和 %[1]s--category%[1]s 时，将以非交互方式创建讨论。
			在连接到终端的情况下，省略上述任一标志都会触发交互式提示。
		`, "`"),
		Example: heredoc.Doc(`
			# 交互式创建
			$ gh discussion create

			# 非交互式创建
			$ gh discussion create --title "My question" --category "Q&A" --body "Details here"
		`),
		Args: cmdutil.NoArgsQuoteReminder,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			if err := cmdutil.MutuallyExclusive("specify only one of --body or --body-file",
				cmd.Flags().Changed("body"), cmd.Flags().Changed("body-file")); err != nil {
				return err
			}

			if opts.Title != "" && strings.TrimSpace(opts.Title) == "" {
				return cmdutil.FlagErrorf("标题不能为空")
			}
			if opts.Body != "" && strings.TrimSpace(opts.Body) == "" {
				return cmdutil.FlagErrorf("内容不能为空")
			}
			if opts.Category != "" && strings.TrimSpace(opts.Category) == "" {
				return cmdutil.FlagErrorf("分类不能为空")
			}

			bodyProvided := cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file")
			needsInput := opts.Title == "" || opts.Category == "" || !bodyProvided
			if needsInput && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须指定 --title、--body(或 --body-file)和 --category")
			}

			if runF != nil {
				return runF(opts)
			}
			return createRun(opts)
		},
	}

	cmdutil.EnableRepoOverride(cmd, f)

	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "讨论的标题")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "讨论的内容")
	cmd.Flags().StringVarP(&opts.BodyFile, "body-file", "F", "", "从文件读取讨论内容(使用 \"-\" 表示从标准输入读取)")
	cmd.Flags().StringVarP(&opts.Category, "category", "c", "", "讨论的分类名称或 slug")
	cmd.Flags().StringSliceVarP(&opts.Labels, "label", "l", nil, "为讨论添加的标签")

	return cmd
}

func createRun(opts *CreateOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	categories, err := c.ListCategories(repo)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if opts.Title == "" {
		opts.Title, err = opts.Prompter.Input("讨论标题", "")
		if err != nil {
			return err
		}
		if strings.TrimSpace(opts.Title) == "" {
			return fmt.Errorf("标题不能为空")
		}
	}

	var category *client.DiscussionCategory
	if opts.Category != "" {
		category, err = shared.MatchCategory(opts.Category, categories)
		if err != nil {
			return err
		}
	} else {
		names := make([]string, len(categories))
		for i, cat := range categories {
			names[i] = cat.Name
		}
		idx, err := opts.Prompter.Select("讨论分类", "", names)
		if err != nil {
			return err
		}
		category = &categories[idx]
	}

	if opts.BodyFile != "" {
		bodyBytes, err := cmdutil.ReadFile(opts.BodyFile, opts.IO.In)
		if err != nil {
			return err
		}
		opts.Body = string(bodyBytes)
	}

	if opts.Body == "" {
		opts.Body, err = opts.Prompter.MarkdownEditor("讨论内容", "", false)
		if err != nil {
			return err
		}
		if strings.TrimSpace(opts.Body) == "" {
			return fmt.Errorf("内容不能为空")
		}
	}

	var labelIDs []string
	if len(opts.Labels) > 0 {
		opts.IO.StartProgressIndicator()
		allLabels, err := c.ListLabels(repo)
		opts.IO.StopProgressIndicator()
		if err != nil {
			return err
		}

		labelIDs, err = shared.ResolveLabels(allLabels, opts.Labels)
		if err != nil {
			return err
		}
	}

	input := client.CreateDiscussionInput{
		CategoryID: category.ID,
		Title:      opts.Title,
		Body:       opts.Body,
		LabelIDs:   labelIDs,
	}

	opts.IO.StartProgressIndicator()
	discussion, err := c.Create(repo, input)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if discussion != nil {
			fmt.Fprintln(opts.IO.Out, discussion.URL)
			fmt.Fprintln(opts.IO.ErrOut, err.Error())
			return cmdutil.SilentError
		}
		return fmt.Errorf("创建讨论失败: %w", err)
	}

	fmt.Fprintln(opts.IO.Out, discussion.URL)

	return nil
}
