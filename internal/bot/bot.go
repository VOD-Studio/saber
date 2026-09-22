// Package bot 封装所有机器人初始化和运行逻辑。
package bot

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
	"rua.plus/saber/internal/meme"
	"rua.plus/saber/internal/persona"
	"rua.plus/saber/internal/platform"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
	violetplatform "rua.plus/saber/internal/platform/violet"
	"rua.plus/saber/internal/server"
	"rua.plus/saber/internal/tui"
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
	personaService   *persona.Service
	client           *matrix.MatrixClient
	// platforms 是已接入的聊天平台，按配置启动并统一停止。
	platforms *platform.Registry
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
// 它处理 CLI 标志与配置，默认运行本机服务，显式启用时接入 Matrix。
// 返回错误而非直接调用 os.Exit，支持测试和优雅关闭。
func Run(info matrix.BuildInfo) error {
	return run(context.Background(), info)
}

func run(parent context.Context, info matrix.BuildInfo) error {
	state := &appState{info: info}

	if err := state.initConfig(os.Args[1:], os.Stdout); err != nil {
		return err
	}

	if state.flags.Command == "chat" {
		endpoint := state.flags.ServerURL
		if endpoint == "" {
			endpoint = "http://" + state.cfg.Server.Listen
		}
		token, err := server.Token(state.cfg.Server.TokenPath(state.flags.ConfigPath), false)
		if err != nil {
			return err
		}
		client, err := server.NewClient(endpoint, token)
		if err != nil {
			return err
		}
		return tui.Run(parent, client, state.flags.Session)
	}
	if err := state.cfg.Server.Validate(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", state.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("无法启动服务（可能已运行）: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warn("关闭监听失败", "error", err)
		}
	}()
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	state.services = &services{}
	defer state.shutdown(cancel)
	if state.cfg.Matrix.Enabled {
		svc, err := state.initMatrixClient()
		if err != nil {
			return err
		}
		state.services = svc
	}
	if err := state.initServices(); err != nil {
		return err
	}
	// 先让平台接上共享聊天入口，再开始投递事件，避免入站消息找不到处理链路。
	for _, p := range state.services.platforms.Enabled(state.cfg) {
		if err := p.Start(ctx, state.services.aiService.HandleChat); err != nil {
			slog.Warn("平台启动失败", "platform", p.Name(), "error", err)
		}
	}
	if state.services.client != nil {
		state.setupEventHandlers()
		state.startSync(ctx)
	}
	token, err := server.Token(state.cfg.Server.TokenPath(state.flags.ConfigPath), true)
	if err != nil {
		return err
	}
	slog.Info("Saber 服务已就绪", "listen", state.cfg.Server.Listen, "chat", "saber chat")
	return server.Serve(ctx, &http.Server{Addr: state.cfg.Server.Listen, Handler: server.New(state.services.aiService, token), ReadHeaderTimeout: 5 * time.Second}, listener)
}

