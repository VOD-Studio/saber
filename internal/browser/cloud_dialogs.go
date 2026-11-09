package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

const cloudDialogEndpoint = "https://saber-dialog.invalid/response"

func (b *browserSession) installCloudDialogs(ctx context.Context, session string) error {
	if b.owner.cfg.Provider != "browserbase" {
		return nil
	}
	script := `(()=>{window.__saberDialogOriginals??={alert:window.alert,confirm:window.confirm,prompt:window.prompt};const endpoint=` + jsLiteral(cloudDialogEndpoint) + `;function ask(type,message,defaultValue){const xhr=new XMLHttpRequest();xhr.open('POST',endpoint,false);xhr.setRequestHeader('Content-Type','text/plain');xhr.send(JSON.stringify({type,message:String(message??''),defaultValue:String(defaultValue??''),url:location.href}));if(xhr.status!==200)throw Error('browser dialog supervisor unavailable');return JSON.parse(xhr.responseText).value}window.alert=m=>{ask('alert',m,'')};window.confirm=m=>!!ask('confirm',m,'');window.prompt=(m,d)=>ask('prompt',m,d)})()`
	var out struct {
		Identifier string `json:"identifier"`
	}
	if err := b.cdp.call(ctx, session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": script}, &out); err != nil {
		return err
	}
	b.mu.Lock()
	b.scripts[session] = out.Identifier
	b.mu.Unlock()
	return nil
}
func (b *browserSession) receiveCloudDialog(ctx context.Context, msg cdpMessage, requestID, postData string) error {
	var p struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal([]byte(postData), &p); err != nil {
		return err
	}
	if p.Type != "alert" && p.Type != "confirm" && p.Type != "prompt" {
		return fmt.Errorf("invalid supervised browser dialog type %q", p.Type)
	}
	b.mu.Lock()
	target := ""
	for t, session := range b.tabs {
		if session == msg.SessionID {
			target = t
			break
		}
	}
	id := "cloud-dialog-" + requestID
	b.dialogs[id] = dialog{ID: id, Target: target, Type: p.Type, Message: p.Message, URL: p.URL, opened: time.Now(), session: msg.SessionID, request: requestID}
	d := b.dialogs[id]
	b.mu.Unlock()
	if b.owner.cfg.DialogPolicy != "must_respond" {
		if err := b.respondDialog(ctx, d, b.owner.cfg.DialogPolicy == "auto_accept", ""); err != nil {
			return err
		}
		b.mu.Lock()
		delete(b.dialogs, id)
		b.mu.Unlock()
	}
	return nil
}
func (b *browserSession) respondDialog(ctx context.Context, d dialog, accept bool, prompt string) error {
	if d.request == "" {
		return b.cdp.call(ctx, d.session, "Page.handleJavaScriptDialog", map[string]any{"accept": accept, "promptText": prompt}, nil)
	}
	var value any
	switch d.Type {
	case "confirm":
		value = accept
	case "prompt":
		if accept {
			value = prompt
		}
	case "alert":
		value = nil
	}
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		return err
	}
	return b.cdp.call(ctx, d.session, "Fetch.fulfillRequest", map[string]any{"requestId": d.request, "responseCode": 200, "responseHeaders": []any{map[string]any{"name": "Content-Type", "value": "application/json"}, map[string]any{"name": "Access-Control-Allow-Origin", "value": "*"}}, "body": base64.StdEncoding.EncodeToString(body)}, nil)
}
