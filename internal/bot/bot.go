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

	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// Run initializes and runs the bot.
// It handles CLI flags, configuration, Matrix client setup, and graceful shutdown.
func Run(version string) {
	flags := cli.Parse()

	// Set version for cli package
	cli.Version = version

	if flags.ShowVersion {
		fmt.Printf("Saber Matrix Bot v%s\n", version)
		os.Exit(0)
	}

	if flags.GenerateConfig {
		if err := generateExampleConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Example configuration generated: config.example.yaml")
		os.Exit(0)
	}

	// Setup logging
	setupLogging(flags.Verbose)

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
		pickleKey, err := matrix.GeneratePickleKey()
		if err != nil {
			slog.Warn("Failed to generate pickle key, E2EE disabled", "error", err)
		} else if err := client.InitCrypto(context.Background(), pickleKey); err != nil {
			slog.Warn("E2EE initialization failed, continuing without encryption", "error", err)
		} else {
			slog.Info("E2EE initialized successfully")
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

	slog.Info("Saber Bot is running. Press Ctrl+C to exit.")

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

// generateExampleConfig creates an example configuration file.
func generateExampleConfig() error {
	exampleConfig := `matrix:
  # Matrix 服务器地址
  homeserver: "https://matrix.org"

  # 完整的 Matrix 用户 ID（格式：@username:server.org）
  user_id: "@your-bot:matrix.org"

  # 设备标识符（可选，留空则服务器自动生成）
  device_id: "saber-bot-device"

  # 设备显示名称（可选）
  device_name: "Saber Bot"

  # 认证方式（二选一，access_token 优先级更高）
  # 方式 1: 使用 Access Token（推荐，更安全）
  access_token: "syt_xxxxxxxxxxxxx_xxxxxxxxxxxx"

  # 方式 2: 使用密码登录（首次登录使用）
  # password: "your-secure-password"

  # 启动时自动加入的房间列表（可选）
  # auto_join_rooms:
  #   - "!roomid1:matrix.org"
  #   - "#public-room:matrix.org"

  # 端到端加密（E2EE）配置（可选）
  enable_e2ee: true  # 启用端到端加密
  e2ee_session_path: "./saber.session"  # 加密会话文件路径
`

	return os.WriteFile("config.example.yaml", []byte(exampleConfig), 0o644)
}
