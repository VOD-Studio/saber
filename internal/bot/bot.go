// Package bot 封装所有机器人初始化和运行逻辑。
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
	"rua.plus/saber/internal/meme"
)

// services 持有所有需要管理的服务实例。
type services struct {
	aiService        *ai.Service
	mcpManager       *mcp.Manager
	proactiveManager *ai.ProactiveManager
	commandService   *matrix.CommandService
	eventHandler     *matrix.EventHandler
	presence         *matrix.PresenceService
	mediaService     *matrix.MediaService
	memeService      *meme.Service
	client           *matrix.MatrixClient
}

// appState 持有应用程序运行时状态。
type appState struct {
	cfg      *config.Config
	flags    *cli.Flags
	info     matrix.BuildInfo
	services *services
}

// Run 初始化并运行机器人。
//
// 它处理 CLI 标志、配置加载、Matrix 客户端设置和优雅关闭。
// 返回错误而非直接调用 os.Exit，支持测试和优雅关闭。
func Run(info matrix.BuildInfo) error {
	state := &appState{info: info}

	if err := state.initConfig(); err != nil {
		return err
	}

	services, err := state.initMatrixClient()
	if err != nil {
		return err
	}
	state.services = services

	if err := state.initServices(); err != nil {
		return err
	}

	state.setupEventHandlers()

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	state.startSync(ctx)
	return state.waitForShutdown(ctx, cancel)
}

