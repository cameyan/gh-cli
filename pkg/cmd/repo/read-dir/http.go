package readdir

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/shurcooL/githubv4"
)

// Git 对象模式(mode)。高位编码对象类型(modeTypeMask)；普通文件
// 还带有权限位，因此在掩码后与 modeFile 匹配，而不是按精确值匹配。
const (
	modeTypeMask  = 0o170000
	modeDir       = 0o040000
	modeFile      = 0o100000
	modeSymlink   = 0o120000
	modeSubmodule = 0o160000
)

// repoDir 是单个树(tree)路径解析后的目录列表。
type repoDir struct {
	GitSHA  string
	ID      string
	Entries []dirEntry
}

// dirEntry 是目录列表中的单个条目。
type dirEntry struct {
	Name      string
	Path      string
	NameRaw   string
	PathRaw   string
	Type      string
	GitType   string
	Mode      int
	GitSHA    string
	Size      int
	Submodule *submodule
}

// submodule 保存 GraphQL 为子模块条目暴露的额外元数据。
type submodule struct {
	GitURL              string
	Branch              *string
	SubprojectCommitOid string
}

// isExecutable 报告该条目是否为设置了可执行位的普通文件。
func (e dirEntry) isExecutable() bool {
	return e.Type == "file" && e.Mode&0o111 != 0
}

// modeOctal 将 git 模式渲染为六位八进制字符串(例如 100644)。
func (e dirEntry) modeOctal() string {
	return fmt.Sprintf("%06o", e.Mode)
}

// ExportData 为单个条目实现 cmdutil 可导出接口。
func (e dirEntry) ExportData(fields []string) map[string]any {
	data := map[string]any{}
	for _, field := range fields {
		switch field {
		case "name":
			data[field] = e.Name
		case "path":
			data[field] = e.Path
		case "nameRaw":
			data[field] = e.NameRaw
		case "pathRaw":
			data[field] = e.PathRaw
		case "type":
			data[field] = e.Type
		case "gitType":
			data[field] = e.GitType
		case "mode":
			data[field] = e.Mode
		case "modeOctal":
			data[field] = e.modeOctal()
		case "gitSHA":
			data[field] = e.GitSHA
		case "size":
			data[field] = e.Size
		case "submodule":
			if e.Submodule == nil {
				data[field] = nil
			} else {
				data[field] = map[string]any{
					"gitUrl":              e.Submodule.GitURL,
					"branch":              e.Submodule.Branch,
					"subprojectCommitOid": e.Submodule.SubprojectCommitOid,
				}
			}
		}
	}
	return data
}

// ExportData 为目录实现 cmdutil 可导出接口。
//
// gitSHA 和 id 是结构性字段，始终存在；所请求的字段决定
// 每个条目上出现哪些属性。
func (d *repoDir) ExportData(fields []string) map[string]any {
	entries := make([]any, 0, len(d.Entries))
	for _, e := range d.Entries {
		entries = append(entries, e.ExportData(fields))
	}
	return map[string]any{
		"gitSHA":  d.GitSHA,
		"id":      d.ID,
		"entries": entries,
	}
}

// fetchTree 解析 dirPath 在 ref 上的目录列表。
//
// 它使用 repository.object(expression) 字段，该字段对目录返回 Tree，
// 对文件返回 Blob，当路径或 ref 无法解析时返回 null。
// 与 REST Contents API 不同，GraphQL 没有 1000 条目上限，并暴露 git 模式(mode)，
// 这让我们能够区分文件、目录、符号链接和子模块。
//
// null 对象表示路径或 ref 无法解析(GraphQL 无法区分这两种情况，
// 因此我们报告一个含糊的统一错误)。非 Tree 对象表示路径指向的是文件而非目录。
// 原始 ref(默认分支时为空)仅在构建错误消息时使用。
func fetchTree(httpClient *http.Client, repo ghrepo.Interface, dirPath, ref string) (*repoDir, error) {
	client := api.NewClientFromHTTP(httpClient)

	expressionRef := ref
	if expressionRef == "" {
		expressionRef = "HEAD"
	}
	expression := fmt.Sprintf("%s:%s", expressionRef, strings.TrimPrefix(dirPath, "/"))

	var query struct {
		Repository struct {
			Object *struct {
				TypeName string `graphql:"__typename"`
				Tree     struct {
					OID     string `graphql:"oid"`
					ID      string `graphql:"id"`
					Entries []struct {
						Name      string
						Path      string
						NameRaw   string
						PathRaw   string
						Type      string
						Mode      int
						OID       string `graphql:"oid"`
						Size      int
						Submodule *struct {
							GitURL              string `graphql:"gitUrl"`
							Branch              *string
							SubprojectCommitOid string
						}
					}
				} `graphql:"... on Tree"`
			} `graphql:"object(expression: $expression)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"owner":      githubv4.String(repo.RepoOwner()),
		"name":       githubv4.String(repo.RepoName()),
		"expression": githubv4.String(expression),
	}

	if err := client.Query(repo.RepoHost(), "RepoReadDir", &query, variables); err != nil {
		return nil, err
	}

	obj := query.Repository.Object
	if obj == nil {
		// 对于路径或 ref 缺失的情况，API 都返回 null 对象，因此我们
		// 无法区分哪个有误，只能根据是否提供了 ref 来推断错误消息。
		if ref != "" {
			return nil, fmt.Errorf("在 %s 的 %q 中找不到 %q(路径或 ref 可能不存在)", ghrepo.FullName(repo), ref, dirPath)
		}
		return nil, fmt.Errorf("在 %s 中找不到 %q", ghrepo.FullName(repo), dirPath)
	}

	if obj.TypeName != "Tree" {
		if obj.TypeName == "Blob" {
			return nil, fmt.Errorf("%q 是文件而非目录；请改用 `gh repo read-file`", dirPath)
		}
		return nil, fmt.Errorf("%q 不是目录", dirPath)
	}

	dir := &repoDir{
		GitSHA:  obj.Tree.OID,
		ID:      obj.Tree.ID,
		Entries: make([]dirEntry, 0, len(obj.Tree.Entries)),
	}

	for _, e := range obj.Tree.Entries {
		entry := dirEntry{
			Name:    e.Name,
			Path:    e.Path,
			NameRaw: e.NameRaw,
			PathRaw: e.PathRaw,
			Type:    entryTypeFromMode(e.Mode),
			GitType: e.Type,
			Mode:    e.Mode,
			GitSHA:  e.OID,
			Size:    e.Size,
		}
		if e.Submodule != nil {
			entry.Submodule = &submodule{
				GitURL:              e.Submodule.GitURL,
				Branch:              e.Submodule.Branch,
				SubprojectCommitOid: e.Submodule.SubprojectCommitOid,
			}
		}
		dir.Entries = append(dir.Entries, entry)
	}

	return dir, nil
}

// entryTypeFromMode 将 git 对象模式归一化为命令报告的条目类型名称:
// dir、file、symlink 或 submodule。无法识别类型的模式报告为 "unknown"。
func entryTypeFromMode(mode int) string {
	switch mode & modeTypeMask {
	case modeDir:
		return "dir"
	case modeFile:
		return "file"
	case modeSymlink:
		return "symlink"
	case modeSubmodule:
		return "submodule"
	default:
		return "unknown"
	}
}
