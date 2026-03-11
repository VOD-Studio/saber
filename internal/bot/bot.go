// Package bot encapsulates all bot initialization and running logic.
package bot

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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
		log.Fatal().Err(err).Str("path", flags.ConfigPath).Msg("Failed to load configuration")
	}

	log.Info().
		Str("path", flags.ConfigPath).
		Str("homeserver", cfg.Matrix.Homeserver).
		Str("user_id", cfg.Matrix.UserID).
		Msg("Configuration loaded")

	// Create Matrix client
	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Matrix client")
	}

	// If using password auth, perform login
	if cfg.Matrix.UsePasswordAuth() {
		log.Info().Msg("Performing password login...")
		if err := client.Login(context.Background()); err != nil {
			log.Fatal().Err(err).Msg("Login failed")
		}

		// Optionally save session for future use
		sessionPath := flags.ConfigPath + ".session"
		if err := client.SaveSession(sessionPath); err != nil {
			log.Warn().Err(err).Msg("Failed to save session")
		} else {
			log.Info().Str("path", sessionPath).Msg("Session saved")
		}
	}

	// Verify login
	if err := client.VerifyLogin(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("Login verification failed")
	}

	log.Info().
		Str("user_id", client.GetUserID().String()).
		Str("device_id", client.GetDeviceID().String()).
		Msg("Matrix client authenticated")

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
	} else {
		log.Warn().Msg("Client syncer is not DefaultSyncer, event handling may not work")
	}

	// Create presence service
	presence := matrix.NewPresenceService(mautrixClient)

	// Set presence to online
	if err := presence.SetPresence("online", "Saber Bot is running"); err != nil {
		log.Warn().Err(err).Msg("Failed to set presence")
	}

	// Auto-join rooms if configured
	if len(cfg.Matrix.AutoJoinRooms) > 0 {
		rooms := matrix.NewRoomService(client)
		for _, roomID := range cfg.Matrix.AutoJoinRooms {
			log.Info().Str("room", roomID).Msg("Joining room")
			if _, err := rooms.JoinRoom(context.Background(), roomID); err != nil {
				log.Warn().Err(err).Str("room", roomID).Msg("Failed to join room")
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
		log.Info().
			Int("max_retries", reconnectCfg.MaxRetries).
			Dur("initial_delay", reconnectCfg.InitialDelay).
			Dur("max_delay", reconnectCfg.MaxDelay).
			Msg("Starting Matrix sync with auto-reconnect")

		if err := presence.StartSyncWithReconnect(ctx, reconnectCfg); err != nil {
			if err != context.Canceled {
				log.Error().Err(err).Msg("Sync failed")
			}
		}
	}()

	log.Info().Msg("Saber Bot is running. Press Ctrl+C to exit.")

	// Wait for shutdown signal
	sig := <-sigChan
	log.Info().Str("signal", sig.String()).Msg("Shutdown signal received")

	// Graceful shutdown
	cancel()
	log.Info().Msg("Bot stopped")
}

// setupLogging configures the global logger based on verbose flag.
func setupLogging(verbose bool) {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	if verbose {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Info().Msg("Debug logging enabled")
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
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
  auto_join_rooms:
    - "!roomid1:matrix.org"
    - "#public-room:matrix.org"
`

	return os.WriteFile("config.example.yaml", []byte(exampleConfig), 0o644)
}
