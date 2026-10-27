package close

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type CloseOptions struct {
	HttpClient func() (*http.Client, error)
	GitClient  *git.Client
	IO         *iostreams.IOStreams
	Branch     func() (string, error)

	Finder shared.PRFinder

	SelectorArg       string
	Comment           string
	DeleteBranch      bool
	DeleteLocalBranch bool
}

func NewCmdClose(f *cmdutil.Factory, runF func(*CloseOptions) error) *cobra.Command {
	opts := &CloseOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Branch:     f.Branch,
	}

	cmd := &cobra.Command{
		Use:   "close {<number> | <url> | <branch>}",
		Short: "关闭拉取请求(PR)",
		Args:  cmdutil.ExactArgs(1, "无法关闭拉取请求(PR): 需要编号(number)、URL 或分支(branch)"),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Finder = shared.NewFinder(f)

			if len(args) > 0 {
				opts.SelectorArg = args[0]
			}

			opts.DeleteLocalBranch = !cmd.Flags().Changed("repo")

			if runF != nil {
				return runF(opts)
			}
			return closeRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Comment, "comment", "c", "", "留一条关闭评论")
	cmd.Flags().BoolVarP(&opts.DeleteBranch, "delete-branch", "d", false, "关闭后删除本地与远端分支(branch)")

	return cmd
}

func closeRun(opts *CloseOptions) error {
	cs := opts.IO.ColorScheme()

	findOptions := shared.FindOptions{
		Selector: opts.SelectorArg,
		Fields:   []string{"state", "number", "title", "isCrossRepository", "headRefName"},
	}
	pr, baseRepo, err := opts.Finder.Find(findOptions)
	if err != nil {
		return err
	}

	if pr.State == "MERGED" {
		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d (%s) 已被合并，无法关闭\n", cs.FailureIcon(), ghrepo.FullName(baseRepo), pr.Number, pr.Title)
		return cmdutil.SilentError
	} else if !pr.IsOpen() {
		fmt.Fprintf(opts.IO.ErrOut, "%s 拉取请求(PR) %s#%d (%s) 已处于关闭状态\n", cs.WarningIcon(), ghrepo.FullName(baseRepo), pr.Number, pr.Title)
		return nil
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	if opts.Comment != "" {
		commentOpts := &shared.CommentableOptions{
			Body:       opts.Comment,
			HttpClient: opts.HttpClient,
			InputType:  shared.InputTypeInline,
			Quiet:      true,
			RetrieveCommentable: func() (shared.Commentable, ghrepo.Interface, error) {
				return pr, baseRepo, nil
			},
		}
		err := shared.CommentableRun(commentOpts)
		if err != nil {
			return err
		}
	}

	err = api.PullRequestClose(httpClient, baseRepo, pr.ID)
	if err != nil {
		return fmt.Errorf("API 调用失败: %w", err)
	}

	fmt.Fprintf(opts.IO.ErrOut, "%s 已关闭拉取请求(PR) %s#%d (%s)\n", cs.SuccessIconWithColor(cs.Red), ghrepo.FullName(baseRepo), pr.Number, pr.Title)

	if opts.DeleteBranch {
		ctx := context.Background()
		branchSwitchString := ""
		apiClient := api.NewClientFromHTTP(httpClient)
		localBranchExists := opts.GitClient.HasLocalBranch(ctx, pr.HeadRefName)

		if opts.DeleteLocalBranch {
			if localBranchExists {
				currentBranch, err := opts.Branch()
				if err != nil {
					return err
				}

				var branchToSwitchTo string
				if currentBranch == pr.HeadRefName {
					branchToSwitchTo, err = api.RepoDefaultBranch(apiClient, baseRepo)
					if err != nil {
						return err
					}
					err = opts.GitClient.CheckoutBranch(ctx, branchToSwitchTo)
					if err != nil {
						return err
					}
				}

				if err := opts.GitClient.DeleteLocalBranch(ctx, pr.HeadRefName); err != nil {
					return fmt.Errorf("删除本地分支(branch) %s 失败: %w", cs.Cyan(pr.HeadRefName), err)
				}

				if branchToSwitchTo != "" {
					branchSwitchString = fmt.Sprintf(" 并已切换到分支(branch) %s", cs.Cyan(branchToSwitchTo))
				}
			} else {
				fmt.Fprintf(opts.IO.ErrOut, "%s 当前目录不是 git 仓库，已跳过删除本地分支(branch)\n", cs.WarningIcon())
			}
		}

		if pr.IsCrossRepository {
			fmt.Fprintf(opts.IO.ErrOut, "%s 来自 fork 的拉取请求(PR)，已跳过删除其远端分支(branch)\n", cs.WarningIcon())
			if !opts.DeleteLocalBranch {
				return nil
			}
		} else {
			if err := api.BranchDeleteRemote(apiClient, baseRepo, pr.HeadRefName); err != nil {
				return fmt.Errorf("删除远端分支(branch) %s 失败: %w", cs.Cyan(pr.HeadRefName), err)
			}
		}
		fmt.Fprintf(opts.IO.ErrOut, "%s 已删除分支(branch) %s%s\n", cs.SuccessIconWithColor(cs.Red), cs.Cyan(pr.HeadRefName), branchSwitchString)
	}

	return nil
}
