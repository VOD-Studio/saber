package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// Service 维护每位可信调用者的独立驱动、快照令牌与不可重放的输入动作。
type Service struct {
	cfg      config.ComputerConfig
	host     toolkit.Host
	mu       sync.Mutex
	sessions map[string]*computerSession
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
}
type computerSession struct {
	driver   *driver
	op       chan struct{}
	mu       sync.Mutex
	last     time.Time
	pid      int
	window   int
	snapshot string
	tokens   map[int]string
	label    string
}

// New 仅验证配置，实际 MCP 连接必须等待调用身份、任务与权限都已确定。
func New(cfg config.ComputerConfig, host toolkit.Host) (toolkit.Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && (host.Check == nil || host.Workspace == nil || host.StateDir == "") {
		return nil, errors.New("computer_use requires trusted task-bound host")
	}
	s := &Service{cfg: cfg, host: host, sessions: make(map[string]*computerSession), stop: make(chan struct{})}
	if cfg.Enabled {
		s.wg.Add(1)
		go s.janitor()
	}
	return s, nil
}

// Definitions 提供完整背景桌面动作与真实驱动诊断，所有动作都需要执行及外部能力授权。
func (s *Service) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	props := map[string]any{"action": map[string]any{"type": "string", "enum": []string{"capture", "click", "double_click", "right_click", "middle_click", "drag", "scroll", "type", "key", "set_value", "wait", "focus_app", "list_apps", "list_windows", "launch_app", "bring_to_front", "doctor", "list_tools", "call", "close"}}, "session": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"som", "vision", "ax"}}, "app": map[string]any{"type": "string"}, "pid": map[string]any{"type": "integer", "minimum": 1}, "window_id": map[string]any{"type": "integer", "minimum": 1}, "element": map[string]any{"type": "integer", "minimum": 0}, "coordinate": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 2, "maxItems": 2}, "button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}}, "modifiers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "from_element": map[string]any{"type": "integer"}, "to_element": map[string]any{"type": "integer"}, "from_coordinate": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 2, "maxItems": 2}, "to_coordinate": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 2, "maxItems": 2}, "direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}}, "amount": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "text": map[string]any{"type": "string"}, "value": map[string]any{"type": "string"}, "keys": map[string]any{"type": "string"}, "seconds": map[string]any{"type": "number", "minimum": 0, "maximum": 30}, "delivery_mode": map[string]any{"type": "string", "enum": []string{"background", "foreground"}}, "bring_to_front": map[string]any{"type": "boolean"}, "raise_window": map[string]any{"type": "boolean"}, "capture_after": map[string]any{"type": "boolean"}, "tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}, "timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": 1800}}
	return []toolkit.Definition{{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "computer_use", Description: "Control a real desktop via cua-driver MCP. Background is default: no cursor/focus stealing. Capture first, then target elements using the current snapshot; stale snapshots fail closed. Screen capture has no interactable window target. Foreground requires separately configured authorization. call/list_tools expose advertised driver recording/configuration tools. Failed or cancelled input is never replayed; capture fresh state.", Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}}}, Permission: "computer_use", Requires: []string{"exec"}, External: true}}
}
func (s *Service) authorize(ctx context.Context) error {
	if !s.cfg.Enabled {
		return errors.New("computer_use is disabled")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("computer_use requires trusted identity")
	}
	if err := s.host.Check(ctx, "computer_use", true); err != nil {
		return err
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return err
	}
	_, err := s.host.Workspace(ctx)
	return err
}
func computerLabel(ctx context.Context, label string) string {
	if label != "" {
		return label
	}
	if id := task.ID(ctx); id > 0 {
		return fmt.Sprintf("task-%d", id)
	}
	return "default"
}
func sessionKey(ctx context.Context, label string) (string, error) {
	id, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("trusted identity required")
	}
	label = computerLabel(ctx, label)
	if len(label) > 64 || strings.ContainsAny(label, "/\\\x00\r\n") {
		return "", errors.New("invalid computer session label")
	}
	return id.UserKey() + string(id.Session.Key()) + "/" + label, nil
}

