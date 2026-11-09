package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"rua.plus/saber/internal/config"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func shellCommand(argv []string) string {
	a := make([]string, len(argv))
	for i, v := range argv {
		a[i] = shellQuote(v)
	}
	return strings.Join(a, " ")
}
func (s *Service) command(ctx context.Context, p config.MediaProviderConfig, payload map[string]any) (map[string]any, error) {
	if p.Provider == "plugin" && s.host.Call == nil {
		return nil, errors.New("media plugin host unavailable")
	}
	if p.Provider == "plugin" {
		result, err := s.host.Call(ctx, p.PluginTool, payload)
		if err != nil {
			return nil, err
		}
		return decodeObject(result)
	}
	if len(p.Command) == 0 {
		return nil, errors.New("media command not configured")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return s.runProcessJSON(ctx, p.Command, string(b)+"\n")
}

func (s *Service) runRawJSONCommand(ctx context.Context, command, input string) (map[string]any, error) {
	return s.runProcessJSON(ctx, []string{"sh", "-c", command}, input)
}

func (s *Service) runProcessJSON(ctx context.Context, argv []string, input string) (response map[string]any, resultErr error) {
	if s.host.StartProcess == nil || s.host.Check == nil {
		return nil, errors.New("media duplex execution backend unavailable")
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return nil, err
	}
	process, err := s.host.StartProcess(ctx, argv, nil)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, process.Close()) }()
	stop := context.AfterFunc(ctx, func() { _ = process.Close() })
	defer stop()
	type readResult struct {
		data []byte
		err  error
	}
	readDone := make(chan readResult, 1)
	// 先读 stdout，再写 stdin，避免模型同时输出大量结果时的双向管道死锁。
	go func() {
		data, err := boundedRead(process, s.cfg.MaxBytes)
		if err != nil {
			err = errors.Join(err, process.Close())
		}
		readDone <- readResult{data, err}
	}()
	_, writeErr := io.Copy(process, io.LimitReader(strings.NewReader(input), int64(len(input))))
	closeErr := process.CloseWrite()
	if writeErr != nil {
		_ = process.Close()
	}
	waitErr := process.Wait()
	output := <-readDone
	if waitErr == nil {
		closeErr = nil
	} // 正常完成的一次性命令可能先于 CloseWrite 关闭自己的 stdin。
	if err = errors.Join(writeErr, closeErr, waitErr, output.err, ctx.Err()); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(output.data, &response); err != nil {
		return nil, fmt.Errorf("media command must return JSON on stdout: %w", err)
	}
	if e, ok := response["error"]; ok && e != nil {
		return nil, fmt.Errorf("media command: %v", e)
	}
	if success, ok := response["success"].(bool); ok && !success {
		return nil, errors.New("media command reported failure")
	}
	return response, nil
}
func decodeObject(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	if err == nil {
		if d := object(m, "data"); d != nil && len(m) == 1 {
			return d, nil
		}
	}
	return m, err
}
