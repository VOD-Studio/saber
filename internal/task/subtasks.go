package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
)

// SubagentStatusRunning 是子代理启动后的初始状态；终态复用 agent.Status 的字符串。
const SubagentStatusRunning = "running"

// Subagent 是父任务下子代理的持久化记录。
// 父任务失败或取消时已完成子代理仍保留在这里，供 `!task logs` 与排查使用。
type Subagent struct {
	// ID 是数据库生成的稳定编号。
	ID int64
	// ParentTaskID 是发起 delegate 的父任务编号。
	ParentTaskID int64
	// Goal 是父模型给出的目标原文。
	Goal string
	// Status 为 running 或 agent.Status 的字符串。
	Status string
	// Result 是子代理的正常终态答复；失败时为空。
	Result string
	// Usage 是子运行报告的主请求用量。
	Usage openai.Usage
	// CreatedAt 是启动时间。
	CreatedAt time.Time
	// FinishedAt 是结束时间；仍在运行的子代理为零值。
	FinishedAt time.Time
}

// RecordSubagentStart 写入子代理启动记录并返回编号。
// 表按需创建，因此旧数据库不需要迁移步骤；parentTaskID 必须对应已有父任务。
func (m *Manager) RecordSubagentStart(ctx context.Context, parentTaskID int64, goal string) (int64, error) {
	if parentTaskID <= 0 {
		return 0, errors.New("子代理必须属于一个已持久化的父任务")
	}
	if err := m.ensureSubagentsTable(ctx); err != nil {
		return 0, err
	}
	created := time.Now().UnixMilli()
	result, err := m.store.db.ExecContext(ctx, `INSERT INTO task_subagents
		(parent_task_id, goal, status, result, usage, created_at, finished_at) VALUES (?,?,?,?,?,?,0)`,
		parentTaskID, goal, SubagentStatusRunning, "", "{}", created)
	if err != nil {
		return 0, fmt.Errorf("记录子代理启动失败: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取子代理编号失败: %w", err)
	}
	return id, nil
}

// RecordSubagentFinish 写入子代理终态、结果与用量。
func (m *Manager) RecordSubagentFinish(ctx context.Context, id int64, status, result string, usage openai.Usage) error {
	if id <= 0 {
		return errors.New("子代理编号必须为正数")
	}
	if status == "" {
		status = SubagentStatusRunning
	}
	if err := m.ensureSubagentsTable(ctx); err != nil {
		return err
	}
	usageJSON, err := json.Marshal(usage)
	if err != nil {
		return fmt.Errorf("序列化子代理用量失败: %w", err)
	}
	if _, err = m.store.db.ExecContext(ctx, `UPDATE task_subagents
		SET status=?, result=?, usage=?, finished_at=? WHERE id=?`,
		status, result, string(usageJSON), time.Now().UnixMilli(), id); err != nil {
		return fmt.Errorf("记录子代理终态失败: %w", err)
	}
	return nil
}

// Subagents 按启动顺序返回指定父任务的全部子代理记录。
func (m *Manager) Subagents(ctx context.Context, parentTaskID int64) ([]Subagent, error) {
	if parentTaskID <= 0 {
		return nil, errors.New("父任务编号必须为正数")
	}
	if err := m.ensureSubagentsTable(ctx); err != nil {
		return nil, err
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT id, parent_task_id, goal, status, result, usage, created_at, finished_at
		FROM task_subagents WHERE parent_task_id=? ORDER BY id`, parentTaskID)
	if err != nil {
		return nil, fmt.Errorf("查询子代理失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []Subagent
	for rows.Next() {
		var (
			record     Subagent
			usage      string
			createdAt  int64
			finishedAt int64
		)
		if err = rows.Scan(&record.ID, &record.ParentTaskID, &record.Goal, &record.Status, &record.Result,
			&usage, &createdAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("读取子代理记录失败: %w", err)
		}
		if usage != "" {
			if err = json.Unmarshal([]byte(usage), &record.Usage); err != nil {
				return nil, fmt.Errorf("解析子代理用量失败: %w", err)
			}
		}
		record.CreatedAt = time.UnixMilli(createdAt)
		if finishedAt > 0 {
			record.FinishedAt = time.UnixMilli(finishedAt)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

var errSubagentSchema = errors.New("子代理表不可用")

// ensureSubagentsTable 懒创建子代理表；重复调用安全，不修改既有数据。
func (m *Manager) ensureSubagentsTable(ctx context.Context) error {
	if m == nil || m.store == nil || m.store.db == nil {
		return errSubagentSchema
	}
	if _, err := m.store.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS task_subagents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		parent_task_id INTEGER NOT NULL,
		goal TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'running',
		result TEXT NOT NULL DEFAULT '',
		usage TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS task_subagents_parent ON task_subagents(parent_task_id, id);`); err != nil {
		return fmt.Errorf("创建子代理表失败: %w", err)
	}
	return nil
}
