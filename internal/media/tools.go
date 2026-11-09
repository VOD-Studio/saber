package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// Definitions 仅公开显式启用且实际实现的工具，具体执行仍逐次核对身份及精确权限。
func (s *Service) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	var defs []toolkit.Definition
	if s.cfg.ASR.Enabled {
		defs = append(defs, tool("audio_transcribe", "转录实际音频，返回原文与时间戳；模型与凭据由配置决定。", map[string]any{"audio_url": textProp()}, []string{"audio_url"}, s.cfg.ASR))
	}
	if s.cfg.TTS.Enabled {
		defs = append(defs, tool("tts", "将正文合成为实际音频文件，不朗读思考块或代码。", map[string]any{"text": textProp()}, []string{"text"}, s.cfg.TTS))
	}
	imageProps := map[string]any{"prompt": textProp(), "aspect_ratio": textProp(), "size": textProp(), "quality": textProp(), "image_url": textProp(), "reference_image_urls": map[string]any{"type": "array", "items": textProp(), "maxItems": 8}, "seed": map[string]any{"type": "integer"}, "num_images": map[string]any{"type": "integer", "minimum": 1, "maximum": 4}}
	switch s.cfg.Image.Provider {
	case "openai":
		delete(imageProps, "seed")
	case "gemini", "openrouter":
		delete(imageProps, "seed")
		delete(imageProps, "num_images")
		delete(imageProps, "quality")
		delete(imageProps, "size")
	case "xai":
		delete(imageProps, "seed")
		delete(imageProps, "quality")
		delete(imageProps, "size")
	case "fal":
		delete(imageProps, "quality")
		delete(imageProps, "size")
	}
	if p := s.cfg.Image; p.Enabled {
		if p.Provider == "xai" {
			imageProps["reference_image_urls"] = map[string]any{"type": "array", "items": textProp(), "maxItems": 1}
		}
		generationProps := imageProps
		if p.Provider == "fal" && p.EditModel == "" {
			generationProps = make(map[string]any, len(imageProps))
			for k, v := range imageProps {
				if k != "image_url" && k != "reference_image_urls" {
					generationProps[k] = v
				}
			}
		}
		defs = append(defs, tool("image_generate", "生成图像；仅配置支持编辑时接受参考图片，模型固定于配置。", generationProps, []string{"prompt"}, p))
		if p.Provider == "openai" || p.Provider == "xai" || p.Provider == "command" || p.Provider == "plugin" || (p.Provider == "fal" && p.EditModel != "") {
			defs = append(defs, tool("image_edit", "对真实输入图片执行指定编辑。", imageProps, []string{"prompt", "image_url"}, p))
		}
		if p.Provider == "command" || p.Provider == "plugin" || (p.Provider == "fal" && p.UpscaleModel != "") {
			defs = append(defs, tool("image_upscale", "调用已配置的真实超分辨率模型放大图像。", imageProps, []string{"image_url"}, p))
		}
	}
	videoProps := map[string]any{"prompt": textProp(), "image_url": textProp(), "reference_image_urls": map[string]any{"type": "array", "items": textProp(), "maxItems": 8}, "aspect_ratio": textProp(), "resolution": textProp(), "duration": map[string]any{"type": "integer", "minimum": 1, "maximum": 60}}
	if p := s.cfg.Video; p.Provider == "xai" || (p.Provider == "fal" && (p.Options["reference_parameter"] == nil || p.Options["reference_parameter"] == "image_url")) {
		videoProps["reference_image_urls"] = map[string]any{"type": "array", "items": textProp(), "maxItems": 1}
	}
	if p := s.cfg.Video; p.Enabled {
		defs = append(defs, tool("video_generate", "生成视频并等待真实供应商任务结果；中断返回 request_id，可显式恢复查询。", videoProps, []string{"prompt"}, p))
		if p.Provider == "xai" {
			for _, name := range []string{"xai_video_edit", "xai_video_extend"} {
				defs = append(defs, tool(name, "使用 xAI Imagine 编辑或延长真实公共 HTTPS 视频。", map[string]any{"prompt": textProp(), "video_url": textProp(), "duration": map[string]any{"type": "integer", "minimum": 1, "maximum": 60}}, []string{"prompt", "video_url"}, p))
			}
		}
	}
	if s.cfg.Image.Enabled || s.cfg.Video.Enabled {
		defs = append(defs, tool("media_job", "读取本会话持久化媒体任务，resume仅查询已有请求，不重新生成；cancel需供应商支持。", map[string]any{"request_id": textProp(), "action": map[string]any{"type": "string", "enum": []string{"status", "resume", "cancel"}}}, []string{"request_id", "action"}, config.MediaProviderConfig{Enabled: true, Provider: "remote"}))
	}
	if s.cfg.Vision.Enabled {
		defs = append(defs, tool("vision_analyze", "理解真实图片或抽取覆盖视频时长的帧；视频解码另需 exec 与 ffmpeg。", map[string]any{"image_url": textProp(), "video_url": textProp(), "prompt": textProp()}, nil, s.cfg.Vision))
	}
	defs = append(defs, tool("media_download", "下载授权媒体为规范附件；拒绝内网地址、凭据 URL 和超限载荷。", map[string]any{"url": textProp(), "kind": map[string]any{"type": "string", "enum": []string{"image", "audio", "video", "file"}}}, []string{"url", "kind"}, config.MediaProviderConfig{Enabled: true, Provider: "remote"}))
	if v := s.cfg.Voice; v.Enabled {
		if len(v.CaptureCommand) > 0 {
			defs = append(defs, tool("voice_capture", "从实际麦克风录制一轮语音；持续语音确认后由静音结束，执行环境需录音设备。", map[string]any{}, nil, config.MediaProviderConfig{Enabled: true, Provider: "command"}))
		}
		if v.WakeWord != "" && (len(v.WakeCommand) > 0 || (s.cfg.ASR.Enabled && s.cfg.ASR.Provider == "whisper_cpp")) {
			d := tool("wake_word", "将实际录音交给本地 KWS 或配置的离线 whisper.cpp，返回真实检测结果。", map[string]any{"audio_url": textProp()}, []string{"audio_url"}, config.MediaProviderConfig{Enabled: true, Provider: "command"})
			if len(v.WakeCommand) == 0 {
				d.Requires = append(d.Requires, "audio_transcribe")
			}
			defs = append(defs, d)
		}
		if v.Live.Enabled && v.Mode == "gpt-live" {
			defs = append(defs, tool("voice_live", "交换 WebRTC SDP 与配置中的实时语音模型建立会话，密钥留在宿主。", map[string]any{"sdp": textProp()}, []string{"sdp"}, v.Live))
		}
	}
	if v := s.cfg.Voice; v.Enabled && len(v.PlaybackCommand) > 0 {
		defs = append(defs, tool("voice_playback", "将实际音频送到授权执行环境中的播放器。", map[string]any{"audio_url": textProp()}, []string{"audio_url"}, config.MediaProviderConfig{Enabled: true, Provider: "command"}))
	}
	return defs
}