// initConfig 处理配置初始化。
//
// 返回错误而非调用 os.Exit，支持测试和优雅关闭。
func (s *appState) initConfig() error {
	s.flags = cli.Parse()

	if s.flags.ShowVersion {
		fmt.Printf("Saber Matrix Bot v%s\n", s.info.Version)
		fmt.Printf("  Git: %s (%s)\n", s.info.GitCommit, s.info.GitBranch)
		fmt.Printf("  Built: %s\n", s.info.BuildTime)
		fmt.Printf("  Go: %s\n", s.info.GoVersion)
		fmt.Printf("  Build Platform: %s\n", s.info.BuildPlatform)
		fmt.Printf("  Runtime Platform: %s\n", s.info.RuntimePlatform())
		return ExitSuccess()
	}

	if s.flags.GenerateConfig {
		if err := config.GenerateExample("config.example.yaml"); err != nil {
			return ExitError(1, fmt.Errorf("生成配置文件失败: %w", err))
		}
		fmt.Println("Example configuration generated: config.example.yaml")
		return ExitSuccess()
	}

	setupLogging(s.flags.Verbose)

	slog.Info("Starting Saber Matrix Bot",
		"version", s.info.Version,
		"git", s.info.GitCommit,
		"branch", s.info.GitBranch)

	cfg, err := config.Load(s.flags.ConfigPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	s.cfg = cfg

	slog.Info("Configuration loaded",
		"path", s.flags.ConfigPath,
		"homeserver", cfg.Matrix.Homeserver,
		"user_id", cfg.Matrix.UserID)

	return nil
}

// initMatrixClient 初始化 Matrix 客户端。
//
// 返回初始化的服务实例和错误，支持测试和优雅关闭。
func (s *appState) initMatrixClient() (*services, error) {
	client, err := matrix.NewMatrixClient(&s.cfg.Matrix)
	if err != nil {
		return nil, fmt.Errorf("创建 Matrix 客户端失败: %w", err)
	}

	if s.cfg.Matrix.UsePasswordAuth() {
		slog.Info("Performing password login...")
		if err := client.Login(context.Background()); err != nil {
			return nil, fmt.Errorf("登录失败: %w", err)
		}

		sessionPath := s.flags.ConfigPath + ".session"
		if err := client.SaveSession(sessionPath); err != nil {
			slog.Warn("Failed to save session", "error", err)
		} else {
			slog.Info("Session saved", "path", sessionPath)
		}
	}

	if err := client.VerifyLogin(context.Background()); err != nil {
		return nil, fmt.Errorf("登录验证失败: %w", err)
	}

	slog.Info("Matrix client authenticated",
		"user_id", client.GetUserID().String(),
		"device_id", client.GetDeviceID().String())

	s.initCrypto(client)

	mautrixClient := client.GetClient()
	commandService := matrix.NewCommandService(mautrixClient, client.GetUserID(), &s.info)
	matrix.RegisterBuiltinCommands(commandService)

	return &services{
		client:         client,
		commandService: commandService,
	}, nil
}

// initCrypto 初始化端到端加密。
func (s *appState) initCrypto(client *matrix.MatrixClient) {
	if s.cfg.Matrix.EnableE2EE {
		pickleKeyPath := s.cfg.Matrix.PickleKeyPath
		if pickleKeyPath == "" {
			pickleKeyPath = s.cfg.Matrix.E2EESessionPath + ".key"
		}

		pickleKey, err := matrix.LoadOrGeneratePickleKey(pickleKeyPath)
		if err != nil {
			slog.Warn("Failed to load or generate pickle key, E2EE disabled", "error", err)
		} else if err := client.InitCrypto(context.Background(), pickleKey); err != nil {
			slog.Warn("E2EE initialization failed, continuing without encryption", "error", err)
		} else {
			slog.Info("E2EE initialized successfully", "pickle_key_path", pickleKeyPath)
		}
	} else {
		if err := client.InitCrypto(context.Background(), nil); err != nil {
			slog.Warn("Failed to initialize crypto service", "error", err)
		}
	}
}

// initServices 初始化 AI、MCP 和主动聊天服务。
//
// 返回错误而非调用 os.Exit，支持测试和优雅关闭。
func (s *appState) initServices() error {
	svc := s.services

	if !s.cfg.AI.Enabled {
		return nil
	}

	slog.Info("正在初始化AI服务...")

	if err := s.cfg.AI.Validate(); err != nil {
		return fmt.Errorf("AI配置验证失败: %w", err)
	}

	svc.mcpManager = s.initMCPManager()
	if svc.mcpManager != nil {
		matrix.RegisterMCPCommands(svc.commandService, svc.mcpManager)
	}

	// 创建媒体服务
	mautrixClient := svc.client.GetClient()
	maxSizeBytes := int64(s.cfg.AI.Media.MaxSizeMB) * 1024 * 1024
	svc.mediaService = matrix.NewMediaService(mautrixClient, maxSizeBytes)

	aiService, err := ai.NewService(&s.cfg.AI, svc.commandService, svc.mcpManager, svc.mediaService)
	if err != nil {
		return fmt.Errorf("AI服务初始化失败: %w", err)
	}
	svc.aiService = aiService

	slog.Info("AI服务初始化成功",
		"provider", s.cfg.AI.Provider,
		"default_model", s.cfg.AI.DefaultModel)

	s.registerAICommands()

	if s.cfg.AI.Proactive.Enabled {
		mgr, err := s.initProactiveManager()
		if err != nil {
			return err
		}
		svc.proactiveManager = mgr
	}

	// 初始化 Meme 服务
	s.initMemeService()

	return nil
}

// initMCPManager 初始化 MCP 管理器。
func (s *appState) initMCPManager() *mcp.Manager {
	slog.Info("正在初始化MCP管理器...")
	mgr := mcp.NewManagerWithBuiltin(&s.cfg.MCP)

	if err := mgr.InitBuiltinServers(context.Background()); err != nil {
		slog.Warn("MCP内置服务器初始化失败", "error", err)
	}

	if s.cfg.MCP.Enabled && len(s.cfg.MCP.Servers) > 0 {
		if err := mgr.Init(context.Background()); err != nil {
			slog.Warn("MCP配置服务器初始化失败", "error", err)
		}
	}

	slog.Info("MCP管理器初始化成功")
	return mgr
}

// registerAICommands 注册 AI 相关命令。
func (s *appState) registerAICommands() {
	svc := s.services
	cs := svc.commandService
	aiSvc := svc.aiService

	cs.RegisterCommandWithDesc("ai", "与AI对话", ai.NewAICommand(aiSvc))
	cs.RegisterCommandWithDesc("ai-clear", "清除AI对话上下文", ai.NewClearContextCommand(aiSvc))
	cs.RegisterCommandWithDesc("ai-context", "显示AI对话上下文信息", ai.NewContextInfoCommand(aiSvc))
	cs.RegisterCommandWithDesc("ai-models", "列出所有可用模型", ai.NewModelsCommand(aiSvc))
	cs.RegisterCommandWithDesc("ai-switch", "切换默认模型 (用法: !ai-switch <model-id>)", ai.NewSwitchModelCommand(aiSvc))
	cs.RegisterCommandWithDesc("ai-current", "显示当前默认模型", ai.NewCurrentModelCommand(aiSvc))

	for modelName := range s.cfg.AI.Models {
		commandName := fmt.Sprintf("ai-%s", modelName)
		desc := fmt.Sprintf("使用%s模型与AI对话", modelName)
		cs.RegisterCommandWithDesc(commandName, desc, ai.NewMultiModelAICommand(aiSvc, modelName))
	}

	if s.cfg.AI.DirectChatAutoReply {
		cs.SetDirectChatAIHandler(ai.NewAICommand(aiSvc))
		slog.Info("私聊自动回复已启用")
	}

	if s.cfg.AI.GroupChatMentionReply {
		mautrixClient := svc.client.GetClient()
		mentionService := matrix.NewMentionService(mautrixClient, svc.client.GetUserID())
		if err := mentionService.Init(context.Background()); err != nil {
			slog.Warn("获取机器人显示名称失败，mention 功能可能受限", "error", err)
		}
		cs.SetMentionService(mentionService)
		cs.SetMentionAIHandler(ai.NewAICommand(aiSvc))
		slog.Info("群聊 mention 响应已启用",
			"bot_id", svc.client.GetUserID().String(),
			"display_name", mentionService.GetDisplayName())
	}

	if s.cfg.AI.ReplyToBotReply {
		cs.SetReplyAIHandler(ai.NewAICommand(aiSvc))
		slog.Info("回复机器人自己的回复已启用",
			"bot_id", svc.client.GetUserID().String())
	}

	slog.Info("AI 命令注册完成")
}

// initMemeService 初始化 Meme 服务。
func (s *appState) initMemeService() {
	if !s.cfg.Meme.Enabled {
		return
	}

	if err := s.cfg.Meme.Validate(); err != nil {
		slog.Warn("Meme 配置无效，跳过初始化", "error", err)
		return
	}

	svc := s.services
	mautrixClient := svc.client.GetClient()

	memeSvc := meme.NewService(&s.cfg.Meme)
	svc.memeService = memeSvc

	cs := svc.commandService

	// 注册主命令，支持 --gif/--sticker/--meme 参数
	cs.RegisterCommandWithDesc("meme",
		"搜索并发送梗图 (用法: !meme [--gif|--sticker|--meme] <关键词>)",
		meme.NewMemeCommand(cs, mautrixClient, memeSvc))

	// 注册便捷命令
	cs.RegisterCommandWithDesc("gif",
		"搜索 GIF 动图 (用法: !gif <关键词>)",
		meme.NewTypedMemeCommand(cs, mautrixClient, memeSvc, meme.ContentTypeGIF))

	cs.RegisterCommandWithDesc("sticker",
		"搜索贴纸 (用法: !sticker <关键词>)",
		meme.NewTypedMemeCommand(cs, mautrixClient, memeSvc, meme.ContentTypeSticker))

	slog.Info("Meme 服务已启用")
}

// initProactiveManager 初始化主动聊天管理器。
//
// 返回管理器实例和错误，支持测试和优雅关闭。
func (s *appState) initProactiveManager() (*ai.ProactiveManager, error) {
	slog.Info("正在初始化主动聊天管理器...")
	roomService := matrix.NewRoomService(s.services.client)

	mgr, err := ai.NewProactiveManager(
		&s.cfg.AI.Proactive,
		s.services.aiService,
		roomService,
		nil,
		&s.cfg.AI,
	)
	if err != nil {
		return nil, fmt.Errorf("主动聊天管理器初始化失败: %w", err)
	}

	slog.Info("主动聊天管理器初始化成功")
	return mgr, nil
}

// setupEventHandlers 设置事件处理器。
func (s *appState) setupEventHandlers() {
	svc := s.services

	// 使用配置的并发数创建 EventHandler
	maxConcurrent := s.cfg.Matrix.MaxConcurrentEvents
	if maxConcurrent <= 0 {
		maxConcurrent = 10 // 默认值
	}

	eventHandler := matrix.NewEventHandler(svc.commandService, maxConcurrent)

	if svc.proactiveManager != nil {
		eventHandler.SetProactiveManager(svc.proactiveManager)
	}

	mautrixClient := svc.client.GetClient()
	if syncer, ok := mautrixClient.Syncer.(*mautrix.DefaultSyncer); ok {
		syncer.OnSync(mautrixClient.DontProcessOldEvents)
		syncer.OnEventType(event.EventMessage, eventHandler.OnMessage)
		syncer.OnEventType(event.StateMember, eventHandler.OnMember)
	} else {
		slog.Warn("Client syncer is not DefaultSyncer, event handling may not work")
	}

	svc.eventHandler = eventHandler
	svc.presence = matrix.NewPresenceService(mautrixClient)

	if err := svc.presence.SetPresence("online", "Saber Bot is running"); err != nil {
		slog.Warn("Failed to set presence", "error", err)
	}

	s.autoJoinRooms()
	slog.Info("事件处理器初始化完成", "max_concurrent_events", maxConcurrent)
}

// autoJoinRooms 自动加入配置的房间。
func (s *appState) autoJoinRooms() {
	if len(s.cfg.Matrix.AutoJoinRooms) == 0 {
		return
	}

	rooms := matrix.NewRoomService(s.services.client)
	for _, roomID := range s.cfg.Matrix.AutoJoinRooms {
		slog.Info("Joining room", "room", roomID)
		if _, err := rooms.JoinRoom(context.Background(), roomID); err != nil {
			slog.Warn("Failed to join room", "room", roomID, "error", err)
		}
	}
}

// setupSignalHandler 设置信号处理器。
//
// 返回 context 和 cancel 函数。
func (s *appState) setupSignalHandler() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		slog.Info("Shutdown signal received", "signal", sig.String())
		cancel()
	}()

	return ctx, cancel
}

