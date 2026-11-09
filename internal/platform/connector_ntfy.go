package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"rua.plus/saber/internal/chat"
)

func (p *connector) sendNtfyMedia(ctx context.Context, reply chat.Reply, upload uploadedMedia) (string, error) {
	if len(upload.Data) == 0 {
		return "", errors.New("ntfy 媒体为空")
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", strings.TrimRight(p.cfg.Endpoint, "/")+"/"+url.PathEscape(reply.Session.Conversation), bytes.NewReader(upload.Data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", upload.MIME)
	req.Header.Set("Filename", upload.Name)
	req.Header.Set("Message", reply.Text)
	if err = p.authorizeRequest(ctx, req); err != nil {
		return "", err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("ntfy 附件发布 HTTP %d", response.StatusCode)
	}
	var out map[string]any
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	id := str(out["id"])
	if id == "" {
		return "", errors.New("ntfy 附件发布未返回 ID")
	}
	return id, nil
}