// Execute 在任务上下文中真实派发一次输入；连接错误或取消会清除快照并关闭旧传输。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (result any, err error) {
	defer func() {
		if err != nil {
			text := err.Error()
			for _, secret := range s.cfg.Headers {
				if secret != "" {
					text = strings.ReplaceAll(text, secret, "[redacted]")
				}
			}
			if s.cfg.Endpoint != "" {
				text = strings.ReplaceAll(text, s.cfg.Endpoint, "[redacted endpoint]")
			}
			err = protectedError{message: text, cause: err}
		}
	}()
	if name != "computer_use" {
		return nil, fmt.Errorf("unknown computer tool %q", name)
	}
	if err = s.authorize(ctx); err != nil {
		return nil, err
	}
	label, _ := args["session"].(string)
	label = computerLabel(ctx, label)
	key, err := sessionKey(ctx, label)
	if err != nil {
		return nil, err
	}
	action, _ := args["action"].(string)
	if action == "close" {
		s.mu.Lock()
		b := s.sessions[key]
		delete(s.sessions, key)
		s.mu.Unlock()
		if b == nil {
			return map[string]any{"closed": true}, nil
		}
		select {
		case b.op <- struct{}{}:
			defer func() { <-b.op }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[string]any{"closed": true}, b.driver.close()
	}
	timeout := s.cfg.CommandTimeout
	if n, ok := integer(args["timeout_s"]); ok {
		timeout = n
	}
	if timeout < 1 || timeout > 1800 {
		return nil, errors.New("computer timeout_s must be 1..1800")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	b, err := s.get(ctx, key, label)
	if err != nil {
		return nil, err
	}
	select {
	case b.op <- struct{}{}:
		defer func() { <-b.op }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.mu.Lock()
	b.last = time.Now()
	b.mu.Unlock()
	defer func() {
		if ctx.Err() != nil || (err != nil && (strings.Contains(err.Error(), "outcome unknown") || strings.Contains(err.Error(), "session ended") || strings.Contains(err.Error(), "has ended") || errors.Is(err, mcp.ErrConnectionClosed))) {
			b.clear()
			s.mu.Lock()
			if s.sessions[key] == b {
				delete(s.sessions, key)
			}
			s.mu.Unlock()
			err = errors.Join(err, ctx.Err(), b.driver.close())
		}
	}()
	return s.action(ctx, b, action, args)
}
func (s *Service) get(ctx context.Context, key, label string) (*computerSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("computer service closed")
	}
	if b := s.sessions[key]; b != nil {
		return b, nil
	}
	if len(s.sessions) >= s.cfg.MaxSessions {
		return nil, errors.New("computer session limit reached; close an unused session")
	}
	d, err := startDriver(ctx, s.cfg, s.host.StateDir)
	if err != nil {
		return nil, err
	}
	b := &computerSession{driver: d, op: make(chan struct{}, 1), last: time.Now(), tokens: make(map[int]string), label: label}
	s.sessions[key] = b
	return b, nil
}
func (b *computerSession) clear() {
	b.pid = 0
	b.window = 0
	b.snapshot = ""
	b.tokens = make(map[int]string)
}
func (s *Service) janitor() {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			s.mu.Lock()
			var stale []*computerSession
			for key, b := range s.sessions {
				b.mu.Lock()
				idle := time.Since(b.last) > time.Duration(s.cfg.InactivityTimeout)*time.Second
				b.mu.Unlock()
				if idle {
					select {
					case b.op <- struct{}{}:
						stale = append(stale, b)
						delete(s.sessions, key)
					default:
					}
				}
			}
			s.mu.Unlock()
			for _, b := range stale {
				_ = b.driver.close()
				<-b.op
			}
		}
	}
}

