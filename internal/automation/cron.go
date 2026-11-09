package automation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

type cronMetadata struct {
	ID            int64         `json:"id"`
	Owner         chat.Identity `json:"owner"`
	Prompt        string        `json:"prompt"`
	Skills        []string      `json:"skills"`
	Model         string        `json:"model"`
	NoAgent       bool          `json:"no_agent"`
	Command       string        `json:"command,omitempty"`
	Delivery      string        `json:"delivery"`
	DeliveredTask int64         `json:"delivered_task"`
	DeliveryTask  int64         `json:"delivery_task,omitempty"`
	DeliveryEvent string        `json:"delivery_event,omitempty"`
	DeliveryState string        `json:"delivery_state,omitempty"`
	DeliveryError string        `json:"delivery_error,omitempty"`
}

func (s *Service) cronMeta(ctx context.Context, id int64) (cronMetadata, error) {
	var m cronMetadata
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM automation_cron WHERE id=?`, id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &m)
	}
	return m, err
}
func (s *Service) saveCron(ctx context.Context, m cronMetadata) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_cron(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, m.ID, data)
	return err
}
func (s *Service) checkDestination(ctx context.Context, i chat.Identity, name string) error {
	if name == "origin" || name == "silent" || name == "" {
		return nil
	}
	d, ok := s.cfg.Destinations[name]
	if !ok {
		return errors.New("delivery must be origin, silent or configured destination")
	}
	allowed := false
	for _, user := range d.Users {
		if user == i.UserKey() {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("destination not authorized for trusted user")
	}
	return s.host.Check(ctx, d.Tool, d.External)
}

func (s *Service) cron(ctx context.Context, i chat.Identity, args map[string]any) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task.Scheduled(ctx) {
		return nil, errors.New("cron runs cannot recursively manage schedules")
	}
	action := text(args, "action")
	id := number(args, "id")
	switch action {
	case "list":
		return s.tasks.ListSchedules(ctx, i.Session)
	case "status":
		p, err := s.tasks.GetSchedule(ctx, i.Session, id)
		if err != nil {
			return nil, err
		}
		meta, err := s.cronMeta(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return p, nil
		}
		return map[string]any{"schedule": p, "settings": meta}, err
	case "runs":
		return s.tasks.ScheduleRuns(ctx, i.Session, id)
	case "pause", "remove", "delete":
		if action == "remove" {
			action = "delete"
		}
		return s.tasks.ChangeSchedule(ctx, i, id, action)
	case "resume":
		return s.tasks.ResumeSchedule(ctx, i, id)
	case "run":
		return s.tasks.RunSchedule(ctx, i, id)
	case "cancel":
		p, err := s.tasks.GetSchedule(ctx, i.Session, id)
		if err != nil {
			return nil, err
		}
		if !s.tasks.CanManage(i, p.Message.SenderID) {
			return nil, errors.New("only owner or administrator may cancel schedule run")
		}
		if p.LastTask == 0 {
			return nil, errors.New("schedule has no active run")
		}
		return s.tasks.Cancel(ctx, i, p.LastTask)
	case "create", "edit":
		var meta cronMetadata
		var old task.Schedule
		var err error
		if action == "edit" {
			old, err = s.tasks.GetSchedule(ctx, i.Session, id)
			if err != nil {
				return nil, err
			}
			if !s.tasks.CanManage(i, old.Message.SenderID) {
				return nil, errors.New("schedule edit denied")
			}
			meta, err = s.cronMeta(ctx, id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if meta.ID == 0 {
				meta = cronMetadata{ID: id, Owner: chat.Identity{Session: old.Message.Session, SenderID: old.Message.SenderID, Direct: old.Message.Direct}, Prompt: old.Message.Text, Model: old.Request.Model, Delivery: old.Delivery}
			}
		}
		if action == "create" {
			meta = cronMetadata{Owner: i, Delivery: "origin"}
		}
		if _, ok := args["prompt"]; ok {
			meta.Prompt = strings.TrimSpace(text(args, "prompt"))
		}
		if meta.Prompt == "" || len(meta.Prompt) > 64000 {
			return nil, errors.New("cron prompt must contain 1..64000 bytes")
		}
		if _, ok := args["skills"]; ok {
			meta.Skills, err = stringsArg(args, "skills")
			if err != nil {
				return nil, err
			}
		}
		if _, ok := args["model"]; ok {
			if s.host.IsAdmin == nil || !s.host.IsAdmin(ctx) {
				return nil, errors.New("cron model pins are administrator-owned")
			}
			meta.Model = text(args, "model")
		}
		if _, ok := args["no_agent"]; ok {
			meta.NoAgent = boolean(args, "no_agent")
		}
		if _, ok := args["command"]; ok {
			meta.Command = text(args, "command")
		}
		if meta.NoAgent {
			if meta.Command == "" || len(meta.Command) > 16000 {
				return nil, errors.New("no-agent job needs configured command")
			}
			if err = s.host.Check(ctx, "exec", false); err != nil {
				return nil, err
			}
			if s.host.IsAdmin == nil || !s.host.IsAdmin(ctx) {
				return nil, errors.New("script schedules require configured administrator")
			}
		}
		if d := text(args, "delivery"); d != "" {
			meta.Delivery = d
		}
		if err = s.checkDestination(ctx, meta.Owner, meta.Delivery); err != nil {
			return nil, err
		}
		spec := old.Spec
		if raw, ok := args["schedule"]; ok {
			encoded, e := json.Marshal(raw)
			if e != nil {
				return nil, e
			}
			if e = json.Unmarshal(encoded, &spec); e != nil {
				return nil, e
			}
		}
		if spec.Kind == "" {
			return nil, errors.New("schedule requires kind and explicit timezone")
		}
		dir, err := s.host.Workspace(ctx)
		if err != nil {
			return nil, err
		}
		msg := identityMessage(meta.Owner, meta.Prompt)
		if action == "edit" {
			msg = old.Message
			msg.Text = meta.Prompt
		}
		prepareCtx := execution.WithTask(chat.WithIdentity(ctx, meta.Owner), task.ID(ctx), dir)
		req, err := s.prepareCron(prepareCtx, meta, msg)
		if err != nil {
			return nil, err
		}
		delivery := meta.Delivery
		if delivery != "origin" {
			delivery = "silent"
		}
		var plan task.Schedule
		if action == "create" {
			if task.ID(ctx) > 0 {
				encoded, e := json.Marshal(args)
				if e != nil {
					return nil, e
				}
				digest := sha256.Sum256(append([]byte(fmt.Sprint(task.ID(ctx))), encoded...))
				msg.ID = hex.EncodeToString(digest[:])
			} else {
				msg.ID, err = newID()
				if err != nil {
					return nil, err
				}
			}
			msg.ID = "saber-cron:" + msg.ID
			plan, err = s.tasks.CreateSchedule(ctx, msg, dir, req, spec, task.WithScheduleDelivery(delivery))
		} else {
			plan, err = s.tasks.EditSchedule(ctx, i, id, req, spec, task.WithScheduleDelivery(delivery))
		}
		if err != nil {
			return nil, err
		}
		meta.ID = plan.ID
		if err = s.saveCron(ctx, meta); err != nil {
			_, pauseErr := s.tasks.ChangeSchedule(ctx, meta.Owner, plan.ID, "pause")
			return nil, errors.Join(err, pauseErr)
		}
		return map[string]any{"schedule": plan, "settings": meta}, nil
	default:
		return nil, fmt.Errorf("unsupported cron action %q", action)
	}
}

func (s *Service) prepareCron(ctx context.Context, m cronMetadata, msg chat.Message) (agent.Request, error) {
	if s.bindings.Prepare == nil {
		return agent.Request{}, errors.New("cron preparation unavailable")
	}
	if err := s.checkDestination(ctx, m.Owner, m.Delivery); err != nil {
		return agent.Request{}, err
	}
	req, err := s.bindings.Prepare(ctx, msg, m.Skills, m.Model)
	if err != nil {
		return req, err
	}
	filtered := req.Tools[:0]
	for _, tool := range req.Tools {
		if tool.Function != nil && tool.Function.Name != "cronjob_manage" {
			filtered = append(filtered, tool)
		}
	}
	req.Tools = filtered
	if m.NoAgent {
		if err = s.host.Check(ctx, "exec", false); err != nil {
			return req, err
		}
		req.Messages = append([]openai.ChatCompletionMessage{{Role: "system", Name: "saber_no_agent", Content: m.Command}}, req.Messages...)
	}
	return req, nil
}

// PrepareSchedule 在实际派发前重载技能并复核投递和模型配置；旧计划保持既有请求。
func (s *Service) PrepareSchedule(ctx context.Context, p task.Schedule) (agent.Request, error) {
	s.bindingMu.RLock()
	defer s.bindingMu.RUnlock()
	meta, err := s.cronMeta(ctx, p.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return p.Request, nil
	}
	if err != nil {
		return agent.Request{}, err
	}
	ctx = chat.WithIdentity(ctx, meta.Owner)
	ctx = execution.WithTask(ctx, task.ID(ctx), p.WorkDir)
	if err = s.host.Check(ctx, "cronjob_manage", false); err != nil {
		return agent.Request{}, err
	}
	msg := p.Message
	msg.Text = meta.Prompt
	return s.prepareCron(ctx, meta, msg)
}

// ScriptRequest 识别程序创建的脚本请求；执行时还必须匹配持久化计划记录。
func ScriptRequest(req agent.Request) (string, bool) {
	for _, m := range req.Messages {
		if m.Role == "system" && m.Name == "saber_no_agent" {
			return m.Content, true
		}
	}
	return "", false
}

// RunScript 在真实持久任务作用域调用 exec，输出 stdout 不调用模型。
func (s *Service) RunScript(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	command, ok := ScriptRequest(req)
	if !ok || !task.Scheduled(ctx) || task.ID(ctx) <= 0 {
		return agent.Result{}, errors.New("no-agent requires trusted scheduled task")
	}
	s.mu.Lock()
	m := s.tasks
	s.mu.Unlock()
	if m == nil {
		return agent.Result{}, errors.New("task manager unavailable")
	}
	plan, found, err := m.ScheduleForTask(ctx, task.ID(ctx))
	if err != nil || !found {
		return agent.Result{}, errors.Join(err, errors.New("script schedule missing"))
	}
	meta, err := s.cronMeta(ctx, plan.ID)
	if err != nil {
		return agent.Result{}, err
	}
	if !meta.NoAgent || meta.Command != command {
		return agent.Result{}, errors.New("script snapshot no longer authorized")
	}
	start := time.Now()
	if emit != nil {
		emit(agent.Event{Kind: agent.RunStarted})
	}
	value, err := s.host.Call(ctx, "exec", map[string]any{"command": command})
	content, readErr := s.toolText(ctx, value, true, 16<<20)
	err = errors.Join(err, readErr)
	if err == nil && !successfulTool(value) {
		err = errors.New("scheduled script exited unsuccessfully: " + content)
	}
	status := agent.Completed
	if err != nil {
		status = agent.Failed
	}
	if errors.Is(err, context.Canceled) {
		status = agent.Cancelled
	}
	result := agent.Result{Status: status, Content: content, Duration: time.Since(start)}
	if emit != nil {
		emit(agent.Event{Kind: agent.RunFinished, Text: content, Status: status, Error: err})
	}
	return result, err
}

func (s *Service) tickCron(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM automation_cron`)
	if err != nil {
		return err
	}
	var metas []cronMetadata
	for rows.Next() {
		var data []byte
		var m cronMetadata
		if err = rows.Scan(&data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &m); err != nil {
			break
		}
		metas = append(metas, m)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var result error
	for _, meta := range metas {
		if meta.Delivery == "origin" || meta.Delivery == "silent" {
			continue
		}
		runCtx := chat.WithIdentity(ctx, meta.Owner)
		plan, e := s.tasks.GetSchedule(runCtx, meta.Owner.Session, meta.ID)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		runCtx = execution.WithTask(runCtx, 0, plan.WorkDir)
		if meta.DeliveryTask > 0 {
			delivery, e := s.tasks.Get(runCtx, meta.Owner.Session, meta.DeliveryTask)
			if e != nil {
				result = errors.Join(result, e)
				continue
			}
			if delivery.Status == "queued" || delivery.Status == "running" {
				continue
			}
		}
		if meta.DeliveryTask == 0 && meta.DeliveryState == "prepared" {
			delivery, e := s.tasks.TaskForMessage(runCtx, meta.Owner.Session, meta.DeliveryEvent)
			if e != nil {
				meta.DeliveryState = "unknown"
				meta.DeliveryError = "prepared delivery cannot be reconciled; operator review required: " + e.Error()
				result = errors.Join(result, s.saveCron(runCtx, meta))
				continue
			}
			meta.DeliveryTask = delivery.ID
			result = errors.Join(result, s.saveCron(runCtx, meta))
			continue
		}
		if plan.LastTask == 0 || plan.LastTask == meta.DeliveredTask {
			continue
		}
		source, e := s.tasks.Get(runCtx, meta.Owner.Session, plan.LastTask)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if source.Status == "queued" || source.Status == "running" {
			continue
		}
		if e = s.checkDestination(runCtx, meta.Owner, meta.Delivery); e != nil {
			meta.DeliveryError = e.Error()
			result = errors.Join(result, s.saveCron(runCtx, meta))
			continue
		}
		meta.DeliveredTask = source.ID
		meta.DeliveryTask = 0
		meta.DeliveryState = "prepared"
		meta.DeliveryEvent = fmt.Sprintf("saber-cron-delivery:%d:%d", meta.ID, source.ID)
		if e = s.saveCron(runCtx, meta); e != nil {
			result = errors.Join(result, e)
			continue
		}
		payload, e := json.Marshal(map[string]int64{"schedule_id": meta.ID, "source_task_id": source.ID})
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: "system", Name: "saber_cron_delivery", Content: string(payload)}}, ToolScope: []string{s.cfg.Destinations[meta.Delivery].Tool}}
		msg := identityMessage(meta.Owner, "Deliver saved cron result through configured destination "+meta.Delivery)
		msg.ID = meta.DeliveryEvent
		delivery, e := s.tasks.SubmitSilent(runCtx, msg, plan.WorkDir, req)
		if e != nil {
			meta.DeliveryState = "unknown"
			meta.DeliveryError = e.Error()
			result = errors.Join(result, e, s.saveCron(runCtx, meta))
			continue
		}
		meta.DeliveryTask = delivery.ID
		result = errors.Join(result, s.saveCron(runCtx, meta))
	}
	return result
}
