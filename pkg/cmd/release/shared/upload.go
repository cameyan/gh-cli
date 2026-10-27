package shared

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"golang.org/x/sync/errgroup"
)

type errNetwork struct{ error }

type AssetForUpload struct {
	Name  string
	Label string

	Size     int64
	MIMEType string
	Open     func() (io.ReadCloser, error)

	ExistingURL safeurl.SafeURL
}

func AssetsFromArgs(args []string) (assets []*AssetForUpload, err error) {
	labeledArgs, unlabeledArgs := cmdutil.Partition(args, func(arg string) bool {
		return strings.Contains(arg, "#")
	})

	args, err = cmdutil.GlobPaths(unlabeledArgs)
	if err != nil {
		return nil, err
	}

	args = append(args, labeledArgs...)

	for _, arg := range args {
		var label string
		fn := arg
		if idx := strings.IndexRune(arg, '#'); idx > 0 {
			fn = arg[0:idx]
			label = arg[idx+1:]
		}

		var fi os.FileInfo
		fi, err = os.Stat(fn)
		if err != nil {
			return
		}

		assets = append(assets, &AssetForUpload{
			Open: func() (io.ReadCloser, error) {
				return os.Open(fn)
			},
			Size:     fi.Size(),
			Name:     fi.Name(),
			Label:    label,
			MIMEType: typeForFilename(fi.Name()),
		})
	}
	return
}

func typeForFilename(fn string) string {
	ext := fileExt(fn)
	switch ext {
	case ".zip":
		return "application/zip"
	case ".js":
		return "application/javascript"
	case ".tar":
		return "application/x-tar"
	case ".tgz", ".tar.gz":
		return "application/x-gtar"
	case ".bz2":
		return "application/x-bzip2"
	case ".dmg":
		return "application/x-apple-diskimage"
	case ".rpm":
		return "application/x-rpm"
	case ".deb":
		return "application/x-debian-package"
	}

	t := mime.TypeByExtension(ext)
	if t == "" {
		return "application/octet-stream"
	}
	return t
}

func fileExt(fn string) string {
	fn = strings.ToLower(fn)
	if strings.HasSuffix(fn, ".tar.gz") {
		return ".tar.gz"
	}
	return path.Ext(fn)
}

func ConcurrentUpload(httpClient *http.Client, hostname string, uploadURL safeurl.SafeURL, numWorkers int, assets []*AssetForUpload) error {
	if numWorkers == 0 {
		return errors.New("并发 worker 数量必须大于 0")
	}

	ctx := context.Background()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(numWorkers)

	for _, a := range assets {
		asset := *a
		g.Go(func() error {
			return uploadWithDelete(gctx, httpClient, hostname, uploadURL, asset)
		})
	}

	return g.Wait()
}

func shouldRetry(err error) bool {
	if _, ok := errors.AsType[errNetwork](err); ok {
		return true
	}
	var httpError api.HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode >= 500
}

// 允许在测试中注入退避(backoff)间隔。
var retryInterval = time.Millisecond * 200

func uploadWithDelete(ctx context.Context, httpClient *http.Client, hostname string, uploadURL safeurl.SafeURL, a AssetForUpload) error {
	if a.ExistingURL != nil && a.ExistingURL.String() != "" {
		if err := deleteAsset(ctx, httpClient, hostname, a.ExistingURL); err != nil {
			return err
		}
	}
	bo := backoff.NewConstantBackOff(retryInterval)
	return backoff.Retry(func() error {
		_, err := uploadAsset(ctx, httpClient, uploadURL, a)
		if err == nil || shouldRetry(err) {
			return err
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(backoff.WithMaxRetries(bo, 3), ctx))
}

func uploadAsset(ctx context.Context, httpClient *http.Client, uploadURL safeurl.SafeURL, asset AssetForUpload) (*ReleaseAsset, error) {
	u, err := url.Parse(uploadURL.String())
	if err != nil {
		return nil, err
	}
	params := u.Query()
	params.Set("name", asset.Name)
	params.Set("label", asset.Label)
	u.RawQuery = params.Encode()

	// 由于 u 派生自 uploadURL(已经是受信任的 safeurl.SafeURL)，因此可以安全地将生成的 URL 声明为 safeURL。
	safeURL := safeurl.NewImmutableSafeURL(u.String())

	f, err := asset.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", safeURL.String(), f)
	if err != nil {
		return nil, err
	}
	req.ContentLength = asset.Size
	req.Header.Set("Content-Type", asset.MIMEType)
	req.GetBody = asset.Open

	// 使用 DoRequest 而非 Request，因为 ContentLength 和 GetBody 是直接设置在请求上的，
	// 无法用请求头表达。上传 URL 由 API 提供，指向 uploads 主机，因此这里无需进行端点解析。
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).DoRequest(req)
	if err != nil {
		// 只有传输层失败才可作为网络错误重试。已到达且携带失败状态码的响应原样上报，
		// 交由 shouldRetry 根据状态码判断。
		if _, ok := errors.AsType[api.HTTPError](err); ok {
			return nil, err
		}
		return nil, errNetwork{err}
	}
	defer resp.Body.Close()

	var newAsset ReleaseAsset
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&newAsset); err != nil {
		return nil, err
	}

	return &newAsset, nil
}

func deleteAsset(ctx context.Context, httpClient *http.Client, hostname string, assetURL safeurl.SafeURL) error {
	// 资产(asset) URL 由 API 提供，因此它是绝对地址，并按原样请求。
	// TODO(api-client-rollout)
	// 这行代码是 api client 机械化推广的一部分。
	// 后续可以考虑将 api client 注入到该调用点，而不是在此构造
	resp, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, hostname, http.MethodDelete, assetURL.String(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
