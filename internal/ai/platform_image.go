package ai

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readWorkspaceImage 钉住任务根目录，拒绝绝对路径与越界链接，限制真实读取大小。
func readWorkspaceImage(workDir, path string, limit int) (data []byte, mimeType, filename string, err error) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return nil, "", "", errors.New("图片路径必须是工作区内的非空相对路径")
	}
	path = filepath.Clean(path)
	if path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator)) {
		return nil, "", "", errors.New("图片路径超出任务工作目录")
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, "", "", fmt.Errorf("打开任务工作目录失败: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Stat(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("读取图片失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", "", errors.New("图片必须是普通文件")
	}
	if info.Size() > int64(limit) {
		return nil, "", "", fmt.Errorf("图片超过 %d 字节上限", limit)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("读取图片失败: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err = io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("读取图片失败: %w", err)
	}
	mimeType, err = platformImageMIME(data, limit)
	if err != nil {
		return nil, "", "", err
	}
	return data, mimeType, filepath.Base(path), nil
}
