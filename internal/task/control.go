package task

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

const controlSchema = `
CREATE TABLE IF NOT EXISTS task_dependencies (
 task_id INTEGER NOT NULL REFERENCES tasks(id), dependency_id INTEGER NOT NULL REFERENCES tasks(id),
 PRIMARY KEY(task_id,dependency_id), CHECK(task_id<>dependency_id)
);
CREATE TABLE IF NOT EXISTS task_control (
 task_id INTEGER PRIMARY KEY REFERENCES tasks(id), phase TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '',
 metadata BLOB NOT NULL DEFAULT '{}', revision INTEGER NOT NULL DEFAULT 0,
 resume_task_id INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS task_children (
 task_id INTEGER PRIMARY KEY REFERENCES tasks(id), parent_id INTEGER NOT NULL REFERENCES tasks(id),
 consumed_at INTEGER, detached INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS task_children_parent ON task_children(parent_id,consumed_at);
CREATE TABLE IF NOT EXISTS task_child_received (
 parent_id INTEGER NOT NULL REFERENCES tasks(id), child_id INTEGER NOT NULL REFERENCES tasks(id),
 payload BLOB NOT NULL, created_at INTEGER NOT NULL, delivered_round INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(parent_id,child_id)
);
CREATE TABLE IF NOT EXISTS task_control_delivery (
 task_id INTEGER PRIMARY KEY REFERENCES tasks(id), revision INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS task_external (
 task_id INTEGER PRIMARY KEY REFERENCES tasks(id), kind TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_questions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL REFERENCES tasks(id),
 question TEXT NOT NULL, choices BLOB NOT NULL DEFAULT '[]', multi_select INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'pending', answer BLOB NOT NULL DEFAULT 'null',
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, answered_at INTEGER,
 resume_task_id INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS task_questions_pending ON task_questions(task_id,status);
CREATE TABLE IF NOT EXISTS task_handoffs (
 task_id INTEGER PRIMARY KEY REFERENCES tasks(id), route TEXT NOT NULL, source BLOB NOT NULL,
 target BLOB NOT NULL, created_at INTEGER NOT NULL
);`

// ControlState 保存执行任务附属的审查、阻塞与恢复信息，不建立第二套任务队列。
type ControlState struct {
	TaskID       int64          `json:"task_id"`
	Phase        string         `json:"phase"`
	Kind         string         `json:"kind,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Revision     int64          `json:"revision"`
	ResumeTaskID int64          `json:"resume_task_id,omitempty"`
}

// GraphNode 将真实任务与依赖、控制状态一起返回给模型或认证 API。
type GraphNode struct {
	Task         Task         `json:"task"`
	Control      ControlState `json:"control"`
	Dependencies []int64      `json:"dependencies"`
	Children     []int64      `json:"children"`
}

func controlIdentity(ctx context.Context) (chat.Identity, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return chat.Identity{}, errors.New("任务控制需要可信身份")
	}
	return identity, nil
}

func (m *Manager) controlledTask(ctx context.Context, identity chat.Identity, id int64) (Task, error) {
	t, err := m.Get(ctx, identity.Session, id)
	if err != nil {
		return Task{}, err
	}
	if t.Message.Session.Thread != identity.Session.Thread || !m.CanManage(identity, t.Message.SenderID) {
		return Task{}, errors.New("任务不属于当前用户或线程")
	}
	if current := ID(ctx); current > 0 && current != id {
		var related bool
		err = m.store.db.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (SELECT ? UNION SELECT c.task_id FROM task_children c JOIN descendants d ON c.parent_id=d.id) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, current, id).Scan(&related)
		if err != nil {
			return Task{}, err
		}
		if !related {
			return Task{}, errors.New("运行中的代理只能控制自己的任务树")
		}
	}
	return t, nil
}

func controlID(args map[string]any, key string, fallback int64) (int64, error) {
	value, exists := args[key]
	if !exists && fallback > 0 {
		return fallback, nil
	}
	id, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return id, nil
}

func controlText(args map[string]any, key string, required bool) (string, error) {
	value, exists := args[key]
	if !exists && !required {
		return "", nil
	}
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	if !ok || (required && text == "") || len(text) > 32768 {
		return "", fmt.Errorf("%s requires text of at most 32768 bytes", key)
	}
	return text, nil
}

func controlToken() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func (m *Manager) authorizeControl(identity chat.Identity, dir string) error {
	if m.controlAuthorize == nil {
		return errors.New("任务创建或恢复没有配置当前工作区授权检查")
	}
	return m.controlAuthorize(identity, dir)
}

func (m *Manager) checkControlledTask(ctx context.Context, t Task) error {
	var controlled bool
	if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_control WHERE task_id=? UNION SELECT 1 FROM task_children WHERE task_id=?)`, t.ID, t.ID).Scan(&controlled); err != nil {
		return err
	}
	if !controlled {
		return nil
	}
	if m.authorizeTask != nil {
		return m.authorizeTask(ctx, t)
	}
	identity := chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct}
	return m.authorizeControl(identity, t.WorkDir)
}

