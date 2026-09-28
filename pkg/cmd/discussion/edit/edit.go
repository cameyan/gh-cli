package edit

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

// EditOptions 保存 discussion edit 命令的配置。
type EditOptions struct {
	IO       *iostreams.IOStreams
	BaseRepo func() (ghrepo.Interface, error)
	Client   func() (client.DiscussionClient, error)
	Prompter prompter.Prompter

	Interactive      bool
	TitleProvided    bool
	BodyProvided     bool
	CategoryProvided bool
	LabelsProvided   bool

	DiscussionNumber int32
	Title            string
	Body             string
	BodyFile         string
	Category         string
	AddLabels        []string
	RemoveLabels     []string
}

// NewCmdEdit 返回用于编辑 GitHub 讨论的 cobra 命令。
func NewCmdEdit(f *cmdutil.Factory, runF func(*EditOptions) error) *cobra.Command {
	opts := &EditOptions{
		IO:       f.IOStreams,
		Prompter: f.Prompter,
		Client:   shared.DiscussionClientFunc(f),
	}

	cmd := &cobra.Command{
		Use:   "edit {<number> | <discussion-url>} [flags]",
		Short: "编辑讨论(预览版)",
		Long: heredoc.Doc(`
			编辑 GitHub 讨论。

			不带任何标志且连接到终端时，命令将以交互方式运行。
			使用标志可以非交互方式更新特定字段。
		`),
		Example: heredoc.Doc(`
			# 交互式编辑
			$ gh discussion edit 123

			# 更新标题、内容和分类
			$ gh discussion edit 123 --title "Updated title" --body "Updated body" --category "Ideas"

			# 从文件更新内容
			$ gh discussion edit 123 --body-file body.md

			# 添加和移除标签
			$ gh discussion edit 123 --add-label "bug,help wanted" --remove-label "stale"
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, repo, err := shared.ParseDiscussionArg(args[0])
			if err != nil {
				return cmdutil.FlagErrorWrap(err)
			}

			if repo != nil {
				opts.BaseRepo = func() (ghrepo.Interface, error) {
					return repo, nil
				}
			} else {
				opts.BaseRepo = f.BaseRepo
			}

			opts.DiscussionNumber = number

			if err := cmdutil.MutuallyExclusive("specify only one of --body or --body-file",
				cmd.Flags().Changed("body"), cmd.Flags().Changed("body-file")); err != nil {
				return err
			}

			opts.TitleProvided = cmd.Flags().Changed("title")
			opts.BodyProvided = cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file")
			opts.CategoryProvided = cmd.Flags().Changed("category")
			opts.LabelsProvided = len(opts.AddLabels) > 0 || len(opts.RemoveLabels) > 0

			noFlagsSet := !opts.TitleProvided && !opts.BodyProvided && !opts.CategoryProvided && !opts.LabelsProvided
			if noFlagsSet && !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式更新讨论时至少需要指定一个标志")
			}

			opts.Interactive = noFlagsSet

			if runF != nil {
				return runF(opts)
			}
			return editRun(opts)
		},
	}

	cmdutil.EnableRepoOverride(cmd, f)

	cmd.Flags().StringVarP(&opts.Title, "title", "t", "", "讨论的新标题")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "讨论的新内容")
	cmd.Flags().StringVarP(&opts.BodyFile, "body-file", "F", "", "从文件读取讨论内容(使用 \"-\" 表示从标准输入读取)")
	cmd.Flags().StringVarP(&opts.Category, "category", "c", "", "讨论的新分类名称或 slug")
	cmd.Flags().StringSliceVar(&opts.AddLabels, "add-label", nil, "按 `name` 添加标签")
	cmd.Flags().StringSliceVar(&opts.RemoveLabels, "remove-label", nil, "按 `name` 移除标签")

	return cmd
}

func editRun(opts *EditOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	c, err := opts.Client()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	discussion, err := c.GetByNumber(repo, opts.DiscussionNumber)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	input := client.UpdateDiscussionInput{
		DiscussionID: discussion.ID,
	}

	if opts.Interactive {
		changed, err := promptEdit(opts, discussion, c, repo, &input)
		if err != nil {
			return err
		}

		if !changed {
			return cmdutil.CancelError
		}
	} else {
		if opts.TitleProvided {
			if strings.TrimSpace(opts.Title) == "" {
				return cmdutil.FlagErrorf("title cannot be blank")
			}
			input.Title = &opts.Title
		}
		if opts.BodyProvided {
			if opts.BodyFile != "" {
				bodyBytes, err := cmdutil.ReadFile(opts.BodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.Body = string(bodyBytes)
			}
			input.Body = &opts.Body
		}
		if opts.CategoryProvided {
			opts.IO.StartProgressIndicator()
			categories, err := c.ListCategories(repo)
			opts.IO.StopProgressIndicator()
			if err != nil {
				return err
			}
			cat, err := shared.MatchCategory(opts.Category, categories)
			if err != nil {
				return err
			}
			input.CategoryID = &cat.ID
		}

		if opts.LabelsProvided {
			opts.IO.StartProgressIndicator()
			allLabels, err := c.ListLabels(repo)
			opts.IO.StopProgressIndicator()
			if err != nil {
				return fmt.Errorf("获取标签失败: %w", err)
			}
			if len(opts.AddLabels) > 0 {
				input.AddLabelIDs, err = shared.ResolveLabels(allLabels, opts.AddLabels)
				if err != nil {
					return err
				}
			}
			if len(opts.RemoveLabels) > 0 {
				input.RemoveLabelIDs, err = shared.ResolveLabels(allLabels, opts.RemoveLabels)
				if err != nil {
					return err
				}
			}
		}
	}

	opts.IO.StartProgressIndicator()
	updated, err := c.Update(repo, input)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if updated != nil {
			fmt.Fprintln(opts.IO.Out, updated.URL)
			fmt.Fprintln(opts.IO.ErrOut, err.Error())
			return cmdutil.SilentError
		}
		return err
	}

	fmt.Fprintln(opts.IO.Out, updated.URL)
	return nil
}

// promptEdit 运行交互式流程，将用户选择填充到 input 中。返回一个布尔值表示是否
// 进行了任何修改，若流程失败则返回错误。
func promptEdit(opts *EditOptions, discussion *client.Discussion, c client.DiscussionClient, repo ghrepo.Interface, input *client.UpdateDiscussionInput) (bool, error) {
	choices := []string{"Title", "Body", "Category"}
	selected, err := opts.Prompter.MultiSelect("想要编辑哪些内容?", nil, choices)
	if err != nil {
		return false, err
	}
	if len(selected) == 0 {
		return false, nil
	}

	for _, idx := range selected {
		switch choices[idx] {
		case "Title":
			title, err := opts.Prompter.Input("标题", discussion.Title)
			if err != nil {
				return false, err
			}
			if strings.TrimSpace(title) == "" {
				return false, fmt.Errorf("标题不能为空")
			}
			input.Title = &title

		case "Body":
			body, err := opts.Prompter.MarkdownEditor("内容", discussion.Body, false)
			if err != nil {
				return false, err
			}
			input.Body = &body

		case "Category":
			opts.IO.StartProgressIndicator()
			categories, err := c.ListCategories(repo)
			opts.IO.StopProgressIndicator()
			if err != nil {
				return false, err
			}
			names := make([]string, len(categories))
			for i, cat := range categories {
				names[i] = cat.Name
			}
			currentName := discussion.Category.Name
			idx, err := opts.Prompter.Select("分类", currentName, names)
			if err != nil {
				return false, err
			}
			input.CategoryID = &categories[idx].ID
		}
	}

	return true, nil
}
