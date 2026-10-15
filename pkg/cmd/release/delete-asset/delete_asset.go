package deleteasset

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
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

type DeleteAssetOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter

	TagName     string
	SkipConfirm bool
	AssetName   string
}

func NewCmdDeleteAsset(f *cmdutil.Factory, runF func(*DeleteAssetOptions) error) *cobra.Command {
	opts := &DeleteAssetOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Prompter:   f.Prompter,
	}

	cmd := &cobra.Command{
		Use:   "delete-asset <tag> <asset-name>",
		Short: "从发布(release)中删除资产(asset)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo
			opts.TagName = args[0]
			opts.AssetName = args[1]
			if runF != nil {
				return runF(opts)
			}
			return deleteAssetRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "跳过确认提示")

	return cmd
}

func deleteAssetRun(opts *DeleteAssetOptions) error {
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
			fmt.Sprintf("删除 %s 中发布 %s 的资产 %s?", ghrepo.FullName(baseRepo), release.TagName, opts.AssetName),
			true)
		if err != nil {
			return err
		}

		if !confirmed {
			return cmdutil.CancelError
		}
	}

	var assetURL string
	for _, a := range release.Assets {
		if a.Name == opts.AssetName {
			assetURL = a.APIURL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("发布 %s 中未找到资产(asset) %s", release.TagName, opts.AssetName)
	}

	err = deleteAsset(httpClient, baseRepo.RepoHost(), safeurl.NewImmutableSafeURL(assetURL))
	if err != nil {
		return err
	}

	if !opts.IO.IsStdoutTTY() || !opts.IO.IsStderrTTY() {
		return nil
	}

	cs := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.ErrOut, "%s 已从发布 %s 中删除资产(asset) %s\n", cs.SuccessIconWithColor(cs.Red), release.TagName, opts.AssetName)

	return nil
}

func deleteAsset(httpClient *http.Client, host string, assetURL safeurl.SafeURL) error {
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	return api.NewClientFromHTTP(httpClient).REST(host, http.MethodDelete, assetURL.String(), nil, nil)
}
