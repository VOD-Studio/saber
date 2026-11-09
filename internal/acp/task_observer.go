package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/server"
)

func (c *chatClient) observeTask(ctx context.Context, s *session, t server.Turn, emit func(agent.Event) error, watch func(context.Context, int64) error) (out server.Turn, runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out = t
	watchDone := make(chan error, 1)
	go func() {
		err := watch(ctx, t.ID)
		watchDone <- err
		if err != nil {
			cancel()
		}
	}()
	defer func() {
		cancel()
		err := <-watchDone
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = err
		}
		if ctx.Err() != nil && (out.Status == "running" || out.Status == "queued") {
			if err := c.stopTask(s.id, t.ID); err != nil {
				runErr = err
			}
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/v1/runs/"+strconv.FormatInt(t.ID, 10)+"/events?session="+url.QueryEscape(s.id), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return out, readUpstreamError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	kind := ""
	var data []string
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		data = nil
		switch kind {
		case "task":
			return json.Unmarshal(raw, &out)
		case "event":
			var event agent.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				return err
			}
			return emit(event)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return out, err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			kind = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if err := consume(); err != nil {
		return out, err
	}
	if out.Status == "running" || out.Status == "queued" {
		return out, fmt.Errorf("任务事件流在终态前结束")
	}
	if out.Error != "" {
		return out, fmt.Errorf("任务失败: %s", out.Error)
	}
	return out, nil
}