func (m *Manager) controlState(ctx context.Context, id int64) (ControlState, error) {
	state := ControlState{TaskID: id}
	var metadata []byte
	err := m.store.db.QueryRowContext(ctx, `SELECT phase,kind,reason,summary,metadata,revision,resume_task_id FROM task_control WHERE task_id=?`, id).Scan(&state.Phase, &state.Kind, &state.Reason, &state.Summary, &metadata, &state.Revision, &state.ResumeTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(metadata, &state.Metadata)
}

func (m *Manager) graphNode(ctx context.Context, t Task) (GraphNode, error) {
	state, err := m.controlState(ctx, t.ID)
	if err != nil {
		return GraphNode{}, err
	}
	node := GraphNode{Task: t, Control: state, Dependencies: []int64{}, Children: []int64{}}
	rows, err := m.store.db.QueryContext(ctx, `SELECT dependency_id,0 FROM task_dependencies WHERE task_id=? UNION ALL SELECT task_id,1 FROM task_children WHERE parent_id=? ORDER BY 2,1`, t.ID, t.ID)
	if err != nil {
		return node, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var child int
		if err := rows.Scan(&id, &child); err != nil {
			return node, err
		}
		if child == 1 {
			node.Children = append(node.Children, id)
		} else {
			node.Dependencies = append(node.Dependencies, id)
		}
	}
	return node, rows.Err()
}

// GraphTool 在同一持久任务权威下管理依赖图和审查；身份不能由参数覆盖。
func (m *Manager) GraphTool(ctx context.Context, args map[string]any) (any, error) {
	if !m.control.Enabled {
		return nil, errors.New("task control is disabled")
	}
	identity, err := controlIdentity(ctx)
	if err != nil {
		return nil, err
	}
	action, _ := args["action"].(string)
	if ID(ctx) > 0 && (action == "approve" || action == "request_changes" || action == "unblock" || action == "resume") {
		return nil, errors.New("审查裁定、解除阻塞和恢复必须由真实用户入口发起")
	}
	if action == "list" {
		tasks, err := m.store.query(ctx, `platform=? AND account=? AND room=? AND sender=? ORDER BY id DESC LIMIT 100`, append(scope(identity.Session), identity.SenderID)...)
		if err != nil {
			return nil, err
		}
		nodes := []GraphNode{}
		for _, t := range tasks {
			if t.Message.Session.Thread != identity.Session.Thread {
				continue
			}
			node, err := m.graphNode(ctx, t)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, node)
		}
		return nodes, nil
	}
	if action == "create" {
		return m.createGraphTask(ctx, identity, args)
	}
	id, err := controlID(args, "task_id", ID(ctx))
	if err != nil {
		return nil, err
	}
	t, err := m.controlledTask(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	switch action {
	case "show":
		return m.graphNode(ctx, t)
	case "link", "unlink":
		dependency, err := controlID(args, "dependency_id", 0)
		if err != nil {
			return nil, err
		}
		if _, err = m.controlledTask(ctx, identity, dependency); err != nil {
			return nil, err
		}
		if action == "link" {
			err = m.LinkDependency(ctx, identity, id, dependency)
		} else {
			err = m.unlinkDependency(ctx, identity, id, dependency)
		}
		if err != nil {
			return nil, err
		}
		return m.graphNode(ctx, t)
	case "cancel":
		return m.Cancel(ctx, identity, id)
	case "handoff":
		route, err := controlText(args, "route", true)
		if err != nil {
			return nil, err
		}
		return m.Handoff(ctx, identity, id, route)
	case "complete":
		if ID(ctx) != id || t.Status != "running" {
			return nil, errors.New("只有当前真实运行任务可以请求完成；审查由用户approve裁定")
		}
		summary, err := controlText(args, "summary", true)
		if err != nil {
			return nil, err
		}
		var pending bool
		if err = m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_dependencies d JOIN tasks p ON p.id=d.dependency_id WHERE d.task_id=? AND p.status<>'completed')`, id).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			return nil, errors.New("任务仍有未完成依赖，不能完成")
		}
		if err = m.pauseControl(ctx, t, "completion_requested", "", summary, args["metadata"]); err != nil {
			return nil, err
		}
		return m.controlState(ctx, id)
	case "drain":
		return m.DrainChildResults(ctx, identity, id)
	case "stop_children":
		return m.StopChildren(ctx, identity, id)
	case "block", "request_review":
		reasonKey := "reason"
		if action == "request_review" {
			reasonKey = "summary"
		}
		reason, err := controlText(args, reasonKey, true)
		if err != nil {
			return nil, err
		}
		kind, _ := args["kind"].(string)
		if action == "block" && kind != "dependency" && kind != "needs_input" && kind != "capability" && kind != "transient" {
			return nil, errors.New("block kind must be dependency, needs_input, capability or transient")
		}
		phase := "blocked"
		if action == "request_review" {
			phase = "review"
		}
		if err := m.pauseControl(ctx, t, phase, kind, reason, args["metadata"]); err != nil {
			return nil, err
		}
		updated, err := m.Get(ctx, identity.Session, id)
		if err != nil {
			return nil, err
		}
		return m.graphNode(ctx, updated)
	case "unblock", "request_changes", "approve", "resume":
		reason, err := controlText(args, "reason", action == "request_changes")
		if err != nil {
			return nil, err
		}
		return m.releaseControl(ctx, identity, t, action, reason)
	case "comment":
		text, err := controlText(args, "text", true)
		if err != nil {
			return nil, err
		}
		return m.Redirect(ctx, identity, id, text)
	default:
		return nil, fmt.Errorf("unknown task graph action %q", action)
	}
}

func (m *Manager) createGraphTask(ctx context.Context, identity chat.Identity, args map[string]any) (Task, error) {
	parentID, err := controlID(args, "parent_id", ID(ctx))
	if err != nil {
		return Task{}, err
	}
	parent, err := m.controlledTask(ctx, identity, parentID)
	if err != nil {
		return Task{}, err
	}
	goal, err := controlText(args, "goal", true)
	if err != nil {
		return Task{}, err
	}
	dir := parent.WorkDir
	if value, exists := args["work_dir"]; exists {
		var ok bool
		dir, ok = value.(string)
		if !ok {
			return Task{}, errors.New("work_dir must be a string")
		}
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	if err = m.authorizeControl(identity, dir); err != nil {
		return Task{}, err
	}
	var dependencies []int64
	if raw, exists := args["dependencies"]; exists {
		data, err := json.Marshal(raw)
		if err != nil {
			return Task{}, err
		}
		if err = json.Unmarshal(data, &dependencies); err != nil {
			return Task{}, errors.New("dependencies must contain integer task IDs")
		}
	}
	if len(dependencies) > m.maxDependencies() {
		return Task{}, errors.New("too many task dependencies")
	}
	for _, dependency := range dependencies {
		if _, err := m.controlledTask(ctx, identity, dependency); err != nil {
			return Task{}, err
		}
	}
	token, err := controlToken()
	if err != nil {
		return Task{}, err
	}
	msg := chat.Message{Session: identity.Session, ID: "graph:" + token, SenderID: identity.SenderID, Direct: identity.Direct, Text: goal}
	req := parent.Request
	req.Messages = []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: goal}}
	req.State = nil
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := insertTaskTx(ctx, tx, msg, dir, req)
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_children(task_id,parent_id,created_at) VALUES(?,?,?)`, t.ID, parentID, time.Now().UnixMilli()); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id) VALUES(?)`, t.ID); err != nil {
		return Task{}, err
	}
	for _, dependency := range dependencies {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,dependency_id) VALUES(?,?) ON CONFLICT DO NOTHING`, t.ID, dependency); err != nil {
			return Task{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	m.transientRequests[t.ID] = req
	if err := m.normalizeInternalMessage(ctx, &t); err != nil {
		return Task{}, err
	}
	m.notify()
	return t, nil
}

func (m *Manager) maxDependencies() int {
	if m.control.MaxDependencies > 0 {
		return m.control.MaxDependencies
	}
	return 64
}

// LinkDependency 原子添加依赖并联合续接链防环；只有当前运行所有者可链接自己的执行卡。
func (m *Manager) LinkDependency(ctx context.Context, identity chat.Identity, id, dependency int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.controlledTask(ctx, identity, id)
	if err != nil {
		return err
	}
	if _, err = m.controlledTask(ctx, identity, dependency); err != nil {
		return err
	}
	if id == dependency || (t.Status != "queued" && t.Status != "blocked" && (t.Status != "running" || ID(ctx) != id)) {
		return errors.New("依赖不能自引用，运行中的任务必须由当前持有者链接")
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var cycle bool
	if err = tx.QueryRowContext(ctx, `WITH RECURSIVE edges(task_id,dependency_id) AS (SELECT task_id,dependency_id FROM task_dependencies UNION ALL SELECT task_id,parent_id FROM task_links), ancestors(id) AS (SELECT ? UNION SELECT e.dependency_id FROM edges e JOIN ancestors a ON e.task_id=a.id) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=?)`, dependency, id).Scan(&cycle); err != nil {
		return err
	}
	if cycle {
		return errors.New("任务依赖会形成环")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM task_dependencies WHERE task_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count >= m.maxDependencies() {
		return errors.New("too many task dependencies")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,dependency_id) VALUES(?,?) ON CONFLICT DO NOTHING`, id, dependency); err != nil {
		return err
	}
	m.notify()
	return tx.Commit()
}

func (m *Manager) unlinkDependency(ctx context.Context, identity chat.Identity, id, dependency int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.controlledTask(ctx, identity, id)
	if err != nil {
		return err
	}
	if t.Status != "queued" && t.Status != "blocked" {
		return errors.New("已派发任务的依赖不可修改")
	}
	_, err = m.store.db.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id=? AND dependency_id=?`, id, dependency)
	m.notify()
	return err
}

func (m *Manager) pauseControl(ctx context.Context, t Task, phase, kind, reason string, metadata any) error {
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.Status != "queued" && (t.Status != "running" || ID(ctx) != t.ID) {
		return errors.New("只有排队任务或持有当前运行身份的任务可以阻塞或请求审查")
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, ok := metadata.(map[string]any); !ok {
		return errors.New("metadata must be an object")
	}
	data, err := json.Marshal(metadata)
	if err != nil || len(data) > 65536 {
		return errors.Join(err, errors.New("metadata exceeds 65536 bytes"))
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	summary := ""
	if phase == "review" || phase == "completion_requested" {
		summary = reason
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id,phase,kind,reason,summary,metadata,revision,updated_at) VALUES(?,?,?,?,?,?,1,?) ON CONFLICT(task_id) DO UPDATE SET phase=excluded.phase,kind=excluded.kind,reason=excluded.reason,summary=excluded.summary,metadata=excluded.metadata,revision=task_control.revision+1,updated_at=excluded.updated_at`, t.ID, phase, kind, reason, summary, data, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=? AND status='queued'`, phase, t.ID); err != nil {
		return err
	}
	if err := controlJournal(ctx, tx, t.ID, map[string]any{"phase": phase, "kind": kind, "reason": reason, "summary": summary}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery='pending',delivery_attempts=0,next_delivery=0 WHERE id=? AND id IN (SELECT task_id FROM task_control_delivery)`, t.ID); err != nil {
		return err
	}
	m.notify()
	return tx.Commit()
}

func (m *Manager) releaseControl(ctx context.Context, identity chat.Identity, t Task, action, reason string) (any, error) {
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.controlState(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if (action == "approve" || action == "request_changes") && state.Phase != "review" {
		return nil, errors.New("task is not waiting for review")
	}
	if action == "unblock" && state.Phase != "blocked" && t.Status != "blocked" {
		return nil, errors.New("task is not blocked")
	}
	if err := m.authorizeControl(identity, t.WorkDir); err != nil {
		return nil, err
	}
	phase := ""
	if action == "approve" {
		phase = "approved"
	}
	if action == "request_changes" {
		phase = "changes_requested"
	}
	if t.Status == "running" {
		if _, err = m.store.db.ExecContext(ctx, `UPDATE task_control SET phase=?,reason=?,revision=revision+1,updated_at=? WHERE task_id=?`, phase, reason, time.Now().UnixMilli(), t.ID); err != nil {
			return nil, err
		}
		if reason != "" {
			if err = m.store.redirect(ctx, t.ID, reason); err != nil {
				return nil, err
			}
		}
		m.notify()
		return m.controlState(ctx, t.ID)
	}
	var dispatched bool
	if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_events WHERE task_id=? AND json_extract(record,'$.Kind') IN ('run_started','model_started','tool_started'))`, t.ID).Scan(&dispatched); err != nil {
		return nil, err
	}
	if t.Status == "queued" || (t.Status == "blocked" && !dispatched) {
		if _, err = m.store.db.ExecContext(ctx, `UPDATE tasks SET status='queued' WHERE id=? AND status IN ('queued','blocked')`, t.ID); err != nil {
			return nil, err
		}
		if _, err = m.store.db.ExecContext(ctx, `UPDATE task_control SET phase=?,reason=?,revision=revision+1 WHERE task_id=?`, phase, reason, t.ID); err != nil {
			return nil, err
		}
		m.notify()
		return m.Get(ctx, identity.Session, t.ID)
	}
	if action == "approve" {
		result := t.Result
		result.Status = agent.Completed
		result.Content = state.Summary
		data, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if _, err = m.store.db.ExecContext(ctx, `UPDATE tasks SET status='completed',result=?,error='',delivery='pending',delivery_attempts=0,delivery_id='',delivery_error='',next_delivery=0 WHERE id=? AND status IN ('review','interrupted')`, data, t.ID); err != nil {
			return nil, err
		}
		if _, err = m.store.db.ExecContext(ctx, `UPDATE task_control SET phase='approved',revision=revision+1 WHERE task_id=?`, t.ID); err != nil {
			return nil, err
		}
		m.notify()
		return m.Get(ctx, identity.Session, t.ID)
	}
	if t.Status != "blocked" && t.Status != "review" && t.Status != "interrupted" && t.Status != "waiting_input" && t.Status != "failed" {
		return nil, errors.New("仅可显式恢复暂停、中断或失败任务，已派发副作用不会重放")
	}
	return m.resumeControlLocked(ctx, identity, t, reason)
}

func (m *Manager) resumeControlLocked(ctx context.Context, identity chat.Identity, t Task, reason string) (Task, error) {
	if t.CancelRequested || t.Status == "cancelled" {
		return Task{}, errors.New("已取消任务不能被控制事件复活，请新建真实用户任务")
	}
	if t.Status != "blocked" && t.Status != "review" && t.Status != "interrupted" && t.Status != "waiting_input" && t.Status != "failed" {
		return Task{}, errors.New("task is not resumable")
	}
	state, err := m.controlState(ctx, t.ID)
	if err != nil {
		return Task{}, err
	}
	if state.ResumeTaskID > 0 {
		return m.Get(ctx, identity.Session, state.ResumeTaskID)
	}
	if err = m.authorizeControl(identity, t.WorkDir); err != nil {
		return Task{}, err
	}
	msg := chat.Message{Session: t.Message.Session, ID: fmt.Sprintf("control-resume:%d:%d", t.ID, state.Revision), SenderID: t.Message.SenderID, Direct: t.Message.Direct, Text: "继续原任务；先核查未确认的外部副作用，不自动重放。\n" + reason}
	req := t.Request
	req.Messages = []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: msg.Text}}
	req.State = nil
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	resumed, err := insertTaskTx(ctx, tx, msg, t.WorkDir, req, t.ID)
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id,resume_task_id,phase,revision) VALUES(?,?,'resumed',1) ON CONFLICT(task_id) DO UPDATE SET resume_task_id=excluded.resume_task_id,phase='resumed',revision=task_control.revision+1`, t.ID, resumed.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id) VALUES(?) ON CONFLICT DO NOTHING`, resumed.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='interrupted' WHERE id=? AND status IN ('blocked','review','waiting_input')`, t.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_dependencies SET dependency_id=? WHERE dependency_id=?`, resumed.ID, t.ID); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	m.transientRequests[resumed.ID] = req
	if err := m.normalizeInternalMessage(ctx, &resumed); err != nil {
		return Task{}, err
	}
	m.notify()
	return resumed, nil
}