// Close 结束所有由本服务创建的驱动生命周期会话和私有运行时。
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	sessions := s.sessions
	s.sessions = make(map[string]*computerSession)
	s.mu.Unlock()
	s.wg.Wait()
	var err error
	for _, b := range sessions {
		b.op <- struct{}{}
		err = errors.Join(err, b.driver.close())
		<-b.op
	}
	return err
}

func integer(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value == float64(int(value)) {
			return int(value), true
		}
	case json.Number:
		n, e := strconv.Atoi(value.String())
		return n, e == nil
	}
	return 0, false
}
func stringArg(args map[string]any, key string) string { v, _ := args[key].(string); return v }
func boolArg(args map[string]any, key string) bool     { v, _ := args[key].(bool); return v }
func coordinate(value any) (float64, float64, error) {
	raw, ok := value.([]any)
	if !ok || len(raw) != 2 {
		return 0, 0, errors.New("coordinate requires two numeric values")
	}
	x, xok := raw[0].(float64)
	y, yok := raw[1].(float64)
	if !xok || !yok {
		return 0, 0, errors.New("coordinate requires numeric values")
	}
	return x, y, nil
}
func outputResult(out *mcp.CallToolResult) (toolkit.Result, error) {
	result := toolkit.Result{}
	if out == nil {
		return result, errors.New("driver returned no result")
	}
	result.IsError = out.IsError
	data := resultObject(out)
	if data != nil {
		copyData := make(map[string]any, len(data))
		for key, value := range data {
			if key != "screenshot_png_b64" && key != "png_b64" {
				copyData[key] = value
			}
		}
		result.Data = copyData
	} else {
		result.Data = resultText(out)
	}
	// addImage 只依据真实图片字节判定 MIME，忽略驱动自报的类型。
	addImage := func(raw []byte) error {
		if len(raw) > 16<<20 {
			return errors.New("computer screenshot exceeds 16 MiB")
		}
		format := ""
		if _, f, e := image.DecodeConfig(bytes.NewReader(raw)); e == nil {
			format = f
		} else {
			decoded, e := base64.StdEncoding.DecodeString(string(raw))
			if e != nil {
				return errors.New("driver returned malformed screenshot")
			}
			_, f, e := image.DecodeConfig(bytes.NewReader(decoded))
			if e != nil {
				return errors.New("driver returned invalid screenshot bytes")
			}
			format = f
			raw = decoded
		}
		var mime string
		switch format {
		case "png":
			mime = "image/png"
		case "jpeg":
			mime = "image/jpeg"
		default:
			return errors.New("unsupported computer screenshot format")
		}
		result.Images = append(result.Images, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(raw))
		return nil
	}
	for _, part := range out.Content {
		if image, ok := part.(*mcp.ImageContent); ok {
			if e := addImage(image.Data); e != nil {
				return result, e
			}
		}
	}
	if len(result.Images) == 0 && data != nil {
		for _, key := range []string{"screenshot_png_b64", "png_b64"} {
			if encoded, ok := data[key].(string); ok && encoded != "" {
				raw, e := base64.StdEncoding.DecodeString(encoded)
				if e != nil {
					return result, e
				}
				if e = addImage(raw); e != nil {
					return result, e
				}
				break
			}
		}
	}
	return result, nil
}
func windows(out *mcp.CallToolResult) []map[string]any {
	data := resultObject(out)
	var rows []any
	if data != nil {
		rows, _ = data["windows"].([]any)
		if rows == nil {
			rows, _ = data["_legacy_windows"].([]any)
		}
	}
	if rows == nil {
		if value, ok := out.StructuredContent.([]any); ok {
			rows = value
		} else {
			_ = json.Unmarshal([]byte(resultText(out)), &rows)
		}
	}
	var result []map[string]any
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		pid, pok := integer(m["pid"])
		window, wok := integer(m["window_id"])
		if !pok || !wok || pid <= 0 || window <= 0 || m["is_on_screen"] == false {
			continue
		}
		m["pid"] = pid
		m["window_id"] = window
		result = append(result, m)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, _ := integer(result[i]["z_index"])
		b, _ := integer(result[j]["z_index"])
		return a > b
	})
	return result
}

