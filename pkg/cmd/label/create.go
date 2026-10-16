package label

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

var randomColors = []string{
	"B60205",
	"D93F0B",
	"FBCA04",
	"0E8A16",
	"006B75",
	"1D76DB",
	"0052CC",
	"5319E7",
	"E99695",
	"F9D0C4",
	"FEF2C0",
	"C2E0C6",
	"BFDADC",
	"C5DEF5",
	"BFD4F2",
	"D4C5F9",
}

type createOptions struct {
	BaseRepo   func() (ghrepo.Interface, error)
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams

	Color       string
	Description string
	Name        string
	Force       bool
}

func newCmdCreate(f *cmdutil.Factory, runF func(*createOptions) error) *cobra.Command {
	opts := createOptions{
		HttpClient: f.HttpClient,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "创建一个新标签(label)",
		Long: heredoc.Docf(`
			在 GitHub 上创建一个新标签(label)，或使用 %[1]s--force%[1]s 更新已有标签。

			必须指定标签名称。描述与颜色是可选的。
			若未提供颜色，将随机选择一个。

			标签颜色必须是 6 位十六进制值。
		`, "`"),
		Example: heredoc.Doc(`
			# 创建新的 bug 标签
			$ gh label create bug --description "Something isn't working" --color E99695
		`),
		Args: cmdutil.ExactArgs(1, "无法创建标签: 需要提供 name 参数"),
		RunE: func(c *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo
			opts.Name = args[0]
			opts.Color = strings.TrimPrefix(opts.Color, "#")
			if runF != nil {
				return runF(&opts)
			}
			return createRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Description, "description", "d", "", "标签的描述")
	cmd.Flags().StringVarP(&opts.Color, "color", "c", "", "标签的颜色")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "若标签已存在，则更新其颜色与描述")

	return cmd
}

var errLabelAlreadyExists = errors.New("标签已存在")

func createRun(opts *createOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.Color == "" {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		opts.Color = randomColors[r.Intn(len(randomColors)-1)]
	}

	opts.IO.StartProgressIndicator()
	err = createLabel(httpClient, baseRepo, opts)
	opts.IO.StopProgressIndicator()
	if err != nil {
		if errors.Is(err, errLabelAlreadyExists) {
			return fmt.Errorf("名为 %q 的标签已存在; 使用 `--force` 更新其颜色与描述", opts.Name)
		}
		return err
	}

	if opts.IO.IsStdoutTTY() {
		cs := opts.IO.ColorScheme()
		successMsg := fmt.Sprintf("%s 已在 %s 中创建标签(label) %q\n", cs.SuccessIcon(), opts.Name, ghrepo.FullName(baseRepo))
		fmt.Fprint(opts.IO.Out, successMsg)
	}

	return nil
}

func createLabel(client *http.Client, repo ghrepo.Interface, opts *createOptions) error {
	apiClient := api.NewClientFromHTTP(client)
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "labels")
	if err != nil {
		return err
	}
	requestByte, err := json.Marshal(map[string]string{
		"name":        opts.Name,
		"description": opts.Description,
		"color":       opts.Color,
	})
	if err != nil {
		return err
	}
	requestBody := bytes.NewReader(requestByte)
	err = apiClient.REST(repo.RepoHost(), "POST", path.String(), requestBody, nil)

	if httpError, ok := err.(api.HTTPError); ok && isLabelAlreadyExistsError(httpError) {
		err = errLabelAlreadyExists
	}

	if opts.Force && errors.Is(err, errLabelAlreadyExists) {
		editOpts := editOptions{
			Description: opts.Description,
			Color:       opts.Color,
			Name:        opts.Name,
		}
		return updateLabel(apiClient, repo, &editOpts)
	}

	return err
}

func updateLabel(apiClient *api.Client, repo ghrepo.Interface, opts *editOptions) error {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "labels", opts.Name)
	if err != nil {
		return err
	}
	properties := map[string]string{}
	if opts.Description != "" {
		properties["description"] = opts.Description
	}
	if opts.Color != "" {
		properties["color"] = opts.Color
	}
	if opts.NewName != "" {
		properties["new_name"] = opts.NewName
	}
	requestByte, err := json.Marshal(properties)
	if err != nil {
		return err
	}
	requestBody := bytes.NewReader(requestByte)
	err = apiClient.REST(repo.RepoHost(), "PATCH", path.String(), requestBody, nil)

	if httpError, ok := err.(api.HTTPError); ok && isLabelAlreadyExistsError(httpError) {
		err = errLabelAlreadyExists
	}

	return err
}

func isLabelAlreadyExistsError(err api.HTTPError) bool {
	return err.StatusCode == 422 && len(err.Errors) == 1 && err.Errors[0].Field == "name" && err.Errors[0].Code == "already_exists"
}