func (m *Manager) tickControl(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.store.db.ExecContext(ctx, `UPDATE tasks SET status=(SELECT phase FROM task_control WHERE task_id=tasks.id) WHERE status='interrupted' AND cancel_requested=0 AND EXISTS(SELECT 1 FROM task_control WHERE task_id=tasks.id AND phase IN ('blocked','review','waiting_input'));
UPDATE tasks SET status='blocked',error='依赖任务失败、取消或中断，需要明确解除依赖后恢复' WHERE status='queued' AND EXISTS(SELECT 1 FROM task_dependencies d JOIN tasks p ON p.id=d.dependency_id WHERE d.task_id=tasks.id AND p.status IN ('failed','cancelled','interrupted'));
UPDATE task_questions SET status='timed_out',answered_at=? WHERE status='pending' AND expires_at<=?`, time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
		return err
	}
	blocked, err := m.store.query(ctx, `status='blocked' AND id IN (SELECT task_id FROM task_control WHERE phase='blocked' AND kind='dependency')`)
	if err != nil {
		return err
	}
	for _, t := range blocked {
		var total, pending int
		if err := m.store.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(p.status<>'completed'),0) FROM task_dependencies d JOIN tasks p ON p.id=d.dependency_id WHERE d.task_id=?`, t.ID).Scan(&total, &pending); err != nil {
			return err
		}
		if total == 0 {
			if _, err := m.store.db.ExecContext(ctx, `UPDATE task_control SET kind='needs_input',revision=revision+1 WHERE task_id=?`, t.ID); err != nil {
				return err
			}
			continue
		}
		if pending != 0 {
			continue
		}
		identity := chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct}
		var dispatched bool
		if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_events WHERE task_id=? AND json_extract(record,'$.Kind') IN ('run_started','model_started','tool_started'))`, t.ID).Scan(&dispatched); err != nil {
			return err
		}
		if !dispatched {
			if _, err := m.store.db.ExecContext(ctx, `UPDATE task_control SET phase='',revision=revision+1 WHERE task_id=?; UPDATE tasks SET status='queued',error='' WHERE id=?`, t.ID, t.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := m.resumeControlLocked(ctx, identity, t, "所有依赖任务已完成，结合已保存轨迹继续，不重放未知工具。"); err != nil {
			if _, saveErr := m.store.db.ExecContext(ctx, `UPDATE task_control SET kind='needs_input',reason=?,revision=revision+1 WHERE task_id=?`, err.Error(), t.ID); saveErr != nil {
				return errors.Join(err, saveErr)
			}
		}
	}
	return nil
}

