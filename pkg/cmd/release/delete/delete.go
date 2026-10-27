package delete

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/release/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type iprompter interface {
	Confirm(string, bool) (bool, error)
}

type DeleteOptions struct {
	HttpClient   func() (*http.Client, error)
	GitClient    *git.Client
	IO           *iostreams.IOStreams
	BaseRepo     func() (ghrepo.Interface, error)
	RepoOverride string
	Prompter     iprompter

	TagName     string
	SkipConfirm bool
	CleanupTag  bool
}

func NewCmdDelete(f *cmdutil.Factory, runF func(*DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		GitClient:  f.GitClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete <tag>",
		Short: "删除发布(release)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo
			opts.RepoOverride, _ = cmd.Flags().GetString("repo")

			opts.TagName = args[0]

			if runF != nil {
				return runF(opts)
			}
			return deleteRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "跳过确认提示")
	cmd.Flags().BoolVar(&opts.CleanupTag, "cleanup-tag", false, "除发布(release)外，同时删除指定的标签(tag)")

	return cmd
}

func deleteRun(opts *DeleteOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	release, err := shared.FetchRelease(context.Background(), httpClient, baseRepo, opts.TagName)
	if err != nil {
		return err
	}

	if !opts.SkipConfirm && opts.IO.CanPrompt() {
		confirmed, err := opts.Prompter.Confirm(
			fmt.Sprintf("删除 %s 中的发布(release) %s?", ghrepo.FullName(baseRepo), release.TagName), true)
		if err != nil {
			return err
		}

		if !confirmed {
			return cmdutil.CancelError
		}
	}

	err = deleteRelease(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(release.APIURL))
	if err != nil {
		return err
	}

	var cleanupMessage string
	if opts.CleanupTag {
		if err := deleteTag(httpClient, baseRepo, release.TagName); err != nil {
			return err
		}
		if opts.RepoOverride == "" {
			_ = opts.GitClient.DeleteLocalTag(context.Background(), release.TagName)
		}
		cleanupMessage = " 和标签(tag)"
	}

	if !opts.IO.IsStdoutTTY() || !opts.IO.IsStderrTTY() {
		return nil
	}

	iofmt := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.ErrOut, "%s 已删除发布(release)%s %s\n", iofmt.SuccessIconWithColor(iofmt.Red), cleanupMessage, release.TagName)
	if !release.IsDraft && !opts.CleanupTag {
		fmt.Fprintf(opts.IO.ErrOut, "%s 注意: git 标签(tag) %s 仍保留在仓库中\n", iofmt.WarningIcon(), release.TagName)
	}

	return nil
}

func deleteRelease(httpClient *http.Client, host string, releaseURL safeurl.SafeURL) error {
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(host, http.MethodDelete, releaseURL.String(), nil, nil)
}

func deleteTag(httpClient *http.Client, baseRepo ghrepo.Interface, tagName string) error {
	path, err := safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "git", "refs", fmt.Sprintf("tags/%s", tagName))
	if err != nil {
		return err
	}
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(baseRepo.RepoHost(), http.MethodDelete, path.String(), nil, nil)
}
