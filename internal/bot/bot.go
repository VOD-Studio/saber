// Package bot 封装所有机器人初始化和运行逻辑。
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lmittmann/tint"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

// services 持有所有需要管理的服务实例。
type services struct {
	aiService        *ai.Service
	mcpManager       *mcp.Manager
	proactiveManager *ai.ProactiveManager
	commandService   *matrix.CommandService
	eventHandler     *matrix.EventHandler
	presence         *matrix.PresenceService
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
func Run(info matrix.BuildInfo) {
	state := &appState{info: info}

	if !state.initConfig() {
		return
	}

	state.services = state.initMatrixClient()
	if state.services == nil {
		return
	}

	if !state.initServices() {
		return
	}

	state.setupEventHandlers()

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	state.startSync(ctx)
	state.waitForShutdown(ctx, cancel)
}

// initConfig 处理配置初始化。
//
// 返回 true 表示成功继续，false 表示应该退出。
func (s *appState) initConfig() bool {
	s.flags = cli.Parse()

	if s.flags.ShowVersion {
		fmt.Printf("Saber Matrix Bot v%s\n", s.info.Version)
		fmt.Printf("  Git: %s (%s)\n", s.info.GitCommit, s.info.GitBranch)
		fmt.Printf("  Built: %s\n", s.info.BuildTime)
		fmt.Printf("  Go: %s\n", s.info.GoVersion)
		fmt.Printf("  Platform: %s\n", s.info.Platform)
		os.Exit(0)
	}

	if s.flags.GenerateConfig {
		if err := config.GenerateExample("config.example.yaml"); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Example configuration generated: config.example.yaml")
		os.Exit(0)
	}

	setupLogging(s.flags.Verbose)

	slog.Info("Starting Saber Matrix Bot",
		"version", s.info.Version,
		"git", s.info.GitCommit,
		"branch", s.info.GitBranch)

	cfg, err := config.Load(s.flags.ConfigPath)
	if err != nil {
		slog.Error("Failed to load configuration",
			"error", err,
			"path", s.flags.ConfigPath)
		os.Exit(1)
	}

	s.cfg = cfg

	slog.Info("Configuration loaded",
		"path", s.flags.ConfigPath,
		"homeserver", cfg.Matrix.Homeserver,
		"user_id", cfg.Matrix.UserID)

	return true
}

// initMatrixClient 初始化 Matrix 客户端。
//
// 返回初始化的服务实例，如果失败返回 nil。
func (s *appState) initMatrixClient() *services {
	client, err := matrix.NewMatrixClient(&s.cfg.Matrix)
	if err != nil {
		slog.Error("Failed to create Matrix client", "error", err)
		os.Exit(1)
	}

	if s.cfg.Matrix.UsePasswordAuth() {
		slog.Info("Performing password login...")
		if err := client.Login(context.Background()); err != nil {
			slog.Error("Login failed", "error", err)
			os.Exit(1)
		}

		sessionPath := s.flags.ConfigPath + ".session"
		if err := client.SaveSession(sessionPath); err != nil {
			slog.Warn("Failed to save session", "error", err)
		} else {
			slog.Info("Session saved", "path", sessionPath)
		}
	}

	if err := client.VerifyLogin(context.Background()); err != nil {
		slog.Error("Login verification failed", "error", err)
		os.Exit(1)
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
	}
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
// 返回 true 表示成功，false 表示应该退出。
func (s *appState) initServices() bool {
	svc := s.services

	if !s.cfg.AI.Enabled {
		return true
	}

	slog.Info("正在初始化AI服务...")

	if err := s.cfg.AI.Validate(); err != nil {
		slog.Error("AI配置验证失败", "error", err)
		os.Exit(1)
	}

	svc.mcpManager = s.initMCPManager()
	if svc.mcpManager != nil {
		matrix.RegisterMCPCommands(svc.commandService, svc.mcpManager)
	}

	aiService, err := ai.NewService(&s.cfg.AI, svc.commandService, svc.mcpManager)
	if err != nil {
		slog.Error("AI服务初始化失败", "error", err)
		os.Exit(1)
	}
	svc.aiService = aiService

	slog.Info("AI服务初始化成功",
		"provider", s.cfg.AI.Provider,
		"default_model", s.cfg.AI.DefaultModel)

	s.registerAICommands()

	if s.cfg.AI.Proactive.Enabled {
		svc.proactiveManager = s.initProactiveManager()
	}

	return true
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

// initProactiveManager 初始化主动聊天管理器。
func (s *appState) initProactiveManager() *ai.ProactiveManager {
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
		slog.Error("主动聊天管理器初始化失败", "error", err)
		os.Exit(1)
	}

	slog.Info("主动聊天管理器初始化成功")
	return mgr
}

// setupEventHandlers 设置事件处理器。
func (s *appState) setupEventHandlers() {
	svc := s.services
	eventHandler := matrix.NewEventHandler(svc.commandService)

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
func (s *appState) waitForShutdown(ctx context.Context, cancel context.CancelFunc) {
	<-ctx.Done()
	s.shutdown(cancel)
}

// shutdown 执行优雅关闭。
func (s *appState) shutdown(cancel context.CancelFunc) {
	svc := s.services

	if svc.aiService != nil {
		slog.Info("Stopping AI service...")
		svc.aiService.Stop()
	}

	if svc.mcpManager != nil {
		slog.Info("Closing MCP connections...")
		if err := svc.mcpManager.Close(); err != nil {
			slog.Warn("Failed to close MCP manager", "error", err)
		}
	}

	if svc.proactiveManager != nil {
		slog.Info("Stopping proactive manager...")
		svc.proactiveManager.Stop()
	}

	cancel()
	slog.Info("Bot stopped")
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
