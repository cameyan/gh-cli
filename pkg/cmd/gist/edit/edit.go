package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/gist/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
	"github.com/spf13/cobra"
)

var editNextOptions = []string{"编辑另一个文件", "提交", "取消"}

type EditOptions struct {
	IO         *iostreams.IOStreams
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	Prompter   prompter.Prompter

	Edit func(string, string, string, *iostreams.IOStreams) (string, error)

	Selector       string
	EditFilename   string
	AddFilename    string
	RemoveFilename string
	SourceFile     string
	Description    string
}

func NewCmdEdit(f *cmdutil.Factory, runF func(*EditOptions) error) *cobra.Command {
	opts := EditOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
		Config:     f.Config,
		Prompter:   f.Prompter,
		Edit: func(editorCmd, filename, defaultContent string, io *iostreams.IOStreams) (string, error) {
			return surveyext.Edit(
				editorCmd,
				"*."+filename,
				defaultContent,
				io.In, io.Out, io.ErrOut)
		},
	}

	cmd := &cobra.Command{
		Use:   "edit {<id> | <url>} [<filename>]",
		Short: "编辑你的某个 gist",
		Example: heredoc.Doc(`
			# 交互式选择要编辑的 gist
			$ gh gist edit

			# 在默认编辑器中编辑 gist 文件
			$ gh gist edit 1234567890abcdef1234567890abcdef

			# 编辑 gist 中的特定文件
			$ gh gist edit 1234567890abcdef1234567890abcdef --filename hello.py

			# 用本地文件的内容替换 gist 文件
			$ gh gist edit 1234567890abcdef1234567890abcdef --filename hello.py hello.py

			# 向 gist 添加新文件
			$ gh gist edit 1234567890abcdef1234567890abcdef --add newfile.py

			# 修改 gist 的描述
			$ gh gist edit 1234567890abcdef1234567890abcdef --desc "新描述"

			# 从 gist 中移除文件
			$ gh gist edit 1234567890abcdef1234567890abcdef --remove hello.py
		`),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return cmdutil.FlagErrorf("参数过多")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.Selector = args[0]
			}
			if len(args) > 1 {
				opts.SourceFile = args[1]
			}

			if runF != nil {
				return runF(&opts)
			}

			return editRun(&opts)
		},
	}

	cmd.Flags().StringVarP(&opts.AddFilename, "add", "a", "", "向 gist 添加新文件")
	cmd.Flags().StringVarP(&opts.Description, "desc", "d", "", "gist 的新描述")
	cmd.Flags().StringVarP(&opts.EditFilename, "filename", "f", "", "选择要编辑的文件")
	cmd.Flags().StringVarP(&opts.RemoveFilename, "remove", "r", "", "从 gist 中移除文件")

	cmd.MarkFlagsMutuallyExclusive("add", "remove")
	cmd.MarkFlagsMutuallyExclusive("remove", "filename")

	return cmd
}

