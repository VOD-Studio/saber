// Package setup 提供明确授权的本机安装、配置与升级操作。
package setup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/server"
)

// Command 执行 init、set、doctor、install、upgrade、rollback；安装必须提供 HTTPS 下载地址与校验值。
func Command(ctx context.Context, args []string, configPath string) error {
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	if len(args) == 0 {
		return fmt.Errorf("setup init|set <yaml.key> <yaml.value>|doctor <endpoint> <token-file>|install <https-url> <sha256> <destination>|upgrade ...|rollback <destination>")
	}
	switch args[0] {
	case "init":
		if _, err := os.Stat(configPath); err == nil {
			return fmt.Errorf("配置已存在，不覆盖: %s", configPath)
		} else if !os.IsNotExist(err) {
			return err
		}
		cfg := config.DefaultConfig()
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		return writePrivate(configPath, data, false)
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("setup set <yaml.key> <yaml.value>")
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var root map[string]any
		if err = yaml.Unmarshal(data, &root); err != nil {
			return err
		}
		var value any
		if err = yaml.Unmarshal([]byte(args[2]), &value); err != nil {
			return err
		}
		keys := strings.Split(args[1], ".")
		node := root
		for _, k := range keys[:len(keys)-1] {
			if k == "" {
				return fmt.Errorf("空配置字段")
			}
			next, ok := node[k].(map[string]any)
			if !ok {
				if node[k] != nil {
					return fmt.Errorf("%s 不是配置对象", k)
				}
				next = map[string]any{}
				node[k] = next
			}
			node = next
		}
		node[keys[len(keys)-1]] = value
		data, err = yaml.Marshal(root)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(configPath), ".config-validate-*")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer func() { _ = os.Remove(name) }()
		if _, err = tmp.Write(data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err = tmp.Close(); err != nil {
			return err
		}
		if _, err = config.Load(name); err != nil {
			return fmt.Errorf("配置验证失败（原配置不变）: %w", err)
		}
		return writePrivate(configPath, data, true)
	case "doctor":
		if len(args) != 3 {
			return fmt.Errorf("setup doctor <loopback-endpoint> <token-file>")
		}
		info, err := os.Stat(args[2])
		if err != nil {
			return err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("令牌文件需要 0600 权限")
		}
		token, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		client, err := server.NewClient(args[1], strings.TrimSpace(string(token)))
		if err != nil {
			return err
		}
		result, err := client.Command(ctx, "setup", fmt.Sprintf("doctor-%d", time.Now().UnixNano()), "/doctor")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, result)
		return err
	case "install", "upgrade":
		if len(args) != 4 {
			return fmt.Errorf("setup %s <https-url> <sha256> <destination>", args[0])
		}
		return Install(ctx, args[1], args[2], args[3], args[0] == "upgrade")
	case "rollback":
		if len(args) != 2 {
			return fmt.Errorf("setup rollback <destination>")
		}
		dest := args[1]
		if !filepath.IsAbs(dest) {
			return fmt.Errorf("安装路径必须是绝对路径")
		}
		backup := dest + ".previous"
		if _, err := os.Stat(backup); err != nil {
			return err
		}
		if err := os.Rename(dest, dest+".failed"); err != nil {
			return err
		}
		if err := os.Rename(backup, dest); err != nil {
			_ = os.Rename(dest+".failed", dest)
			return err
		}
		return nil
	default:
		return fmt.Errorf("未知 setup 操作: %s", args[0])
	}
}

func writePrivate(path string, data []byte, backup bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝写入符号链接")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".saber-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if backup {
		old, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if e = writePrivate(path+".previous", old, false); e != nil {
			return e
		}
	}
	return os.Rename(tmp.Name(), path)
}

// Install 验证完整下载与执行版本后原子替换程序，保存可恢复的旧二进制。
func Install(ctx context.Context, source, digest, destination string, upgrade bool) error {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("安装源必须是无凭据 HTTPS URL")
	}
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size {
		return fmt.Errorf("必须提供可信来源的 SHA256")
	}
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("安装路径必须是绝对路径")
	}
	if st, e := os.Lstat(destination); e == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("安装目标不能是符号链接")
		}
		if !upgrade {
			return fmt.Errorf("目标已存在；请使用 upgrade")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || r.URL.Scheme != "https" || r.URL.User != nil {
			return fmt.Errorf("拒绝不安全重定向")
		}
		return nil
	}}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return fmt.Errorf("下载 HTTP %d", res.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".saber-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(res.Body, 256*1024*1024+1))
	if err == nil && n > 256*1024*1024 {
		err = fmt.Errorf("二进制超过 256 MiB")
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(digest) {
		err = fmt.Errorf("SHA256 校验失败")
	}
	if err == nil {
		err = tmp.Chmod(0700)
	}
	if err == nil {
		err = tmp.Sync()
	}
	ce := tmp.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	probe := exec.CommandContext(ctx, tmp.Name(), "-version")
	probe.Stdout = os.Stdout
	probe.Stderr = os.Stderr
	if err = probe.Run(); err != nil {
		return fmt.Errorf("安装产物不能在当前系统运行: %w", err)
	}
	if upgrade {
		if err = os.Rename(destination, destination+".previous"); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp.Name(), destination); err != nil {
		if upgrade {
			_ = os.Rename(destination+".previous", destination)
		}
		return err
	}
	return nil
}

// Wizard 从交互终端创建配置并保留用户明确输入，不把密钥打印到输出。
func Wizard(ctx context.Context, path string, input io.Reader, output io.Writer) error {
	if err := Command(ctx, []string{"init"}, path); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, "配置已创建；输入监听地址（留空保持本机 127.0.0.1:8320）：")
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() {
		return scanner.Err()
	}
	value := strings.TrimSpace(scanner.Text())
	if value != "" {
		return Command(ctx, []string{"set", "server.listen", value}, path)
	}
	return nil
}
