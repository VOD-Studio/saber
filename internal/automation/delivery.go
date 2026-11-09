package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// DeliveryRequest 判断是否是投递任务标记；标记本身永远不授予权限。
func DeliveryRequest(req agent.Request) bool {
	for _, m := range req.Messages {
		if m.Role == "system" && m.Name == "saber_cron_delivery" {
			return true
		}
	}
	return false
}

// RunDelivery 在普通持久任务内执行固定投递工具；任务中断后的 unknown send 不自动重试。
func (s *Service) RunDelivery(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	if task.ID(ctx) <= 0 || !DeliveryRequest(req) {
		return agent.Result{}, errors.New("cron delivery requires persistent delivery task")
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return agent.Result{}, errors.New("cron delivery requires trusted identity")
	}
	var route struct {
		ScheduleID int64 `json:"schedule_id"`
		SourceTask int64 `json:"source_task_id"`
	}
	found := false
	for _, message := range req.Messages {
		if message.Role == "system" && message.Name == "saber_cron_delivery" {
			if err := json.Unmarshal([]byte(message.Content), &route); err != nil {
				return agent.Result{}, err
			}
			found = true
			break
		}
	}
	if !found {
		return agent.Result{}, errors.New("cron delivery marker missing")
	}
	s.mu.Lock()
	manager := s.tasks
	meta, err := s.cronMeta(ctx, route.ScheduleID)
	s.mu.Unlock()
	if err != nil {
		return agent.Result{}, err
	}
	if manager == nil {
		return agent.Result{}, errors.New("cron delivery task manager unavailable")
	}
	t, err := manager.Get(ctx, i.Session, task.ID(ctx))
	if err != nil {
		return agent.Result{}, err
	}
	if meta.Owner != i || meta.DeliveryTask != t.ID || meta.DeliveredTask != route.SourceTask || t.EventKey != meta.DeliveryEvent {
		return agent.Result{}, errors.New("cron delivery metadata does not authorize current task")
	}
	if meta.DeliveryState != "prepared" {
		return agent.Result{}, errors.New("cron delivery already claimed; refusing replay")
	}
	if err = s.checkDestination(ctx, i, meta.Delivery); err != nil {
		return agent.Result{}, err
	}
	source, err := manager.Get(ctx, i.Session, route.SourceTask)
	if err != nil {
		return agent.Result{}, err
	}
	if source.Message.SenderID != i.SenderID {
		return agent.Result{}, errors.New("cron result owner mismatch")
	}
	meta.DeliveryState = "unknown"
	if err = s.saveCron(ctx, meta); err != nil {
		return agent.Result{}, err
	}
	start := time.Now()
	if emit != nil {
		emit(agent.Event{Kind: agent.RunStarted})
	}
	destination := s.cfg.Destinations[meta.Delivery]
	args := map[string]any{}
	for k, v := range destination.Args {
		args[k] = v
	}
	field := destination.ContentField
	if field == "" {
		field = "text"
	}
	args[field] = source.Result.Content
	if source.Error != "" {
		args[field] = source.Error
	}
	if destination.IdempotencyField != "" {
		args[destination.IdempotencyField] = fmt.Sprintf("saber-cron:%d:task:%d", meta.ID, source.ID)
	}
	value, callErr := s.host.Call(ctx, destination.Tool, args)
	if callErr == nil {
		callErr = reportedToolError(value)
	}
	status := agent.Completed
	if callErr != nil {
		status = agent.Failed
		meta.DeliveryError = callErr.Error()
	} else {
		meta.DeliveryState = "sent"
		meta.DeliveryError = ""
	}
	if errors.Is(callErr, context.Canceled) {
		status = agent.Cancelled
	}
	saveErr := s.saveCron(context.WithoutCancel(ctx), meta)
	encoded, encodeErr := json.Marshal(value)
	result := agent.Result{Status: status, Content: string(encoded), Duration: time.Since(start)}
	finalErr := errors.Join(callErr, saveErr, encodeErr)
	if emit != nil {
		emit(agent.Event{Kind: agent.RunFinished, Text: result.Content, Status: status, Error: finalErr})
	}
	return result, finalErr
}
