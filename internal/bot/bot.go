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
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/lmittmann/tint"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/command"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
	"rua.plus/saber/internal/meme"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/persona"
	"rua.plus/saber/internal/platform"
	discordplatform "rua.plus/saber/internal/platform/discord"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
	slackplatform "rua.plus/saber/internal/platform/slack"
	telegramplatform "rua.plus/saber/internal/platform/telegram"
	terminalplatform "rua.plus/saber/internal/platform/terminal"
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
	commandRegistry  *command.Registry
	mediaService     *matrix.MediaService
	memeService      *meme.Service
	personaService   *persona.Service
	memoryService    *memory.Service
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
	// listener 是 run() 在初始化服务前占住的终端监听；由 terminal 平台在 Start 时接管。
	listener net.Listener
}

// terminalPlatformName 是本机 HTTP 入口的平台标识，与配置键一致。
const terminalPlatformName = "terminal"

// Run 初始化并运行机器人。
//
// 它处理 CLI 标志与配置，默认运行本机服务，显式启用时接入 Matrix。
// 返回错误而非直接调用 os.Exit，支持测试和优雅关闭。
func Run(info matrix.BuildInfo) error {
	return run(context.Background(), info)
}

func run(parent context.Context, info matrix.BuildInfo) error {
	state := &appState{info: info}

	// doctor 必须在严格配置加载失败时也能输出结构化结果。
	if flags, err := cli.ParseArgs(os.Args[1:]); err == nil && flags.Command == "doctor" || len(os.Args) > 1 && os.Args[1] == "doctor" {
		return runDoctor(parent, os.Args[1:], os.Stdout)
	}

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
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	state.services = &services{}
	defer state.shutdown(cancel)

	// 终端入口在初始化服务之前占住端口：端口冲突要早于数据库等副作用失败。
	// 关闭 terminal 平台时既不监听端口，也不生成令牌文件。
	if state.cfg.Platforms.Terminal.Enabled {
		listener, err := net.Listen("tcp", state.cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("无法启动服务（可能已运行）: %w", err)
		}
		state.listener = listener
		defer func() {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Warn("关闭监听失败", "error", err)
			}
		}()
	}
	if state.cfg.Platforms.Matrix.Enabled {
		svc, err := state.initMatrixClient()
		if err != nil {
			return err
		}
		state.services = svc
	}
	if err := state.initServices(); err != nil {
		return err
	}
	// 平台按注册顺序启动：terminal 起本机 HTTP 监听，Matrix 注册事件并跑同步循环，
	// 这里不再有第二份事件注册或同步启动。
	for _, p := range state.services.platforms.Enabled(state.cfg) {
		if err := p.Start(ctx, state.handleChat); err != nil {
			if p.Name() == terminalPlatformName {
				// 本机入口是进程的主要用途，起不来直接退出，避免静默变成一个不响应 saber chat 的进程。
				return fmt.Errorf("终端平台启动失败: %w", err)
			}
			slog.Warn("平台启动失败", "platform", p.Name(), "error", err)
		}
	}
	// 主动聊天跟随进程生命周期，与平台同步一起关闭。
	if state.services.proactiveManager != nil {
		state.services.proactiveManager.Start(ctx)
	}
	slog.Info("Saber 已启动", "platforms", len(state.services.platforms.Enabled(state.cfg)), "chat", "saber chat")
	<-ctx.Done()
	return nil
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
	// 配置文件可留在工作区作为指向私有状态目录的符号链接；运行状态须跟随真实配置路径保存。
	resolved, err := resolveConfigPath(s.flags.ConfigPath)
	if err != nil {
		return fmt.Errorf("解析配置文件符号链接失败: %w", err)
	}
	s.flags.ConfigPath = resolved

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
		"matrix_enabled", cfg.Platforms.Matrix.Enabled)

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
	if err := s.cfg.Commands.Validate(); err != nil {
		return fmt.Errorf("命令授权配置无效: %w", err)
	}
	if err := s.cfg.Memory.Validate(); err != nil {
		return fmt.Errorf("长期记忆配置无效: %w", err)
	}
	if err := s.initMemoryService(); err != nil {
		return err
	}

	if !s.cfg.AI.Enabled {
		s.registerTerminalPlatform()
		if svc.client != nil {
			svc.platforms.Register(s.newMatrixPlatform())
		}
		if s.cfg.Platforms.Violet.Enabled {
			if err := s.cfg.Platforms.Violet.Validate(); err != nil {
				return fmt.Errorf("violet 平台配置无效: %w", err)
			}
			svc.platforms.Register(violetplatform.New(s.cfg))
		}
		// 其余消息平台只登记：没有 AI 时它们仅保持连接可用，不产生回复。
		if s.cfg.Platforms.Telegram.Enabled {
			if err := s.cfg.Platforms.Telegram.Validate(); err != nil {
				return fmt.Errorf("telegram 平台配置无效: %w", err)
			}
			svc.platforms.Register(telegramplatform.New(s.cfg))
		}
		if s.cfg.Platforms.Discord.Enabled {
			if err := s.cfg.Platforms.Discord.Validate(); err != nil {
				return fmt.Errorf("discord 平台配置无效: %w", err)
			}
			svc.platforms.Register(discordplatform.New(s.cfg))
		}
		if s.cfg.Platforms.Slack.Enabled {
			if err := s.cfg.Platforms.Slack.Validate(); err != nil {
				return fmt.Errorf("slack 平台配置无效: %w", err)
			}
			svc.platforms.Register(slackplatform.New(s.cfg))
		}
		if err := s.registerConnectorPlatforms(nil); err != nil {
			return err
		}
		if svc.memoryService != nil {
			// 记忆与技能命令不依赖模型；AI 关闭时也装配服务，保证 /memory 与 /skill
			// 的文件导入导出可用。模型请求仍由 IsEnabled 拒绝。
			memoryAI, err := ai.NewService(s.cfg, ai.WithMemory(svc.memoryService))
			if err != nil {
				return fmt.Errorf("AI服务初始化失败: %w", err)
			}
			svc.aiService = memoryAI
		}
		return s.buildCommands()
	}

	slog.Info("正在初始化AI服务...")

	if err := s.cfg.AI.Validate(); err != nil {
		return fmt.Errorf("AI配置验证失败: %w", err)
	}
	protected, secrets := executionInputs(s.cfg, s.flags.ConfigPath)
	for name, server := range s.cfg.MCP.Servers {
		if err := execution.ValidateEnvironment(server.Env, secrets); err != nil {
			return fmt.Errorf("MCP %s 环境验证失败: %w", name, err)
		}
	}

	svc.mcpManager = s.initMCPManager()

	// 媒体下载仅在 Matrix 接入时初始化。
	if svc.client != nil {
		mautrixClient := svc.client.GetClient()
		maxSizeBytes := int64(s.cfg.Matrix.Media.MaxSizeMB) * 1024 * 1024
		svc.mediaService = matrix.NewMediaService(mautrixClient, maxSizeBytes)
	}

	configDir := filepath.Dir(s.flags.ConfigPath)

	aiService, err := ai.NewService(s.cfg,
		ai.WithMatrix(svc.commandService, svc.mediaService),
		ai.WithMCP(svc.mcpManager),
		ai.WithMemory(svc.memoryService),
		// 命令注册表在本函数末尾才构建，因此闭包延迟解析，循环自动化复用同一套授权与回执。
		ai.WithSessionCommands(func(ctx context.Context, message chat.Message) (string, error) {
			commands := localCommands{registry: svc.commandRegistry, capabilities: s.commandCapabilities}
			text, handled, err := commands.Dispatch(ctx, message.Session, message.SenderID, message.ID, message.Text)
			if err != nil {
				return "", err
			}
			if !handled {
				return "", errors.New("会话指令未被命令注册表处理")
			}
			return text, nil
		}))
	if err != nil {
		return fmt.Errorf("AI服务初始化失败: %w", err)
	}
	svc.aiService = aiService
	if err := aiService.ConfigureExecution(s.cfg.Execution, protected, secrets); err != nil {
		return fmt.Errorf("执行权限初始化失败: %w", err)
	}

	slog.Info("AI服务初始化成功",
		"default_model", s.cfg.AI.DefaultModel)

	// 任务运行独立于入口，Matrix 只注册自己的投递器。
	if err := aiService.EnableTasks(filepath.Join(configDir, "tasks.db")); err != nil {
		return fmt.Errorf("任务服务初始化失败: %w", err)
	}
	s.initPersonaService()
	s.initMemeService()

	// 本机终端入口先登记：注册顺序即启动顺序，本地 HTTP 端口最先就绪。
	s.registerTerminalPlatform()

	// Violet 在私聊或被寻址的群聊中使用同一命令入口。
	if s.cfg.Platforms.Violet.Enabled {
		if err := s.cfg.Platforms.Violet.Validate(); err != nil {
			return fmt.Errorf("violet 平台配置无效: %w", err)
		}
		violet := violetplatform.New(s.cfg)
		// 推文互动是平台的可选第二条入口：平台侧仍受 tweets.enabled 控制，
		// 工具侧再按账号与配置独立判断可见性。
		aiService.SetTweetAdapter(violet)
		// 表情目录与收藏随平台启用即可用，不需要额外配置开关。
		aiService.SetEmojiAdapter(violet)
		aiService.SetAvatarAdapter("violet", violet)
		violet.SetTweetHandler(aiService.HandleTweet)
		if err := s.registerPlatform(violet, aiService); err != nil {
			return err
		}
	}

	// Telegram/Discord/Slack 与 Violet 同层：都走共享 Handler 与任务投递适配器。
	if s.cfg.Platforms.Telegram.Enabled {
		if err := s.cfg.Platforms.Telegram.Validate(); err != nil {
			return fmt.Errorf("telegram 平台配置无效: %w", err)
		}
		if err := s.registerPlatform(telegramplatform.New(s.cfg), aiService); err != nil {
			return err
		}
	}
	if s.cfg.Platforms.Discord.Enabled {
		if err := s.cfg.Platforms.Discord.Validate(); err != nil {
			return fmt.Errorf("discord 平台配置无效: %w", err)
		}
		if err := s.registerPlatform(discordplatform.New(s.cfg), aiService); err != nil {
			return err
		}
	}
	if s.cfg.Platforms.Slack.Enabled {
		if err := s.cfg.Platforms.Slack.Validate(); err != nil {
			return fmt.Errorf("slack 平台配置无效: %w", err)
		}
		if err := s.registerPlatform(slackplatform.New(s.cfg), aiService); err != nil {
			return err
		}
	}

	// Matrix 自动回复、主动聊天与事件处理器只在启用 Matrix 时装配。
	if svc.client != nil {
		s.registerAICommands()
		if s.cfg.Matrix.Proactive.Enabled {
			mgr, err := s.initProactiveManager()
			if err != nil {
				return err
			}
			svc.proactiveManager = mgr
		}
		matrixPlatform := s.newMatrixPlatform()
		aiService.SetAvatarAdapter("matrix", matrixPlatform)
		if err := s.registerPlatform(matrixPlatform, aiService); err != nil {
			return err
		}
	}
	if err := s.registerConnectorPlatforms(aiService); err != nil {
		return err
	}
	return s.buildCommands()
}

