// Package bot encapsulates all bot initialization and running logic.
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
)

// Run initializes and runs the bot.
// It handles CLI flags, configuration, Matrix client setup, and graceful shutdown.
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

	// Setup logging
	setupLogging(flags.Verbose)

	slog.Info("Starting Saber Matrix Bot", "version", version, "git", gitMsg)

	// Load configuration
	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		slog.Error("Failed to load configuration", "error", err, "path", flags.ConfigPath)
		os.Exit(1)
	}

	slog.Info("Configuration loaded",
		"path", flags.ConfigPath,
		"homeserver", cfg.Matrix.Homeserver,
		"user_id", cfg.Matrix.UserID)

	// Create Matrix client
	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	if err != nil {
		slog.Error("Failed to create Matrix client", "error", err)
		os.Exit(1)
	}

	// If using password auth, perform login
	if cfg.Matrix.UsePasswordAuth() {
		slog.Info("Performing password login...")
		if err := client.Login(context.Background()); err != nil {
			slog.Error("Login failed", "error", err)
			os.Exit(1)
		}

		// Optionally save session for future use
		sessionPath := flags.ConfigPath + ".session"
		if err := client.SaveSession(sessionPath); err != nil {
			slog.Warn("Failed to save session", "error", err)
		} else {
			slog.Info("Session saved", "path", sessionPath)
		}
	}

	// Verify login
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

	// Setup command service
	mautrixClient := client.GetClient()
	commandService := matrix.NewCommandService(mautrixClient, client.GetUserID())

	// Register built-in commands
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

		// 创建AI服务实例
		aiService, err = ai.NewService(&cfg.AI, commandService)
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

		slog.Info("AI命令注册完成")
	}

	// Setup event handler
	eventHandler := matrix.NewEventHandler(commandService)

	// Setup syncer - register event handler for message events
	// Use OnEventType to register callback for specific event types
	if syncer, ok := mautrixClient.Syncer.(*mautrix.DefaultSyncer); ok {
		syncer.OnEventType(event.EventMessage, eventHandler.OnMessage)
		syncer.OnEventType(event.StateMember, eventHandler.OnMember)
	} else {
		slog.Warn("Client syncer is not DefaultSyncer, event handling may not work")
	}

	// Create presence service
	presence := matrix.NewPresenceService(mautrixClient)

	// Set presence to online
	if err := presence.SetPresence("online", "Saber Bot is running"); err != nil {
		slog.Warn("Failed to set presence", "error", err)
	}

	// Auto-join rooms if configured
	if len(cfg.Matrix.AutoJoinRooms) > 0 {
		rooms := matrix.NewRoomService(client)
		for _, roomID := range cfg.Matrix.AutoJoinRooms {
			slog.Info("Joining room", "room", roomID)
			if _, err := rooms.JoinRoom(context.Background(), roomID); err != nil {
				slog.Warn("Failed to join room", "room", roomID, "error", err)
			}
		}
	}

	// Setup shutdown signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start sync with auto-reconnect
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

	// Wait for shutdown signal
	sig := <-sigChan
	slog.Info("Shutdown signal received", "signal", sig.String())

	// Graceful shutdown
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
