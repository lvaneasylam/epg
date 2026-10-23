package syncer

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// fetchToFile 下载 EPG 源到本地临时文件，带超时与体积上限保护。
// 返回写入的字节数。
func fetchToFile(ctx context.Context, url string, timeout time.Duration, maxBytes int64, dst string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "goepg/1.0 (+xmltv-fetcher)")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP 状态码 %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	// maxBytes+1：多读 1 字节即可识别超限
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	cerr := f.Close()
	if err != nil {
		return n, err
	}
	if cerr != nil {
		return n, cerr
	}
	if n > maxBytes {
		return n, fmt.Errorf("文件超过上限 %d 字节", maxBytes)
	}
	return n, nil
}

// openMaybeGzip 打开本地 EPG 文件，gzip 魔数（0x1f 0x8b）自动识别解压，
// 与文件扩展名无关。
func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 2)
	n, _ := io.ReadFull(f, head)
	if n < 2 || head[0] != 0x1f || head[1] != 0x8b {
		// 非 gzip：回退文件开头
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
		return f, nil
	}
	zr, err := gzip.NewReader(io.MultiReader(bytes.NewReader(head), f))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("gzip 解压失败: %w", err)
	}
	return &gzCloser{zr: zr, f: f}, nil
}

// gzCloser 保证底层文件随 gzip 流一并关闭。
type gzCloser struct {
	zr *gzip.Reader
	f  *os.File
}

func (g *gzCloser) Read(p []byte) (int, error) { return g.zr.Read(p) }
func (g *gzCloser) Close() error {
	zerr := g.zr.Close()
	ferr := g.f.Close()
	if zerr != nil {
		return zerr
	}
	return ferr
}
