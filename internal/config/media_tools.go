package config

import (
	"fmt"
	"net/url"
	"strings"
)

// MediaProviderConfig 将模型、凭据和端点固定在可信配置中，工具参数不能覆盖它们。
// command 接收一行 JSON 标准输入并在标准输出返回 JSON；由授权执行后端运行。
type MediaProviderConfig struct {
	Enabled        bool           `yaml:"enabled"`
	Provider       string         `yaml:"provider"`
	BaseURL        string         `yaml:"base_url,omitempty"`
	APIKey         string         `yaml:"api_key,omitempty"`
	APIKeyEnv      string         `yaml:"api_key_env,omitempty"`
	Model          string         `yaml:"model,omitempty"`
	Voice          string         `yaml:"voice,omitempty"`
	Language       string         `yaml:"language,omitempty"`
	Format         string         `yaml:"format,omitempty"`
	Speed          float64        `yaml:"speed,omitempty"`
	Command        []string       `yaml:"command,omitempty"`
	PluginTool     string         `yaml:"plugin_tool,omitempty"`
	StreamingModel string         `yaml:"streaming_model,omitempty"`
	EditModel      string         `yaml:"edit_model,omitempty"`
	UpscaleModel   string         `yaml:"upscale_model,omitempty"`
	Options        map[string]any `yaml:"options,omitempty"`
}

// VoiceConfig 控制本地录音、VAD、停止短语及全双工会话；默认不占用麦克风。
type VoiceConfig struct {
	Enabled          bool                `yaml:"enabled"`
	Mode             string              `yaml:"mode"`
	Live             MediaProviderConfig `yaml:"live"`
	CaptureCommand   []string            `yaml:"capture_command,omitempty"`
	PlaybackCommand  []string            `yaml:"playback_command,omitempty"`
	SampleRate       int                 `yaml:"sample_rate"`
	SilenceThreshold float64             `yaml:"silence_threshold"`
	SilenceMS        int                 `yaml:"silence_ms"`
	SpeechMS         int                 `yaml:"speech_ms"`
	MaxRecordSeconds int                 `yaml:"max_record_seconds"`
	StopPhrases      []string            `yaml:"stop_phrases"`
	WakeWord         string              `yaml:"wake_word,omitempty"`
	WakeCommand      []string            `yaml:"wake_command,omitempty"`
	WakeCooldownMS   int                 `yaml:"wake_cooldown_ms"`
	BargeIn          bool                `yaml:"barge_in"`
}

// MediaToolsConfig 配置可选 ASR、TTS、生成和理解能力，不隐式选择收费服务。
type MediaToolsConfig struct {
	Enabled           bool                `yaml:"enabled"`
	ASR               MediaProviderConfig `yaml:"asr"`
	TTS               MediaProviderConfig `yaml:"tts"`
	Image             MediaProviderConfig `yaml:"image"`
	Video             MediaProviderConfig `yaml:"video"`
	Vision            MediaProviderConfig `yaml:"vision"`
	Voice             VoiceConfig         `yaml:"voice"`
	MaxBytes          int64               `yaml:"max_bytes"`
	TimeoutSeconds    int                 `yaml:"timeout_seconds"`
	VideoFrames       int                 `yaml:"video_frames"`
	FFmpegCommand     string              `yaml:"ffmpeg_command"`
	AutoTranscribe    bool                `yaml:"auto_transcribe"`
	AutoVision        bool                `yaml:"auto_vision"`
	AllowedMediaHosts []string            `yaml:"allowed_media_hosts,omitempty"`
}

// DefaultMediaToolsConfig 默认关闭上传和麦克风，仅提供明确启用后的保守资源限额。
func DefaultMediaToolsConfig() MediaToolsConfig {
	return MediaToolsConfig{MaxBytes: 25 * 1024 * 1024, TimeoutSeconds: 120, VideoFrames: 8, FFmpegCommand: "ffmpeg", AutoTranscribe: true,
		Voice: VoiceConfig{Mode: "chained", SampleRate: 16000, SilenceThreshold: 200, SilenceMS: 3000, SpeechMS: 300, MaxRecordSeconds: 120, StopPhrases: []string{"stop"}, WakeCooldownMS: 2000, BargeIn: true}}
}

// Validate 拒绝未实现的协议、无上限读取及可被模型替换的隐式供应商。
func (c MediaToolsConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxBytes <= 0 || c.MaxBytes > 256*1024*1024 || c.TimeoutSeconds <= 0 || c.VideoFrames < 1 || c.VideoFrames > 32 {
		return fmt.Errorf("media_tools: invalid byte/time/frame limits")
	}
	allowed := map[string][]string{
		"asr":    {"whisper_cpp", "openai", "groq", "mistral", "xai", "elevenlabs", "deepinfra", "command", "plugin"},
		"tts":    {"piper", "espeak", "edge", "openai", "deepinfra", "elevenlabs", "xai", "minimax", "mistral", "gemini", "command", "plugin"},
		"image":  {"openai", "fal", "xai", "openrouter", "gemini", "command", "plugin"},
		"video":  {"xai", "fal", "command", "plugin"},
		"vision": {"model", "command", "plugin"},
		"live":   {"openai"},
	}
	for name, p := range map[string]MediaProviderConfig{"asr": c.ASR, "tts": c.TTS, "image": c.Image, "video": c.Video, "vision": c.Vision, "live": c.Voice.Live} {
		if !p.Enabled {
			continue
		}
		found := false
		for _, v := range allowed[name] {
			if p.Provider == v {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("media_tools.%s: unsupported provider %q", name, p.Provider)
		}
		if p.Provider == "command" && len(p.Command) == 0 {
			return fmt.Errorf("media_tools.%s.command required", name)
		}
		if p.Provider == "plugin" && p.PluginTool == "" {
			return fmt.Errorf("media_tools.%s.plugin_tool required", name)
		}
		if p.BaseURL != "" {
			u, e := url.Parse(p.BaseURL)
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("media_tools.%s: invalid base_url", name)
			}
		}
		for _, arg := range p.Command {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("media_tools.%s: command contains NUL", name)
			}
		}
		if p.Speed < 0 || p.Speed > 4 {
			return fmt.Errorf("media_tools.%s: invalid speed", name)
		}
		if name == "tts" && p.Format != "" {
			valid := false
			for _, f := range []string{"mp3", "wav", "opus", "ogg", "flac", "aac", "pcm"} {
				if p.Format == f {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("media_tools.tts.format unsupported")
			}
		}
	}
	if c.Voice.Enabled {
		v := c.Voice
		if v.Mode != "chained" && v.Mode != "gpt-live" {
			return fmt.Errorf("media_tools.voice.mode must be chained or gpt-live")
		}
		if v.SampleRate != 16000 || v.SilenceThreshold <= 0 || v.SilenceMS <= 0 || v.SpeechMS <= 0 || v.MaxRecordSeconds <= 0 || v.MaxRecordSeconds > 600 || v.WakeCooldownMS < 0 {
			return fmt.Errorf("media_tools.voice: invalid PCM/VAD limits")
		}
		if v.Mode == "gpt-live" && !v.Live.Enabled {
			return fmt.Errorf("media_tools.voice.live must be enabled for gpt-live")
		}
		for _, argv := range [][]string{v.CaptureCommand, v.PlaybackCommand, v.WakeCommand} {
			for _, arg := range argv {
				if strings.ContainsRune(arg, 0) {
					return fmt.Errorf("media_tools.voice: command contains NUL")
				}
			}
		}
	}
	return nil
}
