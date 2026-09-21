package bot

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"rua.plus/saber/internal/cli"
)

// runTerminal 只装配平台无关服务，EOF、退出命令和信号均正常结束进程。
func (s *appState) runTerminal() error {
	if !s.cfg.AI.Enabled {
		_, err := fmt.Fprintln(os.Stdout, "Matrix 未启用。终端对话需要在 config.yaml 中设置 ai.enabled: true 并配置 ai.providers、ai.default_model；接入 Matrix 请设置 matrix.enabled: true。")
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s.services = &services{}
	defer s.shutdown(stop)
	if err := s.initServices(); err != nil {
		return err
	}
	return cli.RunTerminal(ctx, os.Stdin, os.Stdout, s.services.aiService.HandleChat, s.cfg.AI.DefaultModel)
}
