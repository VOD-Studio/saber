package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Token 读取本机令牌；服务端首次启动时以 0600 权限创建，客户端只读。
func Token(path string, create bool) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			return Token(path, false)
		}
		if err != nil {
			return "", err
		}
		token := hex.EncodeToString(secret)
		_, writeErr := f.WriteString(token + "\n")
		return token, errors.Join(writeErr, f.Close())
	}
	if err != nil {
		return "", fmt.Errorf("读取服务令牌失败，请先运行 saber serve: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", errors.New("服务令牌至少需要 32 个字符")
	}
	return token, nil
}
