package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/internal/ghinstance"
)

func httpRequest(client *http.Client, hostname string, apiHost string, method string, p string, params any, headers []string) (*http.Response, error) {
	isGraphQL := p == "graphql"
	var requestURL string
	if strings.Contains(p, "://") {
		// 绝对 URL 原样使用; api_host 永远不会作用于它们。
		requestURL = p
	} else if isGraphQL {
		// 首先我们确定规范主机对应的 GQL 端点, 这取决于主机类型
		// 例如 github.com 的端点为 https://api.github.com/graphql, 而 GHES 的 myghes.com 端点为 https://myghes.com/api/graphql。
		requestURL = ghinstance.GraphQLEndpoint(hostname)
		if apiHost != "" {
			requestURL = swapURLHost(requestURL, apiHost)
		}
	} else {
		// 注意 gh api 命令从用户输入中原样获取路径, 因此我们
		// 刻意不将其路由经过 safeurl, 也不在此处对其进行转义。
		requestURL = ghinstance.RESTPrefix(hostname) + strings.TrimPrefix(p, "/")
		if apiHost != "" {
			requestURL = swapURLHost(requestURL, apiHost)
		}
	}

	var body io.Reader
	var bodyIsJSON bool

	switch pp := params.(type) {
	case map[string]any:
		if strings.EqualFold(method, "GET") {
			requestURL = addQuery(requestURL, pp)
		} else {
			if isGraphQL {
				pp = groupGraphQLVariables(pp)
			}
			b, err := json.Marshal(pp)
			if err != nil {
				return nil, fmt.Errorf("序列化参数时出错: %w", err)
			}
			body = bytes.NewBuffer(b)
			bodyIsJSON = true
		}
	case io.Reader:
		body = pp
	case nil:
		body = nil
	default:
		return nil, fmt.Errorf("无法识别的参数类型: %v", params)
	}

	req, err := http.NewRequest(strings.ToUpper(method), requestURL, body)
	if err != nil {
		return nil, err
	}

	for _, h := range headers {
		idx := strings.IndexRune(h, ':')
		if idx == -1 {
			return nil, fmt.Errorf("请求头 %q 需要以 ':' 分隔的值", h)
		}
		name, value := h[0:idx], strings.TrimSpace(h[idx+1:])
		if strings.EqualFold(name, "Content-Length") {
			length, err := strconv.ParseInt(value, 10, 0)
			if err != nil {
				return nil, err
			}
			req.ContentLength = length
		} else {
			req.Header.Add(name, value)
		}
	}
	if bodyIsJSON && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}

	return client.Do(req)
}

func groupGraphQLVariables(params map[string]any) map[string]any {
	topLevel := make(map[string]any)
	variables := make(map[string]any)

	for key, val := range params {
		switch key {
		case "query", "operationName":
			topLevel[key] = val
		default:
			variables[key] = val
		}
	}

	if len(variables) > 0 {
		topLevel["variables"] = variables
	}
	return topLevel
}

func addQuery(path string, params map[string]any) string {
	if len(params) == 0 {
		return path
	}

	query := url.Values{}
	if err := addQueryParam(query, "", params); err != nil {
		panic(err)
	}

	sep := "?"
	if strings.ContainsRune(path, '?') {
		sep = "&"
	}
	return path + sep + query.Encode()
}

func addQueryParam(query url.Values, key string, value any) error {
	switch v := value.(type) {
	case string:
		query.Add(key, v)
	case []byte:
		query.Add(key, string(v))
	case nil:
		query.Add(key, "")
	case int:
		query.Add(key, fmt.Sprintf("%d", v))
	case bool:
		query.Add(key, fmt.Sprintf("%v", v))
	case map[string]any:
		for subkey, value := range v {
			// 如有必要, 可在此处添加对嵌套子键的支持
			if err := addQueryParam(query, subkey, value); err != nil {
				return err
			}
		}
	case []any:
		for _, entry := range v {
			if err := addQueryParam(query, key+"[]", entry); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("未知类型 %v", v)
	}
	return nil
}

// swapURLHost 将 rawURL 的 host 部分替换为 newHost, 同时保持
// scheme、端口(如 rawURL 中已存在)、path 和 query 不变。
func swapURLHost(rawURL, newHost string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// rawURL 始终是由 ghinstance.GraphQLEndpoint 或
		// ghinstance.RESTPrefix 构建的结构合法的 URL, 因此该错误在实践中不可达。若它
		// 确实发生了, 原样返回输入会使请求仍指向
		// 原始主机, 这是安全的(不替换主机, 但请求仍会成功)。
		return rawURL
	}
	u.Host = newHost
	return u.String()
}