func editRun(opts *EditOptions) error {
	client, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	gistID := opts.Selector
	if gistID == "" {
		cs := opts.IO.ColorScheme()
		if gistID == "" {
			if !opts.IO.CanPrompt() {
				return cmdutil.FlagErrorf("非交互式运行时必须指定 gist ID 或 URL")
			}

			gist, err := shared.PromptGists(opts.Prompter, client, host, cs)
			if err != nil {
				return err
			}

			if gist.ID == "" {
				fmt.Fprintln(opts.IO.Out, "未找到 gist。")
				return nil
			}
			gistID = gist.ID
		}
	}

	if strings.Contains(gistID, "/") {
		id, err := shared.GistIDFromURL(gistID)
		if err != nil {
			return err
		}
		gistID = id
	}

	apiClient := api.NewClientFromHTTP(client)

	gist, err := shared.GetGist(client, host, gistID)
	if err != nil {
		if errors.Is(err, shared.NotFoundErr) {
			return fmt.Errorf("gist 未找到: %s", gistID)
		}
		return err
	}

	username, err := api.CurrentLoginName(apiClient, host)
	if err != nil {
		return err
	}

	if username != gist.Owner.Login {
		return errors.New("你不拥有此 gist")
	}

	// 将我们的 gist 转换为更新端点所期望的 schema
	filesToupdate := make(map[string]*gistFileToUpdate, len(gist.Files))
	for filename, file := range gist.Files {
		filesToupdate[filename] = &gistFileToUpdate{
			Content:     file.Content,
			NewFilename: file.Filename,
		}
	}

	gistToUpdate := gistToUpdate{
		id:          gist.ID,
		Description: gist.Description,
		Files:       filesToupdate,
	}

	shouldUpdate := false
	if opts.Description != "" {
		shouldUpdate = true
		gistToUpdate.Description = opts.Description
	}

	if opts.AddFilename != "" {
		var input io.Reader
		switch src := opts.SourceFile; {
		case src == "-":
			input = opts.IO.In
		case src != "":
			f, err := os.Open(src)
			if err != nil {
				return err
			}
			defer func() {
				_ = f.Close()
			}()
			input = f
		default:
			f, err := os.Open(opts.AddFilename)
			if err != nil {
				return err
			}
			defer func() {
				_ = f.Close()
			}()
			input = f
		}

		content, err := io.ReadAll(input)
		if err != nil {
			return fmt.Errorf("读取内容: %w", err)
		}

		files, err := getFilesToAdd(opts.AddFilename, content)
		if err != nil {
			return err
		}

		gistToUpdate.Files = files
		return updateGist(apiClient, host, gistToUpdate)
	}

	// 从 gist 中移除文件
	if opts.RemoveFilename != "" {
		files, err := getFilesToRemove(gistToUpdate, opts.RemoveFilename)
		if err != nil {
			return err
		}

		gistToUpdate.Files = files
		return updateGist(apiClient, host, gistToUpdate)
	}

	filesToUpdate := map[string]string{}

	for {
		filename := opts.EditFilename
		candidates := []string{}
		for filename := range gistToUpdate.Files {
			candidates = append(candidates, filename)
		}

		sort.Strings(candidates)

		if filename == "" {
			if len(candidates) == 1 {
				filename = candidates[0]
			} else if len(candidates) == 0 {
				return errors.New("gist 中没有文件")
			} else {
				if !opts.IO.CanPrompt() {
					return errors.New("无法确定要编辑哪个文件; 请指定 --filename 或交互式运行")
				}
				result, err := opts.Prompter.Select("编辑哪个文件?", "", candidates)
				if err != nil {
					return fmt.Errorf("无法提示: %w", err)
				}
				filename = candidates[result]
			}
		}

		gistFile, found := gistToUpdate.Files[filename]
		if !found {
			return fmt.Errorf("gist 中没有文件 %q", filename)
		}
		if shared.IsBinaryContents([]byte(gistFile.Content)) {
			return fmt.Errorf("不支持编辑二进制文件")
		}

		// 如果文件被截断，则获取完整内容
		// 但仅当它尚未在本次会话中被编辑过
		file := gist.Files[filename]
		if file.Truncated {
			if _, alreadyEdited := filesToUpdate[filename]; !alreadyEdited {
				fullContent, err := shared.GetRawGistFile(client, host, safeurl.NewImmutableSafeURL(file.RawURL))
				if err != nil {
					return err
				}

				// 往返路径: 内容会在编辑器中打开并发送
				// 回 API，因此原始字节必须原样保留。
				gistFile.Content = fullContent.Raw()
			}
		}

		var text string
		if src := opts.SourceFile; src != "" {
			if src == "-" {
				data, err := io.ReadAll(opts.IO.In)
				if err != nil {
					return fmt.Errorf("从 stdin 读取: %w", err)
				}
				text = string(data)
			} else {
				data, err := os.ReadFile(src)
				if err != nil {
					return fmt.Errorf("读取 %s: %w", src, err)
				}
				text = string(data)
			}
		} else {
			editorCommand, err := cmdutil.DetermineEditor(opts.Config)
			if err != nil {
				return err
			}

			data, err := opts.Edit(editorCommand, filename, gistFile.Content, opts.IO)
			if err != nil {
				return err
			}
			text = data
		}

		if text != gistFile.Content {
			gistFile.Content = text // 这样在他们重新编辑时也能看到
			filesToUpdate[filename] = text
		}

		if !opts.IO.CanPrompt() ||
			len(candidates) == 1 ||
			opts.EditFilename != "" {
			break
		}

		choice := ""
		result, err := opts.Prompter.Select("接下来做什么?", "", editNextOptions)
		if err != nil {
			return fmt.Errorf("无法提示: %w", err)
		}
		choice = editNextOptions[result]

		stop := false

		switch choice {
		case "编辑另一个文件":
			continue
		case "提交":
			stop = true
		case "取消":
			return cmdutil.CancelError
		}

		if stop {
			break
		}
	}

	if len(filesToUpdate) > 0 {
		shouldUpdate = true
	}

	if !shouldUpdate {
		return nil
	}

	updatedFiles := make(map[string]*gistFileToUpdate, len(filesToUpdate))
	for filename := range filesToUpdate {
		updatedFiles[filename] = gistToUpdate.Files[filename]
	}
	gistToUpdate.Files = updatedFiles

	return updateGist(apiClient, host, gistToUpdate)
}

// https://docs.github.com/en/rest/gists/gists?apiVersion=2022-11-28#update-a-gist
type gistToUpdate struct {
	// 要更新的 gist 的 id。不会被序列化为 JSON。
	id string
	// gist 的描述
	Description string `json:"description"`
	// 要更新、重命名或删除的 gist 文件。键必须与要更改的文件的
	// 当前文件名匹配。要删除文件，请将值设为 nil
	Files map[string]*gistFileToUpdate `json:"files"`
}

type gistFileToUpdate struct {
	// 文件的新内容
	Content string `json:"content"`
	// 文件的新名称
	NewFilename string `json:"filename,omitempty"`
}

func updateGist(apiClient *api.Client, hostname string, gist gistToUpdate) error {
	requestByte, err := json.Marshal(gist)
	if err != nil {
		return err
	}

	requestBody := bytes.NewReader(requestByte)
	result := shared.Gist{}

	path, err := safeurl.JoinPath("gists", gist.id)
	if err != nil {
		return err
	}
	err = apiClient.REST(hostname, "POST", path.String(), requestBody, &result)
	if err != nil {
		return err
	}

	return nil
}

func getFilesToAdd(file string, content []byte) (map[string]*gistFileToUpdate, error) {
	if shared.IsBinaryContents(content) {
		return nil, fmt.Errorf("上传 %s 失败: 不支持二进制文件", file)
	}

	if len(content) == 0 {
		return nil, errors.New("文件内容不能为空")
	}

	filename := filepath.Base(file)
	return map[string]*gistFileToUpdate{
		filename: {
			NewFilename: filename,
			Content:     string(content),
		},
	}, nil
}

func getFilesToRemove(gist gistToUpdate, filename string) (map[string]*gistFileToUpdate, error) {
	if _, found := gist.Files[filename]; !found {
		return nil, fmt.Errorf("gist 中没有文件 %q", filename)
	}

	gist.Files[filename] = nil
	return map[string]*gistFileToUpdate{
		filename: nil,
	}, nil
}