// initMemoryService 按配置打开长期记忆库；关闭时保持 nil，命令与工具不注册。
//
// 它与 AI 是否启用无关：/skill files 与 /skill import 不调用模型，
// AI 关闭时仍应可用。
func (s *appState) initMemoryService() error {
	if !s.cfg.Memory.Enabled {
		return nil
	}
	configDir := filepath.Dir(s.flags.ConfigPath)
	memorySvc, err := memory.Open(filepath.Join(configDir, "memory.db"), memory.Config{
		MaxChars:       s.cfg.Memory.MaxChars,
		InjectBytes:    s.cfg.Memory.InjectMaxBytes,
		GroupWriter:    s.cfg.Commands.CanWriteSession,
		SkillDirs:      s.cfg.Memory.Skills.Dirs,
		SkillExportDir: s.cfg.Memory.Skills.ExportDir,
	})
	if err != nil {
		return fmt.Errorf("长期记忆初始化失败: %w", err)
	}
	s.services.memoryService = memorySvc
	return nil
}

// registerTerminalPlatform 把本机 HTTP 入口登记为平台。
//
// 关闭 platforms.terminal.enabled 时既不注册也不监听，也不生成令牌文件，saber chat
// 会因缺少令牌或连不上而明确失败，而不是悄悄连上一个本不存在的服务。
func (s *appState) registerTerminalPlatform() {
	if !s.cfg.Platforms.Terminal.Enabled {
		slog.Warn("本机终端入口已关闭", "hint", "platforms.terminal.enabled=false，saber chat 将无法连接")
		return
	}
	// 命令注册表在 buildCommands 之后才存在，因此用闭包延迟解析。
	commands := func() server.CommandDispatcher {
		return localCommands{registry: s.services.commandRegistry, capabilities: s.commandCapabilities}
	}
	s.services.platforms.Register(terminalplatform.New(
		s.cfg.Server.Listen,
		s.cfg.Server.TokenPath(s.flags.ConfigPath),
		s.services.aiService,
		commands,
		terminalplatform.WithListener(s.listener),
	))
}

