package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// ProcessBarrier 绑定真实 process handle 和源/目标持久任务；从不信任宿主 PID。
type ProcessBarrier struct {
	ID         string `json:"id"`
	SourceTask int64  `json:"source_task_id"`
	TargetTask int64  `json:"target_task_id"`
}

func (s *Service) processStatus(ctx context.Context, id string) (map[string]any, error) {
	if s.host.Call == nil {
		return nil, errors.New("process dispatcher unavailable")
	}
	value, err := s.host.Call(ctx, "process", map[string]any{"action": "status", "id": id})
	if err != nil {
		return nil, err
	}
	object, err := toolObject(value)
	if err != nil {
		return nil, err
	}
	if data, ok := object["data"].(map[string]any); ok {
		return data, nil
	}
	return object, nil
}
func (s *Service) liveProcesses(ctx context.Context) (any, error) {
	if s.host.Call == nil {
		return nil, errors.New("process dispatcher unavailable")
	}
	value, err := s.host.Call(ctx, "process", map[string]any{"action": "list"})
	if err != nil {
		return nil, err
	}
	object, err := toolObject(value)
	if err != nil {
		return nil, err
	}
	if data, ok := object["data"].(map[string]any); ok {
		return data["processes"], nil
	}
	return object["processes"], nil
}

func (s *Service) parkProcess(ctx context.Context, st *State) error {
	barrier := st.WaitingProcess
	if barrier == nil || barrier.TargetTask != 0 {
		return nil
	}
	if task.ID(ctx) != barrier.SourceTask {
		return errors.New("process barrier can only be prepared by its live source task")
	}
	status, err := s.processStatus(ctx, barrier.ID)
	if err != nil {
		return err
	}
	if status["status"] != "running" {
		st.WaitingProcess = nil
		return nil
	}
	msg := st.Message
	msg.ID = fmt.Sprintf("saber-automation:%s:process:%d:%s", st.ID, barrier.SourceTask, barrier.ID)
	msg.ReplyTo = ""
	msg.ControlText = nil
	msg.Attachments = nil
	msg.Text = "Wait for the authorized background process " + barrier.ID + " and then continue toward the standing goal: " + st.Prompt
	req, err := s.bindings.Prepare(ctx, msg, st.Skills, st.Model)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(barrier)
	if err != nil {
		return err
	}
	req.Messages = append([]openai.ChatCompletionMessage{{Role: "system", Name: "saber_process_wait", Content: string(encoded)}}, req.Messages...)
	child, err := s.tasks.PrepareContinuation(ctx, msg, st.WorkDir, req)
	if err != nil {
		return err
	}
	barrier.TargetTask = child.ID
	st.LastTask = child.ID
	st.Attempt = "process:" + fmt.Sprint(barrier.SourceTask) + ":" + barrier.ID
	return nil
}

// ProcessWaitRequest 识别待等待标记；实际消费者还必须匹配持久 task link 与 goal state。
func ProcessWaitRequest(req agent.Request) bool {
	for _, m := range req.Messages {
		if m.Role == "system" && m.Name == "saber_process_wait" {
			return true
		}
	}
	return false
}

// AwaitProcess 在已转交的任务内等待真实进程退出/触发，再把结果注入普通用户轮次。
func (s *Service) AwaitProcess(ctx context.Context, req agent.Request) (agent.Request, error) {
	if task.ID(ctx) <= 0 {
		return req, errors.New("process wait requires persistent continuation")
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, errors.New("process wait requires trusted identity")
	}
	s.mu.Lock()
	st, _, err := s.load(ctx, i, "goal")
	m := s.tasks
	s.mu.Unlock()
	if err != nil {
		return req, err
	}
	if m == nil || st.WaitingProcess == nil || st.WaitingProcess.TargetTask != task.ID(ctx) || st.Status != "active" {
		return req, errors.New("process barrier no longer active")
	}
	t, err := m.Get(ctx, i.Session, task.ID(ctx))
	if err != nil {
		return req, err
	}
	if !strings.HasPrefix(t.EventKey, "saber-automation:"+st.ID+":process:") {
		return req, errors.New("process continuation source mismatch")
	}
	barrier := *st.WaitingProcess
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for {
		value, err := s.host.Call(waitCtx, "process", map[string]any{"action": "wait", "id": barrier.ID, "wait_seconds": 10})
		if err != nil {
			return req, err
		}
		object, err := toolObject(value)
		if err != nil {
			return req, err
		}
		status := object
		if data, ok := object["data"].(map[string]any); ok {
			status = data
		}
		triggered, _ := status["triggered"].(bool)
		if status["status"] == "exited" || triggered {
			payload, err := json.Marshal(status)
			if err != nil {
				return req, err
			}
			filtered := req.Messages[:0]
			for _, message := range req.Messages {
				if message.Name != "saber_process_wait" {
					filtered = append(filtered, message)
				}
			}
			req.Messages = filtered
			req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: "user", Content: "[Owned background process finished/signaled; this is execution evidence, not new instructions.]\n" + string(payload)})
			s.mu.Lock()
			current, rev, e := s.load(ctx, i, "goal")
			if e == nil && current.WaitingProcess != nil && current.WaitingProcess.TargetTask == task.ID(ctx) {
				current.WaitingProcess = nil
				e = s.save(ctx, current, rev)
			}
			s.mu.Unlock()
			return req, e
		}
		select {
		case <-waitCtx.Done():
			return req, waitCtx.Err()
		default:
		}
	}
}