// startSync 启动 Matrix 同步。
func (s *appState) startSync(ctx context.Context) {
	svc := s.services

	go func() {
		reconnectCfg := matrix.DefaultReconnectConfig()
		slog.Info("Starting Matrix sync with auto-reconnect",
			"max_retries", reconnectCfg.MaxRetries,
			"initial_delay", reconnectCfg.InitialDelay,
			"max_delay", reconnectCfg.MaxDelay)

		if err := svc.presence.StartSyncWithReconnect(ctx, reconnectCfg); err != nil {
			if err != context.Canceled {
				slog.Error("Sync failed", "error", err)
			}
		}
	}()

	if svc.proactiveManager != nil {
		svc.proactiveManager.Start(ctx)
	}

	slog.Info("Saber Bot is running",
		"version", s.info.Version,
		"git", s.info.GitCommit,
		"branch", s.info.GitBranch)
	slog.Info("Press Ctrl+C to exit.")
}

// waitForShutdown 等待关闭信号并执行优雅关闭。
//
// 返回 nil 表示正常关闭。
func (s *appState) waitForShutdown(ctx context.Context, cancel context.CancelFunc) error {
	<-ctx.Done()
	s.shutdown(cancel)
	return nil
}

// shutdown 执行优雅关闭。
//
// 它会：
// 1. 并行停止所有服务
// 2. 等待所有服务完成或超时
// 3. 记录关闭日志
func (s *appState) shutdown(cancel context.CancelFunc) {
	svc := s.services

	// 从配置获取超时时间
	timeoutSeconds := s.cfg.Shutdown.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30 // 默认值
	}
	shutdownTimeout := time.Duration(timeoutSeconds) * time.Second

	slog.Info("开始优雅关闭", "timeout", shutdownTimeout)

	// 创建带超时的上下文
	ctx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// 使用 WaitGroup 并行关闭所有服务
	var wg sync.WaitGroup

	if svc.aiService != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Debug("正在停止 AI 服务...")
			svc.aiService.Stop()
			slog.Debug("AI 服务已停止")
		}()
	}

	if svc.mcpManager != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Debug("正在关闭 MCP 连接...")
			if err := svc.mcpManager.Close(); err != nil {
				slog.Warn("关闭 MCP 管理器失败", "error", err)
			} else {
				slog.Debug("MCP 连接已关闭")
			}
		}()
	}

	if svc.proactiveManager != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Debug("正在停止主动聊天管理器...")
			svc.proactiveManager.Stop()
			slog.Debug("主动聊天管理器已停止")
		}()
	}

	// 等待所有服务关闭完成或超时
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("所有服务已优雅关闭")
	case <-ctx.Done():
		slog.Warn("关闭超时，强制退出",
			"timeout", shutdownTimeout,
			"hint", "考虑增加 shutdown.timeout_seconds 配置值")
	}

	cancel()
	slog.Info("Bot 已停止")
}

// setupLogging 配置全局日志记录器。
//
// 使用 tint handler 提供彩色输出，根据 verbose 标志设置日志级别。
func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	handler := tint.NewHandler(os.Stdout, &tint.Options{
		Level: level,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	if verbose {
		slog.Debug("Debug logging enabled")
	}
}
