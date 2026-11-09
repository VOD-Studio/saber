package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (b *browserSession) eventLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case msg := <-b.events:
			b.event(msg)
		case <-ticker.C:
			b.mu.Lock()
			var expired []dialog
			for id, d := range b.dialogs {
				if time.Since(d.opened) > time.Duration(b.owner.cfg.DialogTimeout)*time.Second {
					expired = append(expired, d)
					delete(b.dialogs, id)
				}
			}
			b.mu.Unlock()
			for _, d := range expired {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := b.respondDialog(ctx, d, false, "")
				cancel()
				if err != nil {
					b.cdp.fail(err)
				}
			}
		}
	}
}
func (b *browserSession) event(msg cdpMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch msg.Method {
	case "Fetch.requestPaused":
		var p struct {
			RequestID string `json:"requestId"`
			Request   struct {
				URL      string `json:"url"`
				PostData string `json:"postData"`
			} `json:"request"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			b.cdp.fail(err)
			return
		}
		if p.Request.URL == cloudDialogEndpoint && b.owner.cfg.Provider == "browserbase" {
			if err := b.receiveCloudDialog(ctx, msg, p.RequestID, p.Request.PostData); err != nil {
				b.cdp.fail(err)
			}
			return
		}
		err := b.checkURL(ctx, p.Request.URL)
		if strings.HasPrefix(p.Request.URL, "data:") || strings.HasPrefix(p.Request.URL, "blob:") {
			err = nil
		}
		method := "Fetch.continueRequest"
		params := map[string]any{"requestId": p.RequestID}
		if err != nil {
			method = "Fetch.failRequest"
			params["errorReason"] = "BlockedByClient"
			b.mu.Lock()
			b.blocked = append(b.blocked, b.owner.redact(p.Request.URL)+": "+err.Error())
			if len(b.blocked) > 100 {
				b.blocked = b.blocked[len(b.blocked)-100:]
			}
			b.mu.Unlock()
		}
		if e := b.cdp.call(ctx, msg.SessionID, method, params, nil); e != nil {
			b.cdp.fail(e)
		}
	case "Target.attachedToTarget":
		var p struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				TargetID         string `json:"targetId"`
				Type             string `json:"type"`
				BrowserContextID string `json:"browserContextId"`
			} `json:"targetInfo"`
		}
		if e := json.Unmarshal(msg.Params, &p); e != nil {
			b.cdp.fail(e)
			return
		}
		b.mu.Lock()
		parentOwned := false
		ownsTabs := false
		for target, session := range b.tabs {
			if session == msg.SessionID {
				parentOwned = true
				ownsTabs = b.ownedTabs[target]
			}
		}
		for _, session := range b.frameSessions {
			if session == msg.SessionID {
				parentOwned = true
			}
		}
		if parentOwned {
			switch p.TargetInfo.Type {
			case "iframe":
				b.frameSessions[p.TargetInfo.TargetID] = p.SessionID
			case "page":
				b.tabs[p.TargetInfo.TargetID] = p.SessionID
				b.ownedTabs[p.TargetInfo.TargetID] = ownsTabs
			}
		}
		b.mu.Unlock()
		if !parentOwned {
			b.cdp.fail(errors.New("unexpected CDP attachment outside owned browser session"))
			return
		}
		for _, domain := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
			if e := b.cdp.call(ctx, p.SessionID, domain, map[string]any{}, nil); e != nil {
				b.cdp.fail(e)
				return
			}
		}
		if e := b.cdp.call(ctx, p.SessionID, "Fetch.enable", map[string]any{"patterns": []any{map[string]any{"urlPattern": "*", "requestStage": "Request"}}}, nil); e != nil {
			b.cdp.fail(e)
			return
		}
		if e := b.cdp.call(ctx, p.SessionID, "Network.setBypassServiceWorker", map[string]any{"bypass": true}, nil); e != nil {
			b.cdp.fail(e)
			return
		}
		if e := b.installCloudDialogs(ctx, p.SessionID); e != nil {
			b.cdp.fail(e)
			return
		}
		if e := b.cdp.call(ctx, p.SessionID, "Runtime.runIfWaitingForDebugger", map[string]any{}, nil); e != nil {
			b.cdp.fail(e)
		}
	case "Runtime.executionContextCreated":
		var p struct {
			Context struct {
				ID      int64 `json:"id"`
				AuxData struct {
					FrameID   string `json:"frameId"`
					IsDefault bool   `json:"isDefault"`
				} `json:"auxData"`
			} `json:"context"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.Context.AuxData.IsDefault && p.Context.AuxData.FrameID != "" {
			b.mu.Lock()
			b.frameSessions[p.Context.AuxData.FrameID] = msg.SessionID
			b.frameContexts[p.Context.AuxData.FrameID] = p.Context.ID
			b.mu.Unlock()
		}
	case "Runtime.executionContextsCleared":
		b.mu.Lock()
		for frame, session := range b.frameSessions {
			if session == msg.SessionID {
				delete(b.frameContexts, frame)
			}
		}
		b.mu.Unlock()
	case "Page.javascriptDialogOpening":
		var p struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			URL     string `json:"url"`
		}
		if e := json.Unmarshal(msg.Params, &p); e != nil {
			b.cdp.fail(e)
			return
		}
		id := fmt.Sprintf("dialog-%d", time.Now().UnixNano())
		b.mu.Lock()
		target := ""
		for t, session := range b.tabs {
			if session == msg.SessionID {
				target = t
				break
			}
		}
		d := dialog{ID: id, Target: target, Type: p.Type, Message: p.Message, URL: p.URL, opened: time.Now(), session: msg.SessionID}
		b.dialogs[id] = d
		b.mu.Unlock()
		if b.owner.cfg.DialogPolicy != "must_respond" {
			accept := b.owner.cfg.DialogPolicy == "auto_accept"
			if e := b.cdp.call(ctx, msg.SessionID, "Page.handleJavaScriptDialog", map[string]any{"accept": accept}, nil); e != nil {
				b.cdp.fail(e)
				return
			}
			b.mu.Lock()
			delete(b.dialogs, id)
			b.mu.Unlock()
		}
	case "Page.javascriptDialogClosed":
		b.mu.Lock()
		for id, d := range b.dialogs {
			if d.session == msg.SessionID {
				delete(b.dialogs, id)
			}
		}
		b.mu.Unlock()
	case "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			b.mu.Lock()
			for id, session := range b.tabs {
				if session == p.SessionID {
					delete(b.tabs, id)
					if b.current == id {
						b.current = ""
					}
				}
			}
			for id, session := range b.frameSessions {
				if session == p.SessionID {
					delete(b.frameSessions, id)
					delete(b.frameContexts, id)
				}
			}
			b.mu.Unlock()
		}
	}
}
func (b *browserSession) dialogCommand(ctx context.Context, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	id, _ := args["dialog_id"].(string)
	b.mu.Lock()
	if action == "list" {
		list := make([]dialog, 0, len(b.dialogs))
		for _, d := range b.dialogs {
			list = append(list, d)
		}
		b.mu.Unlock()
		return b.owner.redactData(list), nil
	}
	if id == "" && len(b.dialogs) == 1 {
		for only := range b.dialogs {
			id = only
		}
	}
	d, ok := b.dialogs[id]
	b.mu.Unlock()
	if !ok {
		return nil, errors.New("no matching dialog; list dialogs and specify dialog_id when several are pending")
	}
	if action != "accept" && action != "dismiss" {
		return nil, errors.New("browser_dialog action must be list, accept or dismiss")
	}
	text, _ := args["prompt_text"].(string)
	if err := b.respondDialog(ctx, d, action == "accept", text); err != nil {
		return nil, err
	}
	b.mu.Lock()
	delete(b.dialogs, id)
	b.last = time.Now()
	b.mu.Unlock()
	return map[string]any{"dialog_id": id, "handled": true, "action": action}, nil
}
