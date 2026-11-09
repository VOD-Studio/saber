package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

// Question 保存跨重启可读的问题、真实选项及受授权用户锁定的答案。
type Question struct {
	ID           int64     `json:"id"`
	TaskID       int64     `json:"task_id"`
	Question     string    `json:"question"`
	Choices      []string  `json:"choices,omitempty"`
	MultiSelect  bool      `json:"multi_select"`
	Status       string    `json:"status"`
	Answer       any       `json:"answer"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	ResumeTaskID int64     `json:"resume_task_id,omitempty"`
}

const questionColumns = `id,task_id,question,choices,multi_select,status,answer,created_at,expires_at,resume_task_id`

func scanQuestion(row interface{ Scan(...any) error }) (Question, error) {
	var q Question
	var choices, answer []byte
	var created, expires int64
	if err := row.Scan(&q.ID, &q.TaskID, &q.Question, &choices, &q.MultiSelect, &q.Status, &answer, &created, &expires, &q.ResumeTaskID); err != nil {
		return q, err
	}
	q.CreatedAt, q.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
	return q, errors.Join(json.Unmarshal(choices, &q.Choices), json.Unmarshal(answer, &q.Answer))
}

func (m *Manager) questions(ctx context.Context, taskID int64) ([]Question, error) {
	rows, err := m.store.db.QueryContext(ctx, `SELECT `+questionColumns+` FROM task_questions WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	questions := []Question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

// QuestionTool 让模型发起持久澄清，只有真实用户入口可以锁定答案或恢复中断执行。
func (m *Manager) QuestionTool(ctx context.Context, args map[string]any) (any, error) {
	if !m.control.Enabled {
		return nil, errors.New("task control is disabled")
	}
	identity, err := controlIdentity(ctx)
	if err != nil {
		return nil, err
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "ask"
	}
	if action == "answer" || action == "cancel" {
		if ID(ctx) > 0 {
			return nil, errors.New("代理不能代替用户回答或取消澄清")
		}
		return m.answerQuestion(ctx, identity, args, action == "cancel")
	}
	if action == "list" && args["task_id"] == nil && ID(ctx) == 0 {
		rows, err := m.store.db.QueryContext(ctx, `SELECT q.id,q.task_id,q.question,q.choices,q.multi_select,q.status,q.answer,q.created_at,q.expires_at,q.resume_task_id FROM task_questions q JOIN tasks t ON t.id=q.task_id WHERE t.platform=? AND t.account=? AND t.room=? AND t.sender=? AND json_extract(t.message,'$.Session.Thread')=? ORDER BY q.id DESC LIMIT 100`, append(scope(identity.Session), identity.SenderID, identity.Session.Thread)...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		questions := []Question{}
		for rows.Next() {
			q, err := scanQuestion(rows)
			if err != nil {
				return nil, err
			}
			questions = append(questions, q)
		}
		return questions, rows.Err()
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
	case "list":
		return m.questions(ctx, id)
	case "ask":
		questions, err := m.createQuestions(ctx, t, args)
		if err != nil {
			return nil, err
		}
		wait, _ := args["wait"].(bool)
		if !wait {
			return questions, nil
		}
		if ID(ctx) != id || t.Status != "running" {
			return nil, errors.New("只有当前运行任务可以等待澄清；用户入口请查询持久问题")
		}
		return m.waitQuestions(ctx, t.ID, questions)
	case "resume":
		if ID(ctx) > 0 {
			return nil, errors.New("恢复中断澄清必须由真实用户发起")
		}
		questions, err := m.questions(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(questions) == 0 {
			return nil, errors.New("task has no questions")
		}
		for _, q := range questions {
			if q.Status == "pending" {
				return nil, errors.New("请先回答或跳过所有澄清问题")
			}
		}
		data, err := json.Marshal(clarificationResult(questions))
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if t.Status == "running" {
			return nil, errors.New("运行中的任务会直接收到答案，无需重复恢复")
		}
		resumed, err := m.resumeControlLocked(ctx, identity, t, "用户澄清结果："+string(data))
		if err != nil {
			return nil, err
		}
		_, err = m.store.db.ExecContext(ctx, `UPDATE task_questions SET resume_task_id=? WHERE task_id=?`, resumed.ID, t.ID)
		return resumed, err
	default:
		return nil, fmt.Errorf("unknown question action %q", action)
	}
}

func (m *Manager) createQuestions(ctx context.Context, t Task, args map[string]any) ([]Question, error) {
	if t.Status != "running" && t.Status != "queued" {
		return nil, ErrTaskFinished
	}
	var input []struct {
		Question    string   `json:"question"`
		Choices     []string `json:"choices"`
		MultiSelect bool     `json:"multi_select"`
	}
	if t.Status == "running" && ID(ctx) != t.ID {
		return nil, errors.New("真实用户入口不能在正在执行的任务中伪造模型澄清；只能回答已有问题")
	}
	data, err := json.Marshal(args["questions"])
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &input); err != nil || len(input) < 1 || len(input) > 5 {
		return nil, errors.New("questions must contain 1..5 independent questions")
	}
	for i := range input {
		input[i].Question = strings.TrimSpace(input[i].Question)
		if input[i].Question == "" || len(input[i].Question) > 32768 || len(input[i].Choices) > 4 {
			return nil, errors.New("问题不能为空或超过32768字节，每题至多4个选项")
		}
		seen := map[string]bool{}
		for j, choice := range input[i].Choices {
			choice = strings.TrimSpace(choice)
			if choice == "" || len(choice) > 32000 || seen[choice] {
				return nil, errors.New("选项不能为空、重复或超过32000字节")
			}
			seen[choice], input[i].Choices[j] = true, choice
		}
		if input[i].MultiSelect && len(input[i].Choices) == 0 {
			return nil, errors.New("multi_select requires choices")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM task_questions WHERE task_id=? AND status='pending'`, t.ID).Scan(&pending); err != nil {
		return nil, err
	}
	limit := m.control.MaxPendingQuestions
	if limit == 0 {
		limit = 20
	}
	if pending+len(input) > limit {
		return nil, errors.New("too many pending questions")
	}
	timeout := m.control.QuestionTimeoutSeconds
	if timeout == 0 {
		timeout = 86400
	}
	now := time.Now()
	questions := make([]Question, 0, len(input))
	for _, entry := range input {
		choices, err := json.Marshal(entry.Choices)
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO task_questions(task_id,question,choices,multi_select,created_at,expires_at) VALUES(?,?,?,?,?,?)`, t.ID, entry.Question, choices, entry.MultiSelect, now.UnixMilli(), now.Add(time.Duration(timeout)*time.Second).UnixMilli())
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		questions = append(questions, Question{ID: id, TaskID: t.ID, Question: entry.Question, Choices: entry.Choices, MultiSelect: entry.MultiSelect, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Duration(timeout) * time.Second)})
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id,phase,revision,updated_at) VALUES(?,'waiting_input',1,?) ON CONFLICT(task_id) DO UPDATE SET phase='waiting_input',revision=task_control.revision+1,updated_at=excluded.updated_at`, t.ID, now.UnixMilli()); err != nil {
		return nil, err
	}
	if err := controlJournal(ctx, tx, t.ID, map[string]any{"phase": "waiting_input", "questions": questions}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	m.notify()
	return questions, nil
}

func (m *Manager) answerQuestion(ctx context.Context, identity chat.Identity, args map[string]any, cancel bool) (Question, error) {
	id, err := controlID(args, "question_id", 0)
	if err != nil {
		return Question{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	q, err := scanQuestion(m.store.db.QueryRowContext(ctx, `SELECT `+questionColumns+` FROM task_questions WHERE id=?`, id))
	if err != nil {
		return Question{}, err
	}
	if _, err = m.controlledTask(ctx, identity, q.TaskID); err != nil {
		return Question{}, err
	}
	status := "answered"
	answer := args["answer"]
	if cancel {
		status, answer = "cancelled", nil
	} else if answer == nil {
		status = "skipped"
	} else if q.MultiSelect {
		data, err := json.Marshal(answer)
		if err != nil {
			return Question{}, err
		}
		var selections []string
		if err = json.Unmarshal(data, &selections); err != nil || len(selections) == 0 || len(selections) > 20 {
			return Question{}, errors.New("multi-select answer must contain 1..20 strings")
		}
		for _, selection := range selections {
			if strings.TrimSpace(selection) == "" || len(selection) > 32768 {
				return Question{}, errors.New("invalid multi-select answer")
			}
		}
		answer = selections
	} else {
		text, ok := answer.(string)
		if !ok || len(text) > 32768 {
			return Question{}, errors.New("answer must be text of at most 32768 bytes")
		}
		answer = strings.TrimSpace(text)
		if answer == "" {
			status, answer = "skipped", nil
		}
	}
	data, err := json.Marshal(answer)
	if err != nil {
		return Question{}, err
	}
	if q.Status != "pending" {
		existing, _ := json.Marshal(q.Answer)
		if q.Status == status && string(existing) == string(data) {
			return q, nil
		}
		return Question{}, errors.New("问题答案已锁定或已过期，不能覆盖")
	}
	if !q.ExpiresAt.After(time.Now()) {
		return Question{}, errors.New("question has expired")
	}
	if _, err = m.store.db.ExecContext(ctx, `UPDATE task_questions SET status=?,answer=?,answered_at=? WHERE id=? AND status='pending'`, status, data, time.Now().UnixMilli(), id); err != nil {
		return Question{}, err
	}
	if _, err = m.store.db.ExecContext(ctx, `UPDATE task_control SET phase='',revision=revision+1 WHERE task_id=? AND phase='waiting_input' AND NOT EXISTS(SELECT 1 FROM task_questions WHERE task_id=? AND status='pending')`, q.TaskID, q.TaskID); err != nil {
		return Question{}, err
	}
	q.Status, q.Answer = status, answer
	m.notify()
	return q, nil
}

func clarificationResult(questions []Question) map[string]any {
	responses := make([]map[string]any, 0, len(questions))
	outcome := "submitted"
	for _, q := range questions {
		status := q.Status
		if status == "pending" || status == "cancelled" || status == "timed_out" {
			status = "unanswered"
		}
		if q.Status == "cancelled" {
			outcome = "cancelled"
		}
		if q.Status == "timed_out" && outcome != "cancelled" {
			outcome = "timed_out"
		}
		responses = append(responses, map[string]any{"question_id": q.ID, "question": q.Question, "choices_offered": q.Choices, "status": status, "user_response": q.Answer})
	}
	return map[string]any{"responses": responses, "outcome": outcome}
}

func (m *Manager) waitQuestions(ctx context.Context, taskID int64, requested []Question) (any, error) {
	wanted := make(map[int64]bool, len(requested))
	for _, q := range requested {
		wanted[q.ID] = true
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		questions, err := m.questions(context.WithoutCancel(ctx), taskID)
		if err != nil {
			return nil, err
		}
		selected := make([]Question, 0, len(requested))
		pending := false
		for _, q := range questions {
			if wanted[q.ID] {
				selected = append(selected, q)
				pending = pending || q.Status == "pending"
			}
		}
		if !pending {
			_, err := m.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE task_control SET phase='',revision=revision+1 WHERE task_id=? AND phase='waiting_input'`, taskID)
			return clarificationResult(selected), err
		}
		select {
		case <-ctx.Done():
			return clarificationResult(selected), ctx.Err()
		case <-ticker.C:
		}
	}
}
