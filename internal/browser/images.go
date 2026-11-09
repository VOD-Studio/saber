package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"

	"rua.plus/saber/internal/toolkit"
)

func cdpScreenshotImage(data any) (string, error) {
	payload, ok := data.(map[string]any)
	if !ok {
		return "", errors.New("CDP screenshot result is not an object")
	}
	encoded, _ := payload["data"].(string)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("CDP screenshot encoding invalid")
	}
	if len(raw) > 16<<20 {
		return "", errors.New("CDP screenshot exceeds 16 MiB")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", errors.New("CDP screenshot is not a valid image")
	}
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
	} else if format != "png" {
		return "", errors.New("unsupported CDP screenshot format")
	}
	return "data:" + mime + ";base64," + encoded, nil
}
func (b *browserSession) cdpResult(method string, data any) (toolkit.Result, error) {
	if method != "Page.captureScreenshot" {
		result := toolkit.Result{Data: b.owner.redactData(data)}
		if b.source.note != "" {
			if payload, ok := result.Data.(map[string]any); ok {
				payload["browser_note"] = b.owner.redact(b.source.note)
			} else {
				result.Data = map[string]any{"content": result.Data, "browser_note": b.owner.redact(b.source.note)}
			}
		}
		return result, nil
	}
	image, err := cdpScreenshotImage(data)
	if err != nil {
		return toolkit.Result{}, err
	}
	return toolkit.Result{Data: map[string]any{"screenshot": true, "browser_note": b.owner.redact(b.source.note)}, Images: []string{image}}, nil
}
func (b *browserSession) maskPrivacy(ctx context.Context, session string) error {
	_, err := b.evalSession(ctx, session, `(()=>{let s=document.getElementById('__saber_private_mask');if(!s){s=document.createElement('style');s.id='__saber_private_mask';document.documentElement.append(s)}s.textContent='input[type=password], [data-saber-private] {color:transparent!important;text-shadow:none!important;filter:blur(12px)!important}';return true})()`)
	return err
}