func (s *Service) capture(ctx context.Context, b *computerSession, args map[string]any) (any, error) {
	b.clear()
	app := stringArg(args, "app")
	mode := stringArg(args, "mode")
	if mode == "" {
		mode = "som"
	}
	if mode != "som" && mode != "vision" && mode != "ax" {
		return nil, errors.New("capture mode must be som, vision or ax")
	}
	if app == "screen" || app == "fullscreen" || app == "all" {
		out, err := b.driver.captureDesktop(ctx)
		if err != nil {
			return nil, err
		}
		result, err := outputResult(out)
		if err != nil {
			return nil, err
		}
		if len(result.Images) == 0 {
			return nil, errors.New("driver desktop capture has no image; check Screen Recording/display access")
		}
		result.Data = map[string]any{"capture": result.Data, "scope": "desktop", "mode": "vision", "input_target": false, "note": "Full-screen pixels do not establish a window target; capture a specific app or desktop shell before input."}
		return result, nil
	}
	pid, pok := integer(args["pid"])
	window, wok := integer(args["window_id"])
	if pok != wok {
		return nil, errors.New("exact capture requires both pid and window_id")
	}
	if !pok {
		var err error
		pid, window, err = pickWindow(ctx, b, app)
		if err != nil {
			return nil, err
		}
	}
	if pid < 1 || window < 1 {
		return nil, errors.New("capture pid/window_id must be positive")
	}
	out, err := s.captureWindow(ctx, b, pid, window, mode)
	if err != nil {
		return nil, err
	}
	data := resultObject(out)
	result, err := outputResult(out)
	if err != nil {
		return nil, err
	}
	elements, _ := data["elements"].([]any)
	if len(elements) == 0 && len(result.Images) == 0 {
		return nil, errors.New("driver capture returned neither image nor accessibility elements; check real driver health")
	}
	b.pid = pid
	b.window = window
	b.snapshot, _ = data["snapshot_id"].(string)
	for _, raw := range elements {
		if element, ok := raw.(map[string]any); ok {
			index, iok := integer(element["element_index"])
			token, _ := element["element_token"].(string)
			if iok && token != "" {
				b.tokens[index] = token
			}
		}
	}
	resultData, ok := result.Data.(map[string]any)
	if !ok {
		resultData = map[string]any{"content": result.Data}
		result.Data = resultData
	}
	resultData["pid"] = pid
	resultData["window_id"] = window
	resultData["session"] = b.label
	resultData["mode"] = mode
	resultData["coordinate_note"] = "Element frames and coordinate input use the native desktop/window coordinate space; screenshots may be downscaled. Prefer element tokens for reliable targeting."
	if mode == "vision" {
		delete(resultData, "elements")
	}
	if mode == "ax" {
		result.Images = nil
	}
	return result, nil
}