// BindToolContext 为认证 API 绑定真实任务来源；管理员不能借此冒充任务发起人扩大权限。
func (m *Manager) BindToolContext(ctx context.Context, identity chat.Identity, taskID int64) (context.Context, error) {
	t, err := m.controlledTask(ctx, identity, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != "running" || t.CancelRequested {
		return nil, errors.New("工具调用必须绑定未取消的运行中任务")
	}
	trusted := chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct}
	m.mu.Lock()
	runningCtx := m.runContexts[taskID]
	m.mu.Unlock()
	if runningCtx == nil || runningCtx.Err() != nil {
		return nil, errors.New("任务已经停止或当前进程不持有执行权")
	}
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(runningCtx, cancel)
	context.AfterFunc(bound, func() { stop() })
	ctx = bound
	ctx = chat.WithIdentity(ctx, trusted)
	ctx = context.WithValue(ctx, workDirKey{}, t.WorkDir)
	ctx = context.WithValue(ctx, taskIDKey{}, taskID)
	if m.authorizeTask != nil {
		if err := m.authorizeTask(ctx, t); err != nil {
			cancel()
			return nil, err
		}
	} else if err := m.authorizeControl(trusted, t.WorkDir); err != nil {
		cancel()
		return nil, err
	}
	return ctx, nil
}

