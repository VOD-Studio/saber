// Package automation 将持久自动化发生交给唯一任务管理器，不创建第二套执行队列。
package automation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/db"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// Bindings 由可信 AI 宿主装配请求、技能存储和模型调用，不允许工具参数覆盖。
type Bindings struct {
	Prepare     func(context.Context, chat.Message, []string, string) (agent.Request, error)
	Skills      SkillStore
	Consolidate func(context.Context, []Skill, bool) (any, error)
	Command     func(context.Context, string) (string, error)
	LoadSkills  func(context.Context, agent.Request, []string) (agent.Request, error)
}

// Service 保存自动化控制面；所有模型/脚本执行都使用 task.Manager。
type Service struct {
	cfg       config.AutomationConfig
	host      toolkit.Host
	db        *sql.DB
	mu        sync.Mutex
	bindingMu sync.RWMutex
	tasks     *task.Manager
	bindings  Bindings
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// State 是一个会话内 loop、goal 或 heartbeat 的持久状态。
type State struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	Owner             chat.Identity     `json:"owner"`
	Message           chat.Message      `json:"message"`
	Generation        int64             `json:"generation"`
	WorkDir           string            `json:"work_dir"`
	Prompt            string            `json:"prompt"`
	Status            string            `json:"status"`
	Reason            string            `json:"reason,omitempty"`
	Model             string            `json:"model,omitempty"`
	Skills            []string          `json:"skills,omitempty"`
	Interval          time.Duration     `json:"interval"`
	Delay             time.Duration     `json:"delay"`
	SelfPaced         bool              `json:"self_paced,omitempty"`
	MaxTicks          int               `json:"max_ticks"`
	Times             int               `json:"times,omitempty"`
	Ticks             int               `json:"ticks"`
	Until             string            `json:"until,omitempty"`
	Contract          map[string]string `json:"contract,omitempty"`
	Subgoals          []string          `json:"subgoals,omitempty"`
	Gates             []Gate            `json:"gates,omitempty"`
	WaitingTask       int64             `json:"waiting_task,omitempty"`
	WaitingUntil      time.Time         `json:"waiting_until,omitempty"`
	WaitingProcess    *ProcessBarrier   `json:"waiting_process,omitempty"`
	Created           time.Time         `json:"created_at"`
	Next              time.Time         `json:"next_due_at"`
	LastTask          int64             `json:"last_task,omitempty"`
	Attempt           string            `json:"attempt,omitempty"`
	Digest            string            `json:"last_response_digest,omitempty"`
	ParseFailures     int               `json:"parse_failures,omitempty"`
	TransportFailures int               `json:"transport_failures,omitempty"`
}

// Gate 保存管理员设置的确定性检查及有界重试状态。
type Gate struct {
	Command  string `json:"command"`
	Failures int    `json:"failures"`
	Passed   bool   `json:"passed"`
	Output   string `json:"output,omitempty"`
}

// New 创建私有控制库；调用 Configure 和 AttachTaskManager 后自动恢复调度。
func New(cfg config.AutomationConfig, host toolkit.Host) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if host.StateDir == "" {
		return nil, errors.New("automation requires private state directory")
	}
	if err := os.MkdirAll(host.StateDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(host.StateDir, "automation.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	database, err := sql.Open(db.DriverName, path)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE IF NOT EXISTS automation_states (session TEXT NOT NULL,kind TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,data BLOB NOT NULL,PRIMARY KEY(session,kind)); CREATE TABLE IF NOT EXISTS automation_cron (id INTEGER PRIMARY KEY,data BLOB NOT NULL); CREATE TABLE IF NOT EXISTS automation_curator (owner TEXT PRIMARY KEY,data BLOB NOT NULL); CREATE TABLE IF NOT EXISTS automation_ledger (id INTEGER PRIMARY KEY AUTOINCREMENT,owner TEXT NOT NULL,skill TEXT NOT NULL,created INTEGER NOT NULL,data BLOB NOT NULL); CREATE TABLE IF NOT EXISTS automation_archives (owner TEXT NOT NULL,name TEXT NOT NULL,data BLOB NOT NULL,archived INTEGER NOT NULL,PRIMARY KEY(owner,name)); CREATE TABLE IF NOT EXISTS automation_skill_flags (owner TEXT NOT NULL,name TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(owner,name)); CREATE TABLE IF NOT EXISTS automation_backups (id TEXT PRIMARY KEY,owner TEXT NOT NULL,created INTEGER NOT NULL,data BLOB NOT NULL);`)
	if err != nil {
		return nil, errors.Join(err, database.Close())
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{cfg: cfg, host: host, db: database, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go s.loop()
	return s, nil
}

// Configure 安装可信请求和技能端口。
func (s *Service) Configure(b Bindings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	s.bindings = b
}

// AttachTaskManager 接入现有权威队列，不接入时工具和定时执行明确失败。
func (s *Service) AttachTaskManager(m *task.Manager) { s.mu.Lock(); defer s.mu.Unlock(); s.tasks = m }

// Close 取消自动化调度与回调并等待退出，然后关闭控制库。
func (s *Service) Close() error {
	s.closeOnce.Do(func() { s.cancel(); <-s.done; s.closeErr = s.db.Close() })
	return s.closeErr
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func stateKey(i chat.Identity) string { return string(i.Session.Key()) }
func (s *Service) load(ctx context.Context, i chat.Identity, kind string) (State, int64, error) {
	var st State
	var data []byte
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT revision,data FROM automation_states WHERE session=? AND kind=?`, stateKey(i), kind).Scan(&rev, &data)
	if err != nil {
		return st, 0, err
	}
	if err = json.Unmarshal(data, &st); err != nil {
		return st, 0, err
	}
	if st.Owner.SenderID != i.SenderID && (s.host.IsAdmin == nil || !s.host.IsAdmin(ctx)) {
		return State{}, 0, errors.New("自动化只允许负责人或当前会话管理员访问")
	}
	return st, rev, nil
}
func (s *Service) save(ctx context.Context, st State, rev int64) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	var result sql.Result
	if rev == 0 {
		result, err = s.db.ExecContext(ctx, `INSERT INTO automation_states(session,kind,data) VALUES(?,?,?)`, stateKey(st.Owner), st.Kind, data)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE automation_states SET data=?,revision=revision+1 WHERE session=? AND kind=? AND revision=?`, data, stateKey(st.Owner), st.Kind, rev)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		err = errors.New("自动化状态已被其他操作修改")
	}
	return err
}
func (s *Service) loop() {
	defer close(s.done)
	interval := s.cfg.PollSeconds
	if interval < 1 {
		interval = 5
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			if s.cfg.Enabled {
				if err := s.Tick(s.ctx, now); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("自动化轮询失败", "error", err)
				}
			}
		}
	}
}

