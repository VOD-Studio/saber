package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

const edgeClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

func edgeGEC(now time.Time) string {
	ticks := (now.Unix() + 11644473600) / 300 * 300 * 10000000
	sum := sha256.Sum256([]byte(fmt.Sprint(ticks) + edgeClientToken))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
func xmlText(text string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
}
func (s *Service) edgeSpeech(ctx context.Context, p config.MediaProviderConfig, text string) (chat.Attachment, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud"
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/edge/v1")
	if err != nil {
		return chat.Attachment{}, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set("TrustedClientToken", edgeClientToken)
	q.Set("ConnectionId", strings.ReplaceAll(uuid.NewString(), "-", ""))
	q.Set("Sec-MS-GEC", edgeGEC(time.Now()))
	version := "1-130.0.2849.68"
	if v, ok := p.Options["gec_version"].(string); ok && v != "" {
		version = v
	}
	q.Set("Sec-MS-GEC-Version", version)
	u.RawQuery = q.Encode()
	cfg, err := websocket.NewConfig(u.String(), "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	if err != nil {
		return chat.Attachment{}, err
	}
	cfg.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0")
	conn, err := cfg.DialContext(ctx)
	if err != nil {
		return chat.Attachment{}, fmt.Errorf("edge TTS handshake failed; check current gec_version and network: %w", err)
	}
	defer func() { _ = conn.Close() }()
	conn.MaxPayloadBytes = 1024 * 1024
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	date := time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
	configMessage := "X-Timestamp:" + date + "\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n{\"context\":{\"synthesis\":{\"audio\":{\"metadataoptions\":{\"sentenceBoundaryEnabled\":false,\"wordBoundaryEnabled\":false},\"outputFormat\":\"audio-24khz-48kbitrate-mono-mp3\"}}}}"
	if err = websocket.Message.Send(conn, configMessage); err != nil {
		return chat.Attachment{}, err
	}
	voice := p.Voice
	if voice == "" {
		voice = "en-US-AriaNeural"
	}
	language := p.Language
	if language == "" {
		language = "en-US"
	}
	rate := "+0%"
	if p.Speed > 0 {
		rate = fmt.Sprintf("%+d%%", int((p.Speed-1)*100))
	}
	ssml := `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="` + xmlText(language) + `"><voice name="` + xmlText(voice) + `"><prosody pitch="+0Hz" rate="` + rate + `" volume="+0%">` + xmlText(text) + `</prosody></voice></speak>`
	message := "X-RequestId:" + strings.ReplaceAll(uuid.NewString(), "-", "") + "\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + date + "\r\nPath:ssml\r\n\r\n" + ssml
	if err = websocket.Message.Send(conn, message); err != nil {
		return chat.Attachment{}, err
	}
	var audio []byte
	for {
		var frame []byte
		if err = websocket.Message.Receive(conn, &frame); err != nil {
			if ctx.Err() != nil {
				return chat.Attachment{}, ctx.Err()
			}
			return chat.Attachment{}, err
		}
		if bytes.Contains(frame, []byte("Path:turn.end")) {
			break
		}
		if len(frame) > 2 {
			n := int(binary.BigEndian.Uint16(frame[:2]))
			if n > 0 && n+2 <= len(frame) && bytes.Contains(frame[2:n+2], []byte("Path:audio")) {
				audio = append(audio, frame[n+2:]...)
				if int64(len(audio)) > s.cfg.MaxBytes {
					return chat.Attachment{}, errors.New("edge audio exceeds max_bytes")
				}
			}
		}
	}
	if len(audio) == 0 {
		return chat.Attachment{}, errors.New("edge returned no audio")
	}
	return attachment("audio", "audio/mpeg", audio), nil
}