// RecordExternalTool 只为经过当前任务归属检查的外部工具写入真实开始或结束事件。
func (m *Manager) RecordExternalTool(ctx context.Context, identity chat.Identity, taskID int64, event agent.Event) error {
	t, err := m.controlledTask(ctx, identity, taskID)
	if err != nil {
		return err
	}
	if event.Kind == agent.ToolStarted && (t.Status != "running" || t.CancelRequested) {
		return errors.New("任务已经停止，不可记录新的工具派发")
	}
	if event.Kind != agent.ToolStarted && event.Kind != agent.ToolFinished {
		return errors.New("外部入口仅能记录工具开始或结束")
	}
	return m.store.record(context.WithoutCancel(ctx), taskID, event)
}

// RequestedCompletion 让 Runtime 在完整工具批次之后消费正常完成请求，不用取消伪装完成。
func (m *Manager) RequestedCompletion(ctx context.Context, identity chat.Identity, taskID int64) (string, bool, error) {
	if _, err := m.controlledTask(ctx, identity, taskID); err != nil {
		return "", false, err
	}
	state, err := m.controlState(ctx, taskID)
	if err != nil {
		return "", false, err
	}
	switch state.Phase {
	case "completion_requested", "approved":
		return state.Summary, true, nil
	case "blocked":
		return "任务已阻塞（" + state.Kind + "）：" + state.Reason, true, nil
	case "review":
		return "任务等待审查：" + state.Summary, true, nil
	default:
		return "", false, nil
	}
}
