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

// Run 初始化并运行机器人。
//
// 它处理 CLI 标志、配置加载、Matrix 客户端设置和优雅关闭。
func Run(version, gitMsg string) {
	flags := cli.Parse()

	if flags.ShowVersion {
		fmt.Printf("Saber Matrix Bot v%s (%s)\n", version, gitMsg)
		os.Exit(0)
	}

	if flags.GenerateConfig {
		if err := config.GenerateExample("config.example.yaml"); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Example configuration generated: config.example.yaml")
		os.Exit(0)
	}

	// 配置日志
	setupLogging(flags.Verbose)

	slog.Info("Starting Saber Matrix Bot", "version", version, "git", gitMsg)

	// 加载配置
	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		slog.Error("Failed to load configuration", "error", err, "path", flags.ConfigPath)
		os.Exit(1)
	}

	slog.Info("Configuration loaded",
		"path", flags.ConfigPath,
		"homeserver", cfg.Matrix.Homeserver,
		"user_id", cfg.Matrix.UserID)

	// 创建 Matrix 客户端
	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	if err != nil {
		slog.Error("Failed to create Matrix client", "error", err)
		os.Exit(1)
	}

	// 如果使用密码认证，执行登录
	if cfg.Matrix.UsePasswordAuth() {
		slog.Info("Performing password login...")
		if err := client.Login(context.Background()); err != nil {
			slog.Error("Login failed", "error", err)
			os.Exit(1)
		}

		// 可选：保存会话以供后续使用
		sessionPath := flags.ConfigPath + ".session"
		if err := client.SaveSession(sessionPath); err != nil {
			slog.Warn("Failed to save session", "error", err)
		} else {
			slog.Info("Session saved", "path", sessionPath)
		}
	}

	// 验证登录
	if err := client.VerifyLogin(context.Background()); err != nil {
		slog.Error("Login verification failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Matrix client authenticated",
		"user_id", client.GetUserID().String(),
		"device_id", client.GetDeviceID().String())

	// 初始化端到端加密（E2EE）
	if cfg.Matrix.EnableE2EE {
		pickleKeyPath := cfg.Matrix.PickleKeyPath
		if pickleKeyPath == "" {
			pickleKeyPath = cfg.Matrix.E2EESessionPath + ".key"
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
		// 初始化无操作的加密服务
		if err := client.InitCrypto(context.Background(), nil); err != nil {
			slog.Warn("Failed to initialize crypto service", "error", err)
		}
	}

	// 设置命令服务
	mautrixClient := client.GetClient()
	commandService := matrix.NewCommandService(mautrixClient, client.GetUserID())

	// 注册内置命令
	matrix.RegisterBuiltinCommands(commandService)

	// 初始化AI服务（如果启用）
	var aiService *ai.Service
	if cfg.AI.Enabled {
		slog.Info("正在初始化AI服务...")

		// 验证AI配置
		if err := cfg.AI.Validate(); err != nil {
			slog.Error("AI配置验证失败", "error", err)
			os.Exit(1)
		}

		// 初始化MCP管理器（如果配置了MCP）
		var mcpManager *mcp.Manager
		if cfg.MCP.Enabled {
			slog.Info("正在初始化MCP管理器...")
			mcpManager = mcp.NewManager(&cfg.MCP)
			if err := mcpManager.Init(context.Background()); err != nil {
				slog.Error("MCP管理器初始化失败", "error", err)
				os.Exit(1)
			}
			slog.Info("MCP管理器初始化成功")
		}

		// 创建AI服务实例
		aiService, err = ai.NewService(&cfg.AI, commandService, mcpManager)
		if err != nil {
			slog.Error("AI服务初始化失败", "error", err)
			os.Exit(1)
		}

		slog.Info("AI服务初始化成功",
			"provider", cfg.AI.Provider,
			"default_model", cfg.AI.DefaultModel)
	}

	// 注册AI相关命令（如果AI服务已初始化）
	if aiService != nil {
		// 注册基础AI命令
		commandService.RegisterCommandWithDesc("ai", "与AI对话", ai.NewAICommand(aiService))

		// 注册上下文管理命令
		commandService.RegisterCommandWithDesc("ai-clear", "清除AI对话上下文", ai.NewClearContextCommand(aiService))
		commandService.RegisterCommandWithDesc("ai-context", "显示AI对话上下文信息", ai.NewContextInfoCommand(aiService))

		// 注册多模型AI命令
		for modelName := range cfg.AI.Models {
			commandName := fmt.Sprintf("ai-%s", modelName)
			desc := fmt.Sprintf("使用%s模型与AI对话", modelName)
			commandService.RegisterCommandWithDesc(commandName, desc, ai.NewMultiModelAICommand(aiService, modelName))
		}

		// 启用私聊自动回复
		if cfg.AI.DirectChatAutoReply {
			commandService.SetDirectChatAIHandler(ai.NewAICommand(aiService))
			slog.Info("私聊自动回复已启用")
		}

		// 启用群聊 mention 自动回复
		if cfg.AI.GroupChatMentionReply {
			mentionService := matrix.NewMentionService(mautrixClient, client.GetUserID())
			if err := mentionService.Init(context.Background()); err != nil {
				slog.Warn("获取机器人显示名称失败，mention 功能可能受限", "error", err)
			}
			commandService.SetMentionService(mentionService)
			commandService.SetMentionAIHandler(ai.NewAICommand(aiService))
			slog.Info("群聊 mention 响应已启用",
				"bot_id", client.GetUserID().String(),
				"display_name", mentionService.GetDisplayName())
		}

		// 启用回复机器人自己的回复（用于连续对话）
		if cfg.AI.ReplyToBotReply {
			commandService.SetReplyAIHandler(ai.NewAICommand(aiService))
			slog.Info("回复机器人自己的回复已启用",
				"bot_id", client.GetUserID().String())
		}

		slog.Info("AI 命令注册完成")
	}

	// 设置事件处理器
	eventHandler := matrix.NewEventHandler(commandService)

	// 设置同步器：注册消息事件处理器
	// 使用 OnEventType 为特定事件类型注册回调
	if syncer, ok := mautrixClient.Syncer.(*mautrix.DefaultSyncer); ok {
		syncer.OnSync(mautrixClient.DontProcessOldEvents)
		syncer.OnEventType(event.EventMessage, eventHandler.OnMessage)
		syncer.OnEventType(event.StateMember, eventHandler.OnMember)
	} else {
		slog.Warn("Client syncer is not DefaultSyncer, event handling may not work")
	}

	// 创建在线状态服务
	presence := matrix.NewPresenceService(mautrixClient)

	// 设置在线状态为 online
	if err := presence.SetPresence("online", "Saber Bot is running"); err != nil {
		slog.Warn("Failed to set presence", "error", err)
	}

	// 如果配置了自动加入房间
	if len(cfg.Matrix.AutoJoinRooms) > 0 {
		rooms := matrix.NewRoomService(client)
		for _, roomID := range cfg.Matrix.AutoJoinRooms {
			slog.Info("Joining room", "room", roomID)
			if _, err := rooms.JoinRoom(context.Background(), roomID); err != nil {
				slog.Warn("Failed to join room", "room", roomID, "error", err)
			}
		}
	}

	// 设置关闭信号处理
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 启动同步并自动重连
	go func() {
		reconnectCfg := matrix.DefaultReconnectConfig()
		slog.Info("Starting Matrix sync with auto-reconnect",
			"max_retries", reconnectCfg.MaxRetries,
			"initial_delay", reconnectCfg.InitialDelay,
			"max_delay", reconnectCfg.MaxDelay)

		if err := presence.StartSyncWithReconnect(ctx, reconnectCfg); err != nil {
			if err != context.Canceled {
				slog.Error("Sync failed", "error", err)
			}
		}
	}()

	slog.Info("Saber Bot is running", "version", version, "git", gitMsg)
	slog.Info("Press Ctrl+C to exit.")

	// 等待关闭信号
	sig := <-sigChan
	slog.Info("Shutdown signal received", "signal", sig.String())

	// 优雅关闭
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