// Definitions 列出实际控制入口；注册表与执行入口均检查精确权限。
func (s *Service) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	var result []toolkit.Definition
	for _, name := range []string{"automation_loop", "automation_goal", "automation_heartbeat", "cronjob_manage", "curator"} {
		result = append(result, toolkit.Definition{Permission: name, Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Description: "持久自动化控制；身份来自当前会话。action 支持 set/status/pause/resume/clear；cron 支持 create/list/edit/run/cancel/remove/runs；goal 支持 draft/subgoal/gate/wait/unwait；curator 支持 run/status/pin/lock/adopt/archive/restore/backup/rollback/ledger/purge。", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "interval": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"}, "times": map[string]any{"type": "integer", "minimum": 0}, "until": map[string]any{"type": "string"}, "skills": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "schedule": map[string]any{"type": "object"}, "delivery": map[string]any{"type": "string"}, "contract": map[string]any{"type": "object"}, "command": map[string]any{"type": "string"}, "task_id": map[string]any{"type": "integer"}, "seconds": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "dry_run": map[string]any{"type": "boolean"}, "model": map[string]any{"type": "string"}, "no_agent": map[string]any{"type": "boolean"}, "index": map[string]any{"type": "integer"}, "operation": map[string]any{"type": "string"}}, "required": []string{"action"}}}}})
	}
	return result
}

// Execute 在真实操作前复核身份、功能授权与任务服务。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if !s.cfg.Enabled {
		return nil, errors.New("automation disabled")
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("automation requires trusted identity")
	}
	if s.host.Check == nil {
		return nil, errors.New("automation authorization unavailable")
	}
	if err := s.host.Check(ctx, name, false); err != nil {
		return nil, err
	}
	s.mu.Lock()
	m := s.tasks
	s.mu.Unlock()
	if m == nil {
		return nil, errors.New("automation task manager not attached")
	}
	switch name {
	case "cronjob_manage":
		return s.cron(ctx, i, args)
	case "curator":
		return s.curate(ctx, i, args)
	case "automation_loop":
		return s.control(ctx, i, "loop", args)
	case "automation_goal":
		return s.control(ctx, i, "goal", args)
	case "automation_heartbeat":
		return s.control(ctx, i, "heartbeat", args)
	default:
		return nil, fmt.Errorf("unknown automation tool %q", name)
	}
}

func text(args map[string]any, key string) string  { v, _ := args[key].(string); return v }
func boolean(args map[string]any, key string) bool { v, _ := args[key].(bool); return v }
func number(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		if v == float64(int64(v)) {
			return int64(v)
		}
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func stringsArg(args map[string]any, key string) ([]string, error) {
	v, exists := args[key]
	if !exists {
		return nil, nil
	}
	var out []string
	switch xs := v.(type) {
	case []string:
		out = xs
	case []any:
		for _, x := range xs {
			v, ok := x.(string)
			if !ok || v == "" {
				return nil, errors.New("skills requires nonempty strings")
			}
			out = append(out, v)
		}
	default:
		return nil, errors.New("skills requires array")
	}
	if len(out) > 32 {
		return nil, errors.New("too many skills")
	}
	return out, nil
}
func identityMessage(i chat.Identity, prompt string) chat.Message {
	return chat.Message{Session: i.Session, SenderID: i.SenderID, Direct: i.Direct, Text: prompt}
}