// newMatrixPlatform 组装 Matrix 接入端。
//
// 登录与 E2EE 已在 initMatrixClient/initCrypto 完成；这里注入同步会话与事件处理器，
// 由平台在 Start 时注册事件并启动同步循环，装配层不再自己起一份。
func (s *appState) newMatrixPlatform() *platformmatrix.Platform {
	svc := s.services
	return platformmatrix.New(s.cfg, svc.commandService, svc.mediaService,
		platformmatrix.WithSession(platformmatrix.NewSession(svc.client, s.cfg.Matrix.AutoJoinRooms)),
		platformmatrix.WithEventHandler(s.newEventHandler()),
		platformmatrix.WithAvatarClient(svc.client.GetClient()),
	)
}

// newEventHandler 创建 Matrix 事件处理器，并接入已装配的主动聊天管理器。
func (s *appState) newEventHandler() *matrix.EventHandler {
	maxConcurrent := s.cfg.Matrix.MaxConcurrentEvents
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	handler := matrix.NewEventHandler(s.services.commandService, maxConcurrent)
	if s.services.proactiveManager != nil {
		handler.SetProactiveManager(s.services.proactiveManager)
	}
	return handler
}

// registerPlatform 接入一个聊天平台：能收任务结果的先注册投递器，然后进注册表等待 Start。
//
// 平台接入端按各自的触发规则走同一 Handler。没有实现 platform.TaskDelivery 的平台
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

