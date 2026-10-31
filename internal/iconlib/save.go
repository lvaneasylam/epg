// 图标写入辅助：校验（图片 magic bytes、名字/目录合法性）与落盘，
// 供管理端上传 / URL 导入共用，独立于索引与匹配逻辑。
package iconlib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxIconSize 单图标大小上限：台标远小于此，防误传大图。
const MaxIconSize = 2 << 20 // 2MB

// DetectImageType 按 magic bytes 探测图片格式，返回落盘扩展名（含点）；
// 无法识别返回空串。不信任上传方声明的 Content-Type / URL 后缀。
func DetectImageType(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return ".gif"
	}
	return ""
}

// CleanName 清洗图标名（兼目录名）：去首尾空白，拒绝路径分隔符、
// 控制字符与点开头（隐藏文件/当前目录）；不合法返回空串。
func CleanName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || strings.HasPrefix(name, ".") {
		return ""
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return name
}

// SaveIcon 校验并落盘图标，返回相对 root 的路径（斜杠分隔）与是否覆盖了已有文件。
// 目录须为 root 下已存在的一级子目录；扩展名按图片真实格式推断，
// 同目录同名文件直接覆盖。名字尾部若已带受支持的扩展名则自动剥离。
func SaveIcon(root, dir, name string, data []byte) (rel string, replaced bool, err error) {
	dir = CleanName(dir)
	name = CleanName(name)
	if dir == "" {
		return "", false, errors.New("目录名不合法")
	}
	if name == "" {
		return "", false, errors.New("图标名不合法")
	}
	if len(data) > MaxIconSize {
		return "", false, fmt.Errorf("图片超过 %dMB 上限", MaxIconSize>>20)
	}
	ext := DetectImageType(data)
	if ext == "" {
		return "", false, errors.New("不支持的图片格式（仅 png/jpg/webp/gif）")
	}
	// 用户输入常自带扩展名（如 CCTV-1.png），剥掉避免落盘成 .png.png
	if e := strings.ToLower(filepath.Ext(name)); e == ext {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	if fi, err := os.Stat(filepath.Join(root, dir)); err != nil || !fi.IsDir() {
		return "", false, errors.New("目标目录不存在")
	}
	rel = dir + "/" + name + ext
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err == nil {
		replaced = true
	}
	if err := os.WriteFile(abs, data, 0644); err != nil {
		return "", false, err
	}
	return rel, replaced, nil
}
