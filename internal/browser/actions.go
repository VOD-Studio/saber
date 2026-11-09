package browser

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
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"

	"rua.plus/saber/internal/toolkit"
)

func (b *browserSession) newTab(ctx context.Context, raw string) (string, error) {
	if err := b.checkURL(ctx, raw); err != nil {
		return "", err
	}
	if b.source.textOnly {
		b.mu.Lock()
		has := b.current != ""
		id := b.current
		b.mu.Unlock()
		if has {
			return id, b.navigate(ctx, raw)
		}
	}
	params := map[string]any{"url": "about:blank"}
	if b.contextID != "" {
		params["browserContextId"] = b.contextID
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := b.cdp.call(ctx, "", "Target.createTarget", params, &created); err != nil {
		return "", err
	}
	if err := b.attachTab(ctx, created.TargetID, true); err != nil {
		return "", err
	}
	if raw != "about:blank" {
		if err := b.navigate(ctx, raw); err != nil {
			return "", err
		}
	}
	return created.TargetID, nil
}
func (b *browserSession) currentSession() (string, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	session := b.tabs[b.current]
	if session == "" {
		return "", "", errors.New("no live tab; use new_tab() to recover")
	}
	return b.current, session, nil
}
func (b *browserSession) navigate(ctx context.Context, raw string) error {
	if err := b.checkURL(ctx, raw); err != nil {
		return err
	}
	_, session, err := b.currentSession()
	if err != nil {
		return err
	}
	var out struct {
		ErrorText string `json:"errorText"`
	}
	if err = b.cdp.call(ctx, session, "Page.navigate", map[string]any{"url": raw}, &out); err != nil {
		return err
	}
	if out.ErrorText != "" {
		return fmt.Errorf("navigation failed: %s", out.ErrorText)
	}
	return b.waitLoad(ctx)
}
func (b *browserSession) evaluate(ctx context.Context, expression string) (any, error) {
	_, session, err := b.currentSession()
	if err != nil {
		return nil, err
	}
	return b.evalSession(ctx, session, expression)
}
func (b *browserSession) evalSession(ctx context.Context, session, expression string) (any, error) {
	var result struct {
		Result struct {
			Value               any    `json:"value"`
			Description         string `json:"description"`
			UnserializableValue string `json:"unserializableValue"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := b.cdp.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true, "userGesture": true}, &result); err != nil {
		return nil, err
	}
	if len(result.ExceptionDetails) > 0 {
		return nil, fmt.Errorf("page JavaScript exception: %s", result.ExceptionDetails)
	}
	if result.Result.UnserializableValue != "" {
		return result.Result.UnserializableValue, nil
	}
	return result.Result.Value, nil
}
func (b *browserSession) waitLoad(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := b.evaluate(ctx, "document.readyState")
		if err != nil {
			return err
		}
		if value == "complete" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func jsLiteral(value any) string { data, _ := json.Marshal(value); return string(data) }
func (b *browserSession) fill(ctx context.Context, selector, text string) error {
	value, err := b.evaluate(ctx, `(()=>{const e=document.querySelector(`+jsLiteral(selector)+`);if(!e)throw Error('selector not found');if(!(e instanceof HTMLInputElement||e instanceof HTMLTextAreaElement||e.isContentEditable))throw Error('not an input');e.focus();if(e.isContentEditable)e.textContent=`+jsLiteral(text)+`;else{const p=e instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;Object.getOwnPropertyDescriptor(p,'value').set.call(e,`+jsLiteral(text)+`)}e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return true})()`)
	if err != nil {
		return err
	}
	if value != true {
		return errors.New("browser fill was not acknowledged")
	}
	return nil
}
func (b *browserSession) click(ctx context.Context, selector string) error {
	value, err := b.evaluate(ctx, `(()=>{const e=document.querySelector(`+jsLiteral(selector)+`);if(!e)throw Error('selector not found');e.scrollIntoView({block:'center'});const r=e.getBoundingClientRect();if(!r.width||!r.height)throw Error('element is not visible');return [r.left+r.width/2,r.top+r.height/2]})()`)
	if err != nil {
		return err
	}
	xy, ok := value.([]any)
	if !ok || len(xy) != 2 {
		return errors.New("browser selector did not resolve coordinates")
	}
	x, xok := xy[0].(float64)
	y, yok := xy[1].(float64)
	if !xok || !yok {
		return errors.New("invalid browser element coordinates")
	}
	return b.clickXY(ctx, x, y)
}
func (b *browserSession) clickXY(ctx context.Context, x, y float64) error {
	_, session, err := b.currentSession()
	if err != nil {
		return err
	}
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": typ, "x": x, "y": y}
		if typ != "mouseMoved" {
			params["button"] = "left"
			params["clickCount"] = 1
		}
		if err = b.cdp.call(ctx, session, "Input.dispatchMouseEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}
func (b *browserSession) pressKey(ctx context.Context, key string) error {
	_, session, err := b.currentSession()
	if err != nil {
		return err
	}
	for _, typ := range []string{"keyDown", "keyUp"} {
		params := map[string]any{"type": typ, "key": key}
		switch key {
		case "Enter":
			params["windowsVirtualKeyCode"] = 13
		case "Tab":
			params["windowsVirtualKeyCode"] = 9
		case "Escape":
			params["windowsVirtualKeyCode"] = 27
		case "Backspace":
			params["windowsVirtualKeyCode"] = 8
		}
		if err = b.cdp.call(ctx, session, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}
func (b *browserSession) screenshot(ctx context.Context) (string, error) {
	if b.source.textOnly {
		return "", errors.New("lightpanda has no graphical renderer; use page_info()/js() instead")
	}
	_, session, err := b.currentSession()
	if err != nil {
		return "", err
	}
	if err = b.maskPrivacy(ctx, session); err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err = b.cdp.call(ctx, session, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &out); err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return "", errors.New("CDP returned invalid screenshot encoding")
	}
	if len(raw) > 16<<20 {
		return "", errors.New("browser screenshot exceeds 16 MiB")
	}
	if _, _, err = image.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return "", errors.New("CDP screenshot is not a valid image")
	}
	return "data:image/png;base64," + out.Data, nil
}
func (b *browserSession) raw(ctx context.Context, method string, params map[string]any, target, frame string) (any, error) {
	if method == "" || !strings.Contains(method, ".") {
		return nil, errors.New("CDP method required")
	}
	if err := b.checkCookieAccess(method); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]any{}
	}
	if method == "Target.getTargets" {
		var out struct {
			TargetInfos []map[string]any `json:"targetInfos"`
		}
		if err := b.cdp.call(ctx, "", method, map[string]any{}, &out); err != nil {
			return nil, err
		}
		b.mu.Lock()
		owned := make([]map[string]any, 0, len(b.tabs))
		for _, info := range out.TargetInfos {
			id, _ := info["targetId"].(string)
			if b.tabs[id] != "" || b.frameSessions[id] != "" {
				owned = append(owned, info)
			}
		}
		b.mu.Unlock()
		return map[string]any{"targetInfos": owned}, nil
	}
	if method == "Target.createTarget" {
		raw, _ := params["url"].(string)
		if raw == "" {
			raw = "about:blank"
		}
		id, err := b.newTab(ctx, raw)
		return map[string]any{"targetId": id}, err
	}
	if method == "Target.closeTarget" {
		id, _ := params["targetId"].(string)
		return map[string]any{"success": true}, b.closeTab(ctx, id)
	}
	if method == "Browser.getVersion" {
		var out any
		err := b.cdp.call(ctx, "", method, map[string]any{}, &out)
		return out, err
	}
	if isStorageCookieMethod(method) {
		copyParams := make(map[string]any, len(params)+1)
		for k, v := range params {
			if k != "browserContextId" {
				copyParams[k] = v
			}
		}
		// checkCookieAccess 已保证这里是隔离 browser context，调用方无法换成共享上下文。
		copyParams["browserContextId"] = b.contextID
		if err := b.checkParams(ctx, copyParams); err != nil {
			return nil, err
		}
		var out any
		err := b.cdp.call(ctx, "", method, copyParams, &out)
		return out, err
	}
	forbidden := []string{"Fetch.", "Target.", "Browser.", "Storage.", "Network.setBypassServiceWorker", "Network.setBlockedURLs", "Network.setRequestInterception", "Network.disable", "Page.setDownloadBehavior", "Page.crash", "Page.close", "Runtime.runIfWaitingForDebugger"}
	for _, prefix := range forbidden {
		if strings.HasPrefix(method, prefix) {
			return nil, fmt.Errorf("CDP ownership/security method %s is managed by Saber; use browser helpers", method)
		}
	}
	if err := b.checkParams(ctx, params); err != nil {
		return nil, err
	}
	if target == "" && frame == "" {
		var err error
		target, _, err = b.currentSession()
		if err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	session := b.tabs[target]
	var contextID int64
	if frame != "" {
		session = b.frameSessions[frame]
		contextID = b.frameContexts[frame]
	}
	b.mu.Unlock()
	if session == "" {
		return nil, errors.New("target/frame does not belong to this browser session")
	}
	if frame != "" && method == "Runtime.evaluate" {
		if contextID == 0 {
			return nil, errors.New("frame has no live default execution context; capture fresh frame state")
		}
		copyParams := make(map[string]any, len(params)+1)
		for k, v := range params {
			copyParams[k] = v
		}
		copyParams["contextId"] = contextID
		params = copyParams
	}
	if method == "Page.captureScreenshot" {
		if b.source.textOnly {
			return nil, errors.New("lightpanda has no graphical renderer")
		}
		if err := b.maskPrivacy(ctx, session); err != nil {
			return nil, err
		}
	}
	var result any
	err := b.cdp.call(ctx, session, method, params, &result)
	return result, err
}

// isStorageCookieMethod 报告方法是否属于 Storage 域的 Cookie 读写。
func isStorageCookieMethod(method string) bool {
	return strings.HasPrefix(method, "Storage.") && strings.HasSuffix(method, "Cookies")
}

// blockedCookieMethod 报告方法能否直接读写浏览器 Cookie 库。
// Network 域的 Cookie 方法作用于共享 profile，即使隔离会话也不放行。
func blockedCookieMethod(method string) bool {
	switch method {
	case "Network.getAllCookies", "Network.getCookies", "Network.deleteCookies", "Network.clearBrowserCookies":
		return true
	}
	return strings.HasPrefix(method, "Network.setCookie")
}

// checkCookieAccess 只允许真正持有隔离 browser context 的会话访问该上下文的 Cookie；
// 否则 cdp()/js() 可以读到共享真实 profile 的全部 Cookie。错误只描述策略，不回显 Cookie 值。
func (b *browserSession) checkCookieAccess(method string) error {
	if blockedCookieMethod(method) || (isStorageCookieMethod(method) && b.contextID == "") {
		return fmt.Errorf("%s denied: cookie access requires an isolated browser context; use an isolated session instead of the shared profile", method)
	}
	return nil
}

func (b *browserSession) checkParams(ctx context.Context, value any) error {
	switch value := value.(type) {
	case map[string]any:
		for k, v := range value {
			if k == "url" || k == "urls" {
				switch v := v.(type) {
				case string:
					if err := b.checkURL(ctx, v); err != nil {
						return err
					}
				case []any:
					for _, raw := range v {
						str, ok := raw.(string)
						if !ok {
							return errors.New("URL must be string")
						}
						if err := b.checkURL(ctx, str); err != nil {
							return err
						}
					}
				}
			}
			if err := b.checkParams(ctx, v); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range value {
			if err := b.checkParams(ctx, v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (b *browserSession) tabList() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := make([]map[string]any, 0, len(b.tabs))
	for id := range b.tabs {
		list = append(list, map[string]any{"target_id": id, "current": id == b.current})
	}
	return list
}
func (b *browserSession) useTab(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tabs[id] == "" {
		return errors.New("tab does not belong to this session")
	}
	b.current = id
	return nil
}
func (b *browserSession) closeTab(ctx context.Context, id string) error {
	b.mu.Lock()
	exists := b.tabs[id] != ""
	b.mu.Unlock()
	if !exists {
		return errors.New("tab does not belong to this session")
	}
	if err := b.cdp.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": id}, nil); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.tabs, id)
	if b.current == id {
		b.current = ""
		for next := range b.tabs {
			b.current = next
			break
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *browserSession) executeCode(ctx context.Context, args map[string]any) (result any, err error) {
	select {
	case b.op <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	locked := b
	touched := []*browserSession{b}
	b.mu.Lock()
	b.last = time.Now()
	b.mu.Unlock()
	defer func() {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
			for _, session := range touched {
				err = errors.Join(err, session.owner.remove(session.key, session))
			}
		}
		if locked != nil {
			<-locked.op
		}
	}()
	route := func(raw string) error {
		if forced, _ := args["local"].(bool); forced {
			return nil
		}
		next, e := b.route(ctx, raw)
		if e != nil {
			return e
		}
		if next == b {
			return nil
		}
		seen := false
		for _, old := range touched {
			if old == next {
				seen = true
				break
			}
		}
		if !seen {
			touched = append(touched, next)
		}
		<-locked.op
		locked = nil
		select {
		case next.op <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		locked = next
		b = next
		b.mu.Lock()
		b.last = time.Now()
		b.mu.Unlock()
		return nil
	}
	var output []any
	var images []string
	if actions, ok := args["actions"].([]any); ok {
		for _, raw := range actions {
			a, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("each browser action must be an object")
			}
			if url, _ := a["url"].(string); url != "" {
				if err := route(url); err != nil {
					return nil, err
				}
			}
			result, image, err := b.action(ctx, a)
			if err != nil {
				return toolkit.Result{Data: b.owner.redactData(output), Images: images, IsError: true}, err
			}
			if result != nil {
				output = append(output, result)
			}
			if image != "" {
				images = append(images, image)
			}
		}
	}
	code, _ := args["code"].(string)
	if code != "" {
		rt := goja.New()
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				rt.Interrupt(ctx.Err())
			case <-done:
			}
		}()
		functions := map[string]func(goja.FunctionCall) goja.Value{}
		bind := func(name string, fn func(goja.FunctionCall) (any, error)) {
			functions[name] = func(call goja.FunctionCall) goja.Value {
				value, err := fn(call)
				if err != nil {
					panic(rt.NewGoError(err))
				}
				return rt.ToValue(value)
			}
		}
		bind("print", func(c goja.FunctionCall) (any, error) {
			for _, v := range c.Arguments {
				output = append(output, v.Export())
			}
			return nil, nil
		})
		bind("new_tab", func(c goja.FunctionCall) (any, error) {
			raw := c.Argument(0).String()
			if err := route(raw); err != nil {
				return nil, err
			}
			return b.newTab(ctx, raw)
		})
		bind("ensure_real_tab", func(goja.FunctionCall) (any, error) {
			if id, _, err := b.currentSession(); err == nil {
				return id, nil
			}
			return b.newTab(ctx, "about:blank")
		})
		bind("goto_url", func(c goja.FunctionCall) (any, error) {
			raw := c.Argument(0).String()
			if err := route(raw); err != nil {
				return nil, err
			}
			return nil, b.navigate(ctx, raw)
		})
		bind("wait_for_load", func(goja.FunctionCall) (any, error) { return nil, b.waitLoad(ctx) })
		bind("js", func(c goja.FunctionCall) (any, error) { return b.evaluate(ctx, c.Argument(0).String()) })
		bind("page_info", func(goja.FunctionCall) (any, error) { return b.pageInfo(ctx) })
		bind("tabs", func(goja.FunctionCall) (any, error) { return b.tabList(), nil })
		bind("use_tab", func(c goja.FunctionCall) (any, error) { return nil, b.useTab(c.Argument(0).String()) })
		bind("close_tab", func(c goja.FunctionCall) (any, error) { return nil, b.closeTab(ctx, c.Argument(0).String()) })
		bind("fill_input", func(c goja.FunctionCall) (any, error) {
			return nil, b.fill(ctx, c.Argument(0).String(), c.Argument(1).String())
		})
		bind("click", func(c goja.FunctionCall) (any, error) { return nil, b.click(ctx, c.Argument(0).String()) })
		bind("click_at_xy", func(c goja.FunctionCall) (any, error) {
			return nil, b.clickXY(ctx, c.Argument(0).ToFloat(), c.Argument(1).ToFloat())
		})
		bind("key", func(c goja.FunctionCall) (any, error) { return nil, b.pressKey(ctx, c.Argument(0).String()) })
		bind("scroll", func(c goja.FunctionCall) (any, error) {
			return b.evaluate(ctx, "window.scrollBy("+jsLiteral(c.Argument(0).ToFloat())+","+jsLiteral(c.Argument(1).ToFloat())+")")
		})
		bind("capture_screenshot", func(goja.FunctionCall) (any, error) {
			image, err := b.screenshot(ctx)
			if err == nil {
				images = append(images, image)
			}
			return map[string]any{"image_index": len(images) - 1}, err
		})
		bind("cdp", func(c goja.FunctionCall) (any, error) {
			params, _ := c.Argument(1).Export().(map[string]any)
			method := c.Argument(0).String()
			data, err := b.raw(ctx, method, params, "", "")
			if err != nil {
				return nil, err
			}
			if method == "Page.captureScreenshot" {
				result, err := b.cdpResult(method, data)
				images = append(images, result.Images...)
				return result.Data, err
			}
			return data, nil
		})
		bind("cookies", func(goja.FunctionCall) (any, error) { return b.raw(ctx, "Storage.getCookies", nil, "", "") })
		for name, fn := range functions {
			if err := rt.Set(name, fn); err != nil {
				return nil, err
			}
		}
		value, err := rt.RunString(code)
		if err != nil {
			return toolkit.Result{Data: b.owner.redactData(output), Images: images, IsError: true}, err
		}
		if value != nil && !goja.IsUndefined(value) {
			output = append(output, value.Export())
		}
	} else if _, ok := args["actions"]; !ok {
		return nil, errors.New("browser_exec requires JavaScript code or an actions array")
	}
	return toolkit.Result{Data: b.owner.redactData(map[string]any{"session": b.label, "output": output, "tabs": b.tabList(), "text_only": b.source.textOnly, "browser_note": b.source.note}), Images: images}, nil
}
func (b *browserSession) pageInfo(ctx context.Context) (any, error) {
	return b.evaluate(ctx, `(()=>({url:location.href,title:document.title,text:document.body?.innerText?.slice(0,60000),inputs:[...document.querySelectorAll('input,textarea,select')].slice(0,100).map(e=>({tag:e.tagName,type:e.type,name:e.name,id:e.id,value:e.type==='password'||e.hasAttribute('data-saber-private')?'[redacted]':e.value})),links:[...document.querySelectorAll('a[href]')].slice(0,200).map(a=>({text:a.innerText,url:a.href}))}))()`)
}
func (b *browserSession) action(ctx context.Context, a map[string]any) (any, string, error) {
	name, _ := a["action"].(string)
	text := func(key string) string { v, _ := a[key].(string); return v }
	switch name {
	case "new_tab":
		id, e := b.newTab(ctx, text("url"))
		return id, "", e
	case "navigate", "goto_url":
		return nil, "", b.navigate(ctx, text("url"))
	case "read", "page_info":
		v, e := b.pageInfo(ctx)
		return v, "", e
	case "evaluate", "js":
		v, e := b.evaluate(ctx, text("expression"))
		return v, "", e
	case "fill", "input":
		return nil, "", b.fill(ctx, text("selector"), text("text"))
	case "click":
		return nil, "", b.click(ctx, text("selector"))
	case "key":
		return nil, "", b.pressKey(ctx, text("key"))
	case "screenshot":
		image, e := b.screenshot(ctx)
		return nil, image, e
	case "wait":
		return nil, "", b.waitLoad(ctx)
	case "tabs":
		return b.tabList(), "", nil
	case "use_tab":
		return nil, "", b.useTab(text("target_id"))
	case "close_tab":
		return nil, "", b.closeTab(ctx, text("target_id"))
	case "cdp":
		params, _ := a["params"].(map[string]any)
		v, e := b.raw(ctx, text("method"), params, text("target_id"), text("frame_id"))
		return v, "", e
	default:
		return nil, "", fmt.Errorf("unknown browser action %q", name)
	}
}

// Origin 返回当前真实页面来源，仅用于保险库在注入前绑定目标。
func (s *Service) Origin(ctx context.Context, label string) (string, error) {
	if err := s.authorize(ctx, "browser_exec"); err != nil {
		return "", err
	}
	key, err := scopeKey(ctx, label)
	if err != nil {
		return "", err
	}
	b := s.selectedSession(key)
	if b == nil {
		return "", errors.New("open a browser tab before using vault")
	}
	select {
	case b.op <- struct{}{}:
		defer func() { <-b.op }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	value, err := b.evaluate(ctx, "location.origin")
	if err != nil {
		return "", err
	}
	origin, ok := value.(string)
	if !ok || origin == "null" {
		return "", errors.New("page has no usable origin")
	}
	return origin, nil
}

// Fill 在真实页面原子复核来源后注入保险库字段，不把秘密放入结果或日志。
func (s *Service) Fill(ctx context.Context, label, origin string, fields map[string]string) (err error) {
	defer func() {
		if err != nil {
			err = redactedError{message: s.redact(err.Error()), cause: err}
		}
	}()
	for _, value := range fields {
		s.remember(value)
	}
	if err := s.authorize(ctx, "browser_exec"); err != nil {
		return err
	}
	key, err := scopeKey(ctx, label)
	if err != nil {
		return err
	}
	b := s.selectedSession(key)
	if b == nil {
		return errors.New("browser session not found")
	}
	select {
	case b.op <- struct{}{}:
		defer func() { <-b.op }()
	case <-ctx.Done():
		return ctx.Err()
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return errors.New("vault origin is invalid")
	}
	_, err = b.evaluate(ctx, `(()=>{if(location.origin!==`+jsLiteral(origin)+`)throw Error('vault origin mismatch');const fields=`+jsLiteral(fields)+`;const selectors={username:'input[autocomplete=username],input[type=email],input[name=username]',password:'input[type=password]',code:'input[autocomplete=one-time-code],input[name=otp],input[name=code]',totp:'input[autocomplete=one-time-code],input[name=otp],input[name=code]',card_number:'input[autocomplete=cc-number]',card_name:'input[autocomplete=cc-name]',card_expiry:'input[autocomplete=cc-exp]',card_cvc:'input[autocomplete=cc-csc]',name:'input[autocomplete=name]',address:'input[autocomplete=street-address]',city:'input[autocomplete=address-level2]',postal_code:'input[autocomplete=postal-code]',country:'input[autocomplete=country]'};const resolved=Object.entries(fields).map(([key,value])=>{const selector=selectors[key]||key;const e=document.querySelector(selector);if(!e)throw Error('vault field not found: '+key);if(!(e instanceof HTMLInputElement||e instanceof HTMLTextAreaElement||e instanceof HTMLSelectElement))throw Error('vault target is not a form field');return [e,value]});for(const[e,value]of resolved){e.setAttribute('data-saber-private','');const p=e instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:e instanceof HTMLSelectElement?HTMLSelectElement.prototype:HTMLInputElement.prototype;Object.getOwnPropertyDescriptor(p,'value').set.call(e,value);e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}))}return {filled:resolved.length}})()`)
	return err
}
