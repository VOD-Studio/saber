package execution

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// artifactPrefixLen 是产物文件名中随机前缀加分隔符的长度；Artifacts 依赖它还原展示名。
const artifactPrefixLen = 33

// ArtifactDir 返回任务私有产物目录。
//
// 交付文件与轨迹导出共用这一路径，任何新产物都必须写在同一目录，
// 不能按功能另建归档根，否则交付与清理会各看一半。
func (e *Executor) ArtifactDir(taskID int64) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "artifacts")
}

// WriteArtifact 以私有权限原子写入任务产物并返回落盘路径。
//
// 文件名固定为 <32hex>-<展示名>：Artifacts 按前 33 字节剥离随机前缀，
// 交付端按同一规则把展示名发给用户；调用方只能提供展示名，不能指定宿主路径。
//
// Errors:
//   - taskID 非正数或展示名含路径分隔符时拒绝写入
//   - 目录无法创建或其权限无法收紧时返回原始错误
func (e *Executor) WriteArtifact(taskID int64, name string, data []byte) (string, error) {
	if taskID <= 0 {
		return "", errors.New("artifact requires a persistent task")
	}
	if name == "" || filepath.Base(name) != name || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid artifact name %q", name)
	}
	taskDir := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10))
	dir := e.ArtifactDir(taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hex.EncodeToString(random[:])+"-"+name)
	temp, err := os.CreateTemp(taskDir, ".artifact-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	if err = os.Link(temp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