func (s *Service) foreground(ctx context.Context) error {
	if !s.cfg.AllowForeground {
		return errors.New("foreground/focus stealing disabled; configure computer_use.allow_foreground explicitly")
	}
	return s.host.Check(ctx, "computer_use_foreground", true)
}
func (s *Service) action(ctx context.Context, b *computerSession, action string, args map[string]any) (any, error) {
	switch action {
	case "capture":
		return s.capture(ctx, b, args)
	case "wait":
		seconds, ok := args["seconds"].(float64)
		if !ok {
			seconds = 1
		}
		if seconds < 0 || seconds > 30 {
			return nil, errors.New("wait seconds must be 0..30")
		}
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return map[string]any{"waited_seconds": seconds}, nil
		}
	case "list_tools":
		tools := make([]*mcp.Tool, 0, len(b.driver.tools))
		for _, tool := range b.driver.tools {
			tools = append(tools, tool)
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
		return tools, nil
	case "list_apps", "list_windows", "doctor":
		name := action
		params := map[string]any{}
		if action == "list_windows" {
			params["on_screen_only"] = true
		}
		if action == "doctor" {
			name = "health_report"
		}
		out, err := b.driver.call(ctx, name, params)
		if err != nil {
			return nil, err
		}
		return outputResult(out)
	case "launch_app":
		params, _ := args["arguments"].(map[string]any)
		if params == nil {
			params = map[string]any{"name": stringArg(args, "app")}
		}
		out, err := b.driver.call(ctx, "launch_app", params)
		if err != nil {
			return nil, err
		}
		return outputResult(out)
	case "focus_app":
		result, err := s.capture(ctx, b, args)
		if err != nil {
			return nil, err
		}
		if boolArg(args, "raise_window") {
			if err = s.foreground(ctx); err != nil {
				return nil, err
			}
			if _, err = b.driver.call(ctx, "bring_to_front", map[string]any{"pid": b.pid, "window_id": b.window}); err != nil {
				return nil, err
			}
		}
		return result, nil
	case "call":
		name := stringArg(args, "tool")
		if name == "start_session" || name == "end_session" {
			return nil, errors.New("driver lifecycle is owned by Saber")
		}
		params, _ := args["arguments"].(map[string]any)
		if name == "bring_to_front" || stringArg(params, "delivery_mode") == "foreground" {
			if err := s.foreground(ctx); err != nil {
				return nil, err
			}
		}
		out, err := b.driver.call(ctx, name, params)
		if err != nil {
			return nil, err
		}
		b.clear()
		return outputResult(out)
	case "bring_to_front":
		if err := s.foreground(ctx); err != nil {
			return nil, err
		}
		if b.pid == 0 || b.window == 0 {
			return nil, errors.New("capture an exact window before bring_to_front")
		}
		out, err := b.driver.call(ctx, "bring_to_front", map[string]any{"pid": b.pid, "window_id": b.window})
		if err != nil {
			return nil, err
		}
		return outputResult(out)
	}
	if b.pid == 0 || b.window == 0 {
		return nil, errors.New("no active window; capture first (full-screen captures do not establish an input target)")
	}
	params := map[string]any{"pid": b.pid, "window_id": b.window}
	tool := action
	addElement := func(key string) error {
		index, ok := integer(args[key])
		if !ok || index < 0 {
			return errors.New("element requires a nonnegative integer from the latest capture")
		}
		token := b.tokens[index]
		if token == "" {
			return errors.New("element has no token in the current snapshot; capture fresh state before input")
		}
		params["element_index"] = index
		if b.driver.property(tool, "element_token") {
			params["element_token"] = token
		}
		if b.snapshot != "" && b.driver.property(tool, "snapshot_id") {
			params["snapshot_id"] = b.snapshot
		}
		if !b.driver.property(tool, "element_token") && !b.driver.property(tool, "snapshot_id") {
			return errors.New("live driver cannot validate snapshot tokens; update driver before element input")
		}
		return nil
	}
	switch action {
	case "click", "double_click", "right_click", "middle_click":
		tool = "click"
		if action == "double_click" {
			tool = "double_click"
		}
		button := stringArg(args, "button")
		if button == "" {
			button = "left"
		}
		if action == "right_click" {
			button = "right"
		}
		if action == "middle_click" {
			button = "middle"
		}
		if button != "left" && button != "right" && button != "middle" {
			return nil, errors.New("button must be left, right or middle")
		}
		params["button"] = button
		if _, ok := args["element"]; ok {
			if err := addElement("element"); err != nil {
				return nil, err
			}
		} else {
			x, y, err := coordinate(args["coordinate"])
			if err != nil {
				return nil, err
			}
			params["x"] = x
			params["y"] = y
		}
		if modifiers, ok := args["modifiers"]; ok {
			params["modifier"] = modifiers
		}
	case "drag":
		if from, ok := integer(args["from_element"]); ok {
			to, tok := integer(args["to_element"])
			if !tok {
				return nil, errors.New("drag requires both from_element and to_element")
			}
			if b.tokens[from] == "" || b.tokens[to] == "" {
				return nil, errors.New("drag elements not present in latest snapshot")
			}
			if !b.driver.property(tool, "snapshot_id") || b.snapshot == "" {
				return nil, errors.New("element drag requires a driver-advertised snapshot_id contract; use coordinates or update driver")
			}
			params["from_element"] = from
			params["to_element"] = to
			params["snapshot_id"] = b.snapshot
		} else {
			x, y, e := coordinate(args["from_coordinate"])
			if e != nil {
				return nil, e
			}
			tx, ty, e := coordinate(args["to_coordinate"])
			if e != nil {
				return nil, e
			}
			params["from_x"] = x
			params["from_y"] = y
			params["to_x"] = tx
			params["to_y"] = ty
		}
	case "scroll":
		direction := stringArg(args, "direction")
		if direction != "up" && direction != "down" && direction != "left" && direction != "right" {
			return nil, errors.New("scroll direction required")
		}
		amount, ok := integer(args["amount"])
		if !ok {
			amount = 3
		}
		if amount < 1 || amount > 50 {
			return nil, errors.New("scroll amount must be 1..50")
		}
		params["direction"] = direction
		params["amount"] = amount
		if _, ok := args["element"]; ok {
			if e := addElement("element"); e != nil {
				return nil, e
			}
		} else if xy, ok := args["coordinate"]; ok {
			if !b.driver.property(tool, "x") || !b.driver.property(tool, "y") {
				return nil, errors.New("live driver does not support coordinate scrolling")
			}
			x, y, e := coordinate(xy)
			if e != nil {
				return nil, e
			}
			params["x"] = x
			params["y"] = y
		}
	case "type":
		tool = "type_text"
		params["text"] = stringArg(args, "text")
	case "key":
		keys := stringArg(args, "keys")
		parts := strings.FieldsFunc(keys, func(r rune) bool { return r == '+' || r == '-' })
		if len(parts) == 0 {
			return nil, errors.New("key combination required")
		}
		if len(parts) > 1 {
			tool = "hotkey"
			params["keys"] = parts
		} else {
			tool = "press_key"
			params["key"] = parts[0]
		}
	case "set_value":
		if e := addElement("element"); e != nil {
			return nil, e
		}
		params["value"] = stringArg(args, "value")
	default:
		return nil, fmt.Errorf("unknown computer action %q", action)
	}
	if delivery := stringArg(args, "delivery_mode"); delivery != "" && delivery != "background" {
		if delivery != "foreground" {
			return nil, errors.New("delivery_mode must be background or foreground")
		}
		if err := s.foreground(ctx); err != nil {
			return nil, err
		}
		if !b.driver.property(tool, "delivery_mode") {
			return nil, errors.New("connected driver does not advertise foreground input")
		}
		params["delivery_mode"] = delivery
	}
	if boolArg(args, "bring_to_front") {
		if stringArg(args, "delivery_mode") != "foreground" {
			return nil, errors.New("bring_to_front requires foreground delivery")
		}
		if err := s.foreground(ctx); err != nil {
			return nil, err
		}
		if _, err := b.driver.call(ctx, "bring_to_front", map[string]any{"pid": b.pid, "window_id": b.window}); err != nil {
			return nil, err
		}
	}
	out, err := b.driver.call(ctx, tool, params)
	if err != nil {
		b.clear()
		return nil, err
	}
	result, err := outputResult(out)
	if err != nil {
		return nil, err
	}
	if boolArg(args, "capture_after") {
		pid, window := b.pid, b.window
		capture, e := s.capture(ctx, b, map[string]any{"pid": pid, "window_id": window})
		if e != nil {
			return result, e
		}
		if after, ok := capture.(toolkit.Result); ok {
			result.Data = map[string]any{"action": result.Data, "capture": after.Data}
			result.Images = append(result.Images, after.Images...)
		}
	}
	return result, nil
}
