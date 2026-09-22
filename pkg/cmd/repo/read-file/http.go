package readfile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// repoFile 是单个路径解析后的文件内容与元数据。
type repoFile struct {
	Name        string
	Path        string
	SHA         string
	Size        int
	URL         string
	HTMLURL     string
	GitURL      string
	DownloadURL string
	Type        string
	Encoding    string
	Content     []byte
}

// ExportData 为 --json 输出实现 cmdutil 可导出接口。
func (f *repoFile) ExportData(fields []string) map[string]any {
	data := map[string]any{}
	for _, field := range fields {
		switch field {
		case "name":
			data[field] = f.Name
		case "path":
			data[field] = f.Path
		case "gitSHA":
			data[field] = f.SHA
		case "size":
			data[field] = f.Size
		case "url":
			data[field] = f.URL
		case "htmlUrl":
			data[field] = f.HTMLURL
		case "gitUrl":
			data[field] = f.GitURL
		case "downloadUrl":
			data[field] = f.DownloadURL
		case "type":
			data[field] = f.Type
		case "encoding":
			data[field] = "base64"
		case "content":
			data[field] = base64.StdEncoding.EncodeToString(f.Content)
		}
	}
	return data
}

// contentsResponse 建模单个路径的 REST Contents API 响应。
type contentsResponse struct {
	Type            string `json:"type"`
	Encoding        string `json:"encoding"`
	Size            int    `json:"size"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	Content         string `json:"content"`
	SHA             string `json:"sha"`
	URL             string `json:"url"`
	GitURL          string `json:"git_url"`
	HTMLURL         string `json:"html_url"`
	DownloadURL     string `json:"download_url"`
	Target          string `json:"target"`
	SubmoduleGitURL string `json:"submodule_git_url"`
}

// fetchContent 获取单个路径的原始 Contents API 响应。
//
// 它请求统一对象媒体类型，这样目录、文件、符号链接和子模块
// 都会以单个 JSON 对象返回，并通过 type 字段加以区分。
func fetchContent(httpClient *http.Client, repo ghrepo.Interface, filePath, ref string) (*contentsResponse, error) {
	apiPath, err := contentsAPIPath(repo, filePath, ref)
	if err != nil {
		return nil, err
	}

	// 我们使用 "application/vnd.github.object+json" 媒体类型向 Contents API 请求统一对象表示。
	// 若不这样做，API 对目录返回 JSON 数组，对文件返回 JSON 对象。
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).Request(repo.RepoHost(), http.MethodGet, apiPath.String(), nil,
		api.WithHeader("Accept", "application/vnd.github.object+json"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var content contentsResponse
	if err := json.Unmarshal(body, &content); err != nil {
		return nil, err
	}
	return &content, nil
}

// fetchFile 通过 REST Contents API 获取单个路径的元数据和内联内容。
//
// 当路径是目录、符号链接或子模块时，返回对应的类型化错误。仅当 API 内联返回内容时
// 才会填充 Content；较大的文件返回空内容，实际需要内容时由调用方通过 fetchRawFile
// 获取原始字节。
func fetchFile(httpClient *http.Client, repo ghrepo.Interface, filePath, ref string) (*repoFile, error) {
	content, err := fetchContent(httpClient, repo, filePath, ref)
	if err != nil {
		return nil, err
	}

	if content.Type != "file" {
		// 该路径解析为普通文件以外的东西。在这些消息中使用 content.Path
		// (API 清理过的路径)而不是用户输入，这样构造的路径就无法将终端
		// 转义序列走私到我们的输出中。
		switch content.Type {
		case "dir":
			return nil, fmt.Errorf("路径 %q 是目录；请改用 `gh repo read-dir`", content.Path)
		case "symlink":
			return nil, fmt.Errorf("路径 %q 是指向 %q 的符号链接，而该目标不存在", content.Path, content.Target)
		case "submodule":
			return nil, fmt.Errorf("路径 %q 是子模块(submodule)(%s 位于 %s)", content.Path, content.SubmoduleGitURL, content.SHA)
		default:
			return nil, fmt.Errorf("路径 %q 不是普通文件(类型: %s)", content.Path, content.Type)
		}
	}

	file := &repoFile{
		Name:        content.Name,
		Path:        content.Path,
		SHA:         content.SHA,
		Size:        content.Size,
		URL:         content.URL,
		HTMLURL:     content.HTMLURL,
		GitURL:      content.GitURL,
		DownloadURL: content.DownloadURL,
		Type:        content.Type,
		Encoding:    content.Encoding,
	}

	if content.Encoding == "base64" && content.Content != "" {
		decoded, err := base64.StdEncoding.DecodeString(content.Content)
		if err != nil {
			return nil, fmt.Errorf("解码 base64 文件内容失败: %w", err)
		}
		file.Content = decoded
	}

	return file, nil
}

// fetchRawFile 获取文件的原始字节，用于超过 Contents API 1 MB 内联内容限制的文件。
func fetchRawFile(httpClient *http.Client, repo ghrepo.Interface, filePath, ref string) ([]byte, error) {
	apiPath, err := contentsAPIPath(repo, filePath, ref)
	if err != nil {
		return nil, err
	}

	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 作为后续工作，考虑是否可以将 api client 注入到此调用处，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).Request(repo.RepoHost(), http.MethodGet, apiPath.String(), nil,
		api.WithHeader("Accept", "application/vnd.github.raw"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// contentsAPIPath 构建相对于主机 REST 端点的 Contents API 路径，用于路径和可选的 ref。
func contentsAPIPath(repo ghrepo.Interface, filePath, ref string) (safeurl.SafeURL, error) {
	// Contents API 接受完全百分号编码的路径，包括编码为 %2F 的路径分隔符，
	// 因此空格和其他特殊字符会被透明地处理。
	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "contents", strings.TrimPrefix(filePath, "/"))
	if err != nil {
		return nil, err
	}
	if ref != "" {
		u.SetQuery("ref", ref)
	}
	return u, nil
}
