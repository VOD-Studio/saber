package media

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func (s *Service) localTranscribe(ctx context.Context, p config.MediaProviderConfig, data []byte, mt string) (map[string]any, error) {
	if p.Model == "" {
		return nil, errors.New("whisper_cpp requires model path in media_tools.asr.model and whisper-cli in execution backend")
	}
	argv := p.Command
	if len(argv) == 0 {
		argv = []string{"whisper-cli"}
	}
	language := p.Language
	if language == "" {
		language = "auto"
	}
	ffmpeg := s.cfg.FFmpegCommand
	command := "d=$(mktemp -d) && trap 'rm -rf \"$d\"' EXIT && base64 -d > \"$d/source\" && "
	if mt == "audio/wav" {
		command += "cp \"$d/source\" \"$d/input.wav\" && "
	} else {
		command += shellQuote(ffmpeg) + " -v error -i \"$d/source\" -ar 16000 -ac 1 \"$d/input.wav\" && "
	}
	command += shellCommand(argv) + " -m " + shellQuote(p.Model) + " -f \"$d/input.wav\" -l " + shellQuote(language) + " -otxt -of \"$d/result\" -np > /dev/null && printf '{\"text_base64\":\"' && base64 < \"$d/result.txt\" | tr -d '\\r\\n' && printf '\"}'"
	r, err := s.runRawJSONCommand(ctx, command, base64.StdEncoding.EncodeToString(data))
	if err != nil {
		return nil, err
	}
	text, err := base64.StdEncoding.DecodeString(str(r, "text_base64"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": strings.TrimSpace(string(text)), "language": language}, nil
}
func (s *Service) localSpeech(ctx context.Context, p config.MediaProviderConfig, text string) (chat.Attachment, error) {
	argv := p.Command
	var command string
	switch p.Provider {
	case "piper":
		if p.Model == "" {
			return chat.Attachment{}, errors.New("piper requires .onnx model path in media_tools.tts.model and matching .onnx.json config")
		}
		if len(argv) == 0 {
			argv = []string{"piper"}
		}
		command = "d=$(mktemp -d) && trap 'rm -rf \"$d\"' EXIT && " + shellCommand(argv) + " --model " + shellQuote(p.Model) + " --output_file \"$d/output.wav\" > /dev/null && printf '{\"data\":\"' && base64 < \"$d/output.wav\" | tr -d '\\r\\n' && printf '\",\"mime_type\":\"audio/wav\"}'"
	case "espeak":
		if len(argv) == 0 {
			argv = []string{"espeak-ng"}
		}
		voice := p.Voice
		if voice == "" {
			voice = "en"
		}
		command = "printf '{\"data\":\"' && " + shellCommand(argv) + " --stdout --stdin -v " + shellQuote(voice) + " | base64 | tr -d '\\r\\n' && printf '\",\"mime_type\":\"audio/wav\"}'"
	default:
		return chat.Attachment{}, errors.New("unknown native local TTS")
	}
	r, err := s.runRawJSONCommand(ctx, command, text+"\n")
	if err != nil {
		return chat.Attachment{}, err
	}
	return s.decodeAudio(ctx, r)
}
