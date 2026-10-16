package label

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type cloneOptions struct {
	BaseRepo   func() (ghrepo.Interface, error)
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	SourceRepo ghrepo.Interface
	Force      bool
}

func newCmdClone(f *cmdutil.Factory, runF func(*cloneOptions) error) *cobra.Command {
	opts := cloneOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "clone <source-repository>",
		Short: "将标签(label)从一个仓库克隆到另一个仓库",
		Long: heredoc.Docf(`
			将标签(label)从 GitHub 上的源仓库克隆到目标仓库。
			默认情况下，目标仓库为当前仓库。

			源仓库中的所有标签都会被复制到目标仓库。
			目标仓库中不在源仓库里的标签不会被删除或修改。

			源仓库中已存在于目标仓库的标签将被跳过。
			可以使用 %[1]s--force%[1]s flag 覆盖目标仓库中的已有标签。
		`, "`"),
		Example: heredoc.Doc(`
			# 从 cli/cli 仓库克隆并覆盖标签到当前仓库
			$ gh label clone cli/cli --force

			# 从 cli/cli 仓库克隆标签到 octocat/cli 仓库
			$ gh label clone cli/cli --repo octocat/cli
		`),
		Args: cmdutil.ExactArgs(1, "无法克隆标签: 需要提供 source-repository 参数"),
		RunE: func(c *cobra.Command, args []string) error {
			var err error
			opts.BaseRepo = f.BaseRepo
			opts.SourceRepo, err = ghrepo.FromFullName(args[0])
			if err != nil {
				return err
			}
			if runF != nil {
				return runF(&opts)
			}
			return cloneRun(&opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "覆盖目标仓库中的标签")

	return cmd
}

func cloneRun(opts *cloneOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	opts.IO.StartProgressIndicator()
	successCount, totalCount, err := cloneLabels(httpClient, baseRepo, opts)
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		pluralize := func(num int) string {
			return text.Pluralize(num, "label")
		}

		successCount := int(successCount)
		switch {
		case successCount == totalCount:
			fmt.Fprintf(opts.IO.Out, "%s 已将 %s 从 %s 克隆到 %s\n", cs.SuccessIcon(), pluralize(successCount), ghrepo.FullName(opts.SourceRepo), ghrepo.FullName(baseRepo))
		default:
			fmt.Fprintf(opts.IO.Out, "%s 已将 %s 中的 %d 条从 %s 克隆到 %s\n", cs.WarningIcon(), pluralize(successCount), totalCount, ghrepo.FullName(opts.SourceRepo), ghrepo.FullName(baseRepo))
		}
	}

	return nil
}

func cloneLabels(client *http.Client, destination ghrepo.Interface, opts *cloneOptions) (successCount uint32, totalCount int, err error) {
	successCount = 0
	labels, totalCount, err := listLabels(client, opts.SourceRepo, listQueryOptions{Limit: -1})
	if err != nil {
		return
	}

	workers := 10
	toCreate := make(chan createOptions)

	wg, ctx := errgroup.WithContext(context.Background())
	for range workers {
		wg.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case l, ok := <-toCreate:
					if !ok {
						return nil
					}
					err := createLabel(client, destination, &l)
					if err != nil {
						if !errors.Is(err, errLabelAlreadyExists) {
							return err
						}
					} else {
						atomic.AddUint32(&successCount, 1)
					}
				}
			}
		})
	}

	for _, label := range labels {
		createOpts := createOptions{
			Name:        label.Name,
			Description: label.Description,
			Color:       label.Color,
			Force:       opts.Force,
		}
		toCreate <- createOpts
	}

	close(toCreate)
	err = wg.Wait()

	return
}