// Execute 派发真实能力，不接受供应商 URL、身份或凭据覆盖参数。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	known := false
	for _, d := range s.Definitions() {
		if d.Tool.Function.Name == name {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("media tool %s disabled or unknown", name)
	}
	switch name {
	case "audio_transcribe":
		return s.Transcribe(ctx, chat.Attachment{Kind: "audio", URL: stringArg(args, "audio_url")})
	case "tts":
		a, err := s.Synthesize(ctx, stringArg(args, "text"))
		if err != nil {
			return nil, err
		}
		path, err := s.saveArtifact(ctx, a)
		if err != nil {
			return nil, err
		}
		return toolkit.Result{Data: map[string]any{"attachment": a}, Files: []string{path}}, nil
	case "image_generate", "image_edit", "image_upscale", "video_generate", "xai_video_edit", "xai_video_extend":
		return s.generate(ctx, name, args)
	case "media_job":
		return s.jobTool(ctx, args)
	case "vision_analyze":
		a := chat.Attachment{Kind: "image", URL: stringArg(args, "image_url")}
		if v := stringArg(args, "video_url"); v != "" {
			a.Kind = "video"
			a.URL = v
		}
		if a.URL == "" {
			return nil, errors.New("vision requires image_url or video_url")
		}
		return s.Analyze(ctx, a, stringArg(args, "prompt"))
	case "media_download":
		p := config.MediaProviderConfig{Enabled: true, Provider: "remote"}
		if err := s.authorize(ctx, name, p); err != nil {
			return nil, err
		}
		kind := stringArg(args, "kind")
		b, mt, err := s.load(ctx, chat.Attachment{Kind: kind, URL: stringArg(args, "url")})
		if err != nil {
			return nil, err
		}
		if mt == "" {
			mt = "application/octet-stream"
		}
		return attachment(kind, strings.Split(mt, ";")[0], b), nil
	case "voice_capture":
		a, err := s.Capture(ctx)
		if err != nil {
			return nil, err
		}
		path, err := s.saveArtifact(ctx, a)
		if err != nil {
			return nil, err
		}
		return toolkit.Result{Data: map[string]any{"attachment": a}, Files: []string{path}}, nil
	case "voice_playback":
		return nil, s.Play(ctx, chat.Attachment{Kind: "audio", URL: stringArg(args, "audio_url")})
	case "wake_word":
		detected, err := s.DetectWake(ctx, chat.Attachment{Kind: "audio", URL: stringArg(args, "audio_url")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"detected": detected, "phrase": s.cfg.Voice.WakeWord}, nil
	case "voice_live":
		return s.CreateLiveSession(ctx, stringArg(args, "sdp"))
	default:
		return nil, errors.New("unknown media tool")
	}
}

// Play 将真实合成音频送到可信配置的播放器，不能在 Docker 模式偷偷调用宿主声卡。
func (s *Service) Play(ctx context.Context, a chat.Attachment) error {
	p := config.MediaProviderConfig{Enabled: s.cfg.Voice.Enabled, Provider: "command"}
	if err := s.authorize(ctx, "voice_playback", p); err != nil {
		return err
	}
	if len(s.cfg.Voice.PlaybackCommand) == 0 {
		return errors.New("playback_command unavailable in execution backend")
	}
	data, _, err := s.load(ctx, a)
	if err != nil {
		return err
	}
	_, err = s.runRawJSONCommand(ctx, "base64 -d | "+shellCommand(s.cfg.Voice.PlaybackCommand)+" && printf '{}' ", encode64(data))
	return err
}