// registerConnectorPlatforms 把显式启用的扩展平台连接器接入注册表，
// 并在任务服务可用时登记结果投递与跨平台交接路由。
// aiService 为 nil 表示 AI 关闭：只登记连接，不注册投递，与该分支其余消息平台行为一致。
func (s *appState) registerConnectorPlatforms(aiService *ai.Service) error {
	stateDir := filepath.Dir(s.flags.ConfigPath)
	if err := platform.RegisterConnectors(s.services.platforms, s.cfg.Platforms, stateDir); err != nil {
		return fmt.Errorf("连接器平台注册失败: %w", err)
	}
	if aiService == nil {
		return nil
	}
	names := make([]string, 0, len(s.cfg.Platforms.Connectors))
	for name, c := range s.cfg.Platforms.Connectors {
		if c.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if mgr := aiService.Tasks(); mgr != nil {
		for _, name := range names {
			p, ok := s.services.platforms.Lookup(name)
			if !ok {
				continue
			}
			if delivery, ok := p.(platform.TaskDelivery); ok {
				if err := aiService.RegisterTaskDelivery(name, delivery.DeliveryAdapter()); err != nil {
					return fmt.Errorf("%s 平台任务投递注册失败: %w", name, err)
				}
			}
		}
		if s.cfg.Handoff.Enabled {
			mgr.SetHandoffResolver(platform.HandoffResolver(s.cfg.Handoff, s.services.platforms))
		}
	}
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

// registerAICommands 装配 Matrix 自动回复触发；文本命令由共享注册表执行。
func (s *appState) registerAICommands() {
	svc := s.services
	cs := svc.commandService
	chatAdapter := matrix.NewChatAdapter(cs, svc.mediaService, s.cfg.Matrix.Media, s.cfg.Matrix.StreamEdit.Enabled, s.handleChat)

	if s.cfg.Matrix.DirectChatAutoReply {
		cs.SetDirectChatAIHandler(chatAdapter)
		slog.Info("私聊自动回复已启用")
	}

	if s.cfg.Matrix.GroupChatMentionReply {
		mautrixClient := svc.client.GetClient()
		mentionService := matrix.NewMentionService(mautrixClient, svc.client.GetUserID())
		if err := mentionService.Init(context.Background()); err != nil {
			slog.Warn("获取机器人显示名称失败，mention 功能可能受限", "error", err)
		}
		cs.SetMentionService(mentionService)
		cs.SetMentionAIHandler(chatAdapter)
		slog.Info("群聊 mention 响应已启用",
			"bot_id", svc.client.GetUserID().String(),
			"display_name", mentionService.GetDisplayName())
	}

	if s.cfg.Matrix.ReplyToBotReply {
		cs.SetReplyAIHandler(chatAdapter)
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
	if account := s.cfg.Commands.LegacyPersonaAccount; account != "" && svc.client != nil {
		if account != svc.client.GetUserID().String() {
			slog.Warn("旧人格归属账号与当前 Matrix bot 不同，保留待核查", "configured_account", account)
		} else if migrated, err := personaSvc.MigrateLegacyMatrix(context.Background(), account); err != nil {
			slog.Warn("迁移旧 Matrix 人格绑定失败，保留待核查", "error", err)
		} else if migrated > 0 {
			slog.Info("已按明确配置关联旧 Matrix 人格绑定", "count", migrated)
		}
	}

	// 设置 AI 服务的人格服务
	svc.aiService.SetPromptProvider(personaSvc)

	// 注册 persona 命令
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
	memeSvc := meme.NewService(&s.cfg.Matrix.Meme)
	svc.memeService = memeSvc

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

// setupSignalHandler 设置信号处理器。
//
// 返回 context 和 cancel 函数。
func (s *appState) setupSignalHandler() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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

	if svc.memoryService != nil {
		wg.Go(func() {
			slog.Debug("正在关闭长期记忆服务...")
			if err := svc.memoryService.Close(); err != nil {
				slog.Warn("关闭长期记忆服务失败", "error", err)
			}
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