// initConfig 处理配置初始化。
//
// args 为命令行参数（不含程序名），stdout 接收版本与示例配置输出。
// 返回错误而非调用 os.Exit，支持测试和优雅关闭。
func (s *appState) initConfig(args []string, stdout io.Writer) error {
	flags, err := cli.ParseArgs(args)
	s.flags = flags
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess()
		}
		return ExitError(2, err)
	}

	if s.flags.ShowVersion {
		text := fmt.Sprintf("Saber v%s\n  Git: %s (%s)\n  Built: %s\n  Go: %s\n  Build Platform: %s\n  Runtime Platform: %s\n",
			s.info.Version, s.info.GitCommit, s.info.GitBranch, s.info.BuildTime,
			s.info.GoVersion, s.info.BuildPlatform, s.info.RuntimePlatform())
		_, _ = fmt.Fprint(stdout, text)
		return ExitSuccess()
	}

	if s.flags.GenerateConfig {
		if s.flags.OutputPath != "" {
			// 输出到指定文件
			if err := config.GenerateExample(s.flags.OutputPath); err != nil {
				return ExitError(1, fmt.Errorf("生成配置文件失败: %w", err))
			}
			_, _ = fmt.Fprintf(stdout, "Example configuration generated: %s\n", s.flags.OutputPath)
		} else {
			// 输出到 stdout
			_, _ = fmt.Fprint(stdout, config.ExampleConfig())
		}
		return ExitSuccess()
	}

	if s.flags.Command == "chat" {
		cfg, err := config.LoadOrDefault(s.flags.ConfigPath)
		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}
		s.cfg = cfg
		return nil
	}
	setupLogging(s.flags.Verbose)

	slog.Info("Starting Saber",
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
		"matrix_enabled", cfg.Matrix.Enabled)

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
	// 注入加密服务到 CommandService
	commandService.SetCryptoService(client.GetCryptoService())
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
	// 平台注册表与 AI 是否启用无关：run() 与 shutdown() 都会遍历它，
	// 缺少这一步时关闭 AI 会拿到 nil 注册表并 panic。
	svc.platforms = platform.NewRegistry()

	if !s.cfg.AI.Enabled {
		return nil
	}

	slog.Info("正在初始化AI服务...")

	if err := s.cfg.AI.Validate(); err != nil {
		return fmt.Errorf("AI配置验证失败: %w", err)
	}
	secrets := []string{s.cfg.Matrix.AccessToken, s.cfg.Matrix.Password}
	for _, provider := range s.cfg.AI.Providers {
		secrets = append(secrets, provider.APIKey)
		for _, model := range provider.Models {
			secrets = append(secrets, model.APIKey)
		}
	}
	for _, model := range s.cfg.AI.Models {
		secrets = append(secrets, model.APIKey)
	}
	for name, server := range s.cfg.MCP.Servers {
		if err := execution.ValidateEnvironment(server.Env, secrets); err != nil {
			return fmt.Errorf("MCP %s 环境验证失败: %w", name, err)
		}
	}

	svc.mcpManager = s.initMCPManager()
	if svc.mcpManager != nil && svc.commandService != nil {
		matrix.RegisterMCPCommands(svc.commandService, svc.mcpManager)
	}

	// 媒体下载仅在 Matrix 接入时初始化。
	if svc.client != nil {
		mautrixClient := svc.client.GetClient()
		maxSizeBytes := int64(s.cfg.Matrix.Media.MaxSizeMB) * 1024 * 1024
		svc.mediaService = matrix.NewMediaService(mautrixClient, maxSizeBytes)
	}

	aiService, err := ai.NewService(s.cfg, ai.WithMatrix(svc.commandService, svc.mediaService), ai.WithMCP(svc.mcpManager))
	if err != nil {
		return fmt.Errorf("AI服务初始化失败: %w", err)
	}
	svc.aiService = aiService
	configDir := filepath.Dir(s.flags.ConfigPath)
	protected := []string{s.cfg.Server.TokenPath(s.flags.ConfigPath), s.flags.ConfigPath, s.flags.ConfigPath + ".session", s.cfg.Matrix.E2EESessionPath, s.cfg.Matrix.E2EESessionPath + ".key", s.cfg.Matrix.PickleKeyPath, filepath.Join(configDir, "tasks.db"), filepath.Join(configDir, "persona.db")}
	if err := aiService.ConfigureExecution(s.cfg.Execution, protected, secrets); err != nil {
		return fmt.Errorf("执行权限初始化失败: %w", err)
	}

	slog.Info("AI服务初始化成功",
		"default_model", s.cfg.AI.DefaultModel)

	// 任务运行独立于入口，Matrix 只注册自己的投递器。
	if err := aiService.EnableTasks(filepath.Join(configDir, "tasks.db")); err != nil {
		return fmt.Errorf("任务服务初始化失败: %w", err)
	}
	// Matrix 作为平台接入端持有聊天命令入口与任务投递器，ai 核心不再构造平台 adapter。
	// 命令入口暂时只有 Matrix 能挂：ai.ChatEntrypoint 既是单例 setter，签名又带 mautrix 类型。
	if svc.client != nil {
		matrixPlatform := platformmatrix.New(s.cfg, svc.commandService, svc.mediaService, aiService.HandleChatModel)
		aiService.SetChatEntrypoint(matrixPlatform)
		if err := s.registerPlatform(matrixPlatform, aiService); err != nil {
			return err
		}
	}

	// Violet 不挂命令入口，只接管「私聊直答 + 群聊被 @」这条最小链路。
	// 注册必须留在下面 Matrix 专属装配的提前 return 之前：否则关掉 Matrix 就什么都注册不了。
	if s.cfg.Platforms.Violet.Enabled {
		if err := s.cfg.Platforms.Violet.Validate(); err != nil {
			return fmt.Errorf("violet 平台配置无效: %w", err)
		}
		if err := s.registerPlatform(violetplatform.New(s.cfg), aiService); err != nil {
			return err
		}
	}

	// 可选的 Matrix 功能（人格、!ai 命令注册、主动聊天、meme）只在启用该入口时装配。
	if svc.client == nil {
		return nil
	}

	// 初始化人格服务
	s.initPersonaService()

	s.registerAICommands()

	if s.cfg.Matrix.Proactive.Enabled {
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

// registerPlatform 接入一个聊天平台：能收任务结果的先注册投递器，然后进注册表等待 Start。
//
// 命令入口不在这里挂：那仍由 Matrix 独占（见 ai.ChatEntrypoint 的单例限制），
// 新平台先按各自的触发规则走 HandleChat。没有实现 platform.TaskDelivery 的平台
// 只接即时消息，任务与计划结果不会投到它那里。
func (s *appState) registerPlatform(p platform.Platform, aiService *ai.Service) error {
	if delivery, ok := p.(platform.TaskDelivery); ok {
		if err := aiService.RegisterTaskDelivery(p.Name(), delivery.DeliveryAdapter()); err != nil {
			return fmt.Errorf("%s 平台任务投递注册失败: %w", p.Name(), err)
		}
	}
	s.services.platforms.Register(p)
	return nil
}

// initMCPManager 初始化 MCP 管理器。
func (s *appState) initMCPManager() *mcp.Manager {
	slog.Info("正在初始化MCP管理器...")
	mgr := mcp.NewManagerWithBuiltin(&s.cfg.MCP)
	if !s.cfg.MCP.Enabled {
		return mgr
	}

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

	// 创建 AI 命令路由器，统一处理 !ai <subcommand> 格式
	aiRouter := ai.NewAICommandRouter(aiSvc)
	aiRouter.RegisterSubcommand("clear", ai.NewClearContextCommand(aiSvc))
	aiRouter.RegisterSubcommand("context", ai.NewContextInfoCommand(aiSvc))
	aiRouter.RegisterSubcommand("models", ai.NewModelsCommand(aiSvc))
	aiRouter.RegisterSubcommand("switch", ai.NewSwitchModelCommand(aiSvc))
	aiRouter.RegisterSubcommand("current", ai.NewCurrentModelCommand(aiSvc))

	cs.RegisterCommandWithDesc("ai", "AI 相关命令 (用法: !ai <子命令>)", aiRouter)

	// 注册模型快捷命令（如 !ai-gpt-4）
	for modelName := range s.cfg.AI.Models {
		commandName := fmt.Sprintf("ai-%s", modelName)
		desc := fmt.Sprintf("使用%s模型与AI对话", modelName)
		cs.RegisterCommandWithDesc(commandName, desc, ai.NewMultiModelAICommand(aiSvc, modelName))
	}

	if s.cfg.Matrix.DirectChatAutoReply {
		cs.SetDirectChatAIHandler(ai.NewAICommand(aiSvc))
		slog.Info("私聊自动回复已启用")
	}

	if s.cfg.Matrix.GroupChatMentionReply {
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

	if s.cfg.Matrix.ReplyToBotReply {
		cs.SetReplyAIHandler(ai.NewAICommand(aiSvc))
		slog.Info("回复机器人自己的回复已启用",
			"bot_id", svc.client.GetUserID().String())
	}

	slog.Info("AI 命令注册完成")
}

// initPersonaService 初始化人格服务。
func (s *appState) initPersonaService() {
	svc := s.services

	// 确定 persona 数据库路径
	configDir := filepath.Dir(s.flags.ConfigPath)
	dbPath := filepath.Join(configDir, "persona.db")

	personaSvc, err := persona.NewService(dbPath)
	if err != nil {
		slog.Warn("人格服务初始化失败", "error", err, "db_path", dbPath)
		return
	}
	svc.personaService = personaSvc

	// 设置 AI 服务的人格服务
	svc.aiService.SetPromptProvider(personaSvc)

	// 注册 persona 命令
	cs := svc.commandService
	cs.RegisterCommandWithDesc("persona",
		"人格管理 (用法: !persona [list|set|clear|status|new|del])",
		persona.NewPersonaCommand(cs, personaSvc))

	slog.Info("人格服务已启用", "db_path", dbPath)
}

// initMemeService 初始化 Meme 服务。
func (s *appState) initMemeService() {
	if !s.cfg.Matrix.Meme.Enabled {
		return
	}

	if err := s.cfg.Matrix.Meme.Validate(); err != nil {
		slog.Warn("Meme 配置无效，跳过初始化", "error", err)
		return
	}

	svc := s.services
	mautrixClient := svc.client.GetClient()

	memeSvc := meme.NewService(&s.cfg.Matrix.Meme)
	svc.memeService = memeSvc

	cs := svc.commandService

	// 注册主命令，支持 --gif/--sticker/--meme 参数
	cs.RegisterCommandWithDesc("meme",
		"搜索并发送梗图 (用法: !meme [--gif|--sticker|--meme] <关键词>)",
		meme.NewMemeCommand(cs, mautrixClient, memeSvc))

	slog.Info("Meme 服务已启用")
}

// initProactiveManager 初始化主动聊天管理器。
//
// 返回管理器实例和错误，支持测试和优雅关闭。
func (s *appState) initProactiveManager() (*ai.ProactiveManager, error) {
	slog.Info("正在初始化主动聊天管理器...")
	// Matrix 房间服务由平台接入端包装成通用会话端口，ai 核心不直接依赖 internal/matrix。
	rooms := platformmatrix.NewRooms(matrix.NewRoomService(s.services.client))

	mgr, err := ai.NewProactiveManager(
		&s.cfg.Matrix.Proactive,
		s.services.aiService,
		rooms,
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
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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

	// 使用 WaitGroup 并行关闭所有服务：wg.Go 负责计数，避免 Add/Done 成对写错
	var wg sync.WaitGroup

	if svc.aiService != nil {
		wg.Go(func() {
			slog.Debug("正在停止 AI 服务...")
			svc.aiService.Stop()
			slog.Debug("AI 服务已停止")
		})
	}

	if svc.mcpManager != nil {
		wg.Go(func() {
			slog.Debug("正在关闭 MCP 连接...")
			if err := svc.mcpManager.Close(); err != nil {
				slog.Warn("关闭 MCP 管理器失败", "error", err)
			} else {
				slog.Debug("MCP 连接已关闭")
			}
		})
	}

	if svc.platforms != nil {
		for _, p := range svc.platforms.Enabled(s.cfg) {
			platformName := p.Name()
			wg.Go(func() {
				slog.Debug("正在停止平台...", "platform", platformName)
				p.Stop()
				slog.Debug("平台已停止", "platform", platformName)
			})
		}
	}

	if svc.proactiveManager != nil {
		wg.Go(func() {
			slog.Debug("正在停止主动聊天管理器...")
			svc.proactiveManager.Stop()
			slog.Debug("主动聊天管理器已停止")
		})
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
	slog.Info("Saber 已停止")
}

// setupLogging 配置全局日志记录器。
//
// 使用 tint handler 提供彩色输出，根据 verbose 标志设置日志级别。
func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	logger := slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{
		Level: level,
	}))
	slog.SetDefault(logger)

	if verbose {
		slog.Debug("Debug logging enabled")
	}
}
