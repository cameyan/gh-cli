package verification

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cenkalti/backoff/v5"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/cli/go-gh/v2/pkg/config"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

//go:embed embed/tuf-repo.github.com/root.json
var githubRoot []byte

const GitHubTUFMirror = "https://tuf-repo.github.com"

func DefaultOptionsWithCacheSetting(tufMetadataDir o.Option[string], hc *http.Client) *tuf.Options {
	opts := tuf.DefaultOptions()

	// 在 Codespaces 工作区中，CODESPACES 环境变量将被设置为 true
	if os.Getenv("CODESPACES") == "true" {
		// 如果该工具在 Codespace 中使用，则禁用本地缓存，
		// 因为存在权限问题导致 tuf 库无法将 Sigstore 缓存写入主目录
		opts.DisableLocalCache = true
	}

	// 将缓存路径设置为提供的目录，或 CLI 所属的目录
	opts.CachePath = tufMetadataDir.UnwrapOr(filepath.Join(config.CacheDir(), ".sigstore", "root"))

	// 允许 TUF 缓存保留 1 天
	opts.CacheValidity = 1

	// 配置 fetcher 的超时与重试
	f := fetcher.NewDefaultFetcher()
	f.SetHTTPClient(hc)
	retryOptions := []backoff.RetryOption{backoff.WithMaxTries(3)}
	f.SetRetryOptions(retryOptions...)
	opts.WithFetcher(f)

	return opts
}

func GitHubTUFOptions(tufMetadataDir o.Option[string], hc *http.Client) *tuf.Options {
	opts := DefaultOptionsWithCacheSetting(tufMetadataDir, hc)

	opts.Root = githubRoot
	opts.RepositoryBaseURL = GitHubTUFMirror

	return opts
}
