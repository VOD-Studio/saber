package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// instructionChain 按任务链先后返回真实用户原文，不从摘要或引用文本猜测。
func (m *Manager) instructionChain(ctx context.Context, taskID int64) ([]agent.Instruction, error) {
	rows, err := m.store.db.QueryContext(ctx, `WITH RECURSIVE chain(id,message) AS (
		SELECT id,message FROM tasks WHERE id=?
		UNION ALL
		SELECT p.id,p.message FROM tasks p JOIN task_links l ON l.parent_id=p.id JOIN chain c ON l.task_id=c.id
	) SELECT id,message FROM chain ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var instructions []agent.Instruction
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var message chat.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		if message.Text != "" {
			instructions = append(instructions, agent.Instruction{TaskID: id, Text: message.Text})
		}
	}
	return instructions, rows.Err()
}

// initialTaskState 以真实任务链构造程序状态；仅继承父任务的工作字段。
func (m *Manager) initialTaskState(ctx context.Context, t Task) (agent.TaskState, error) {
	instructions, err := m.instructionChain(ctx, t.ID)
	if err != nil {
		return agent.TaskState{}, err
	}
	state := agent.TaskState{Instructions: instructions}
	if !m.contextInheritance() {
		return state, nil
	}
	var parentID int64
	err = m.store.db.QueryRowContext(ctx, `SELECT parent_id FROM task_links WHERE task_id=?`, t.ID).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return agent.TaskState{}, err
	}
	parentState, _, err := m.store.runtimeState(ctx, parentID)
	if err != nil {
		return agent.TaskState{}, err
	}
	if parentState == nil {
		var raw []byte
		err = m.store.db.QueryRowContext(ctx, `SELECT result FROM tasks WHERE id=?`, parentID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return agent.TaskState{}, err
		}
		if len(raw) > 0 {
			var result agent.Result
			if json.Unmarshal(raw, &result) == nil {
				parentState = result.State
			}
		}
	}
	if parentState != nil {
		state.Plan = append(state.Plan, parentState.Plan...)
		state.Findings = append(state.Findings, parentState.Findings...)
		state.Artifacts = append(state.Artifacts, parentState.Artifacts...)
		state.OpenIssues = append(state.OpenIssues, parentState.OpenIssues...)
	}
	return state, nil
}

// RuntimeState 返回同群可见的任务状态；旧任务没有记录时回退到最终结果或只读初始状态。
func (m *Manager) RuntimeState(ctx context.Context, session chat.Session, taskID int64) (agent.TaskState, error) {
	t, err := m.Get(ctx, session, taskID)
	if err != nil {
		return agent.TaskState{}, err
	}
	state, _, err := m.store.runtimeState(ctx, taskID)
	if err != nil {
		return agent.TaskState{}, err
	}
	if state != nil {
		return *agent.CloneTaskState(state), nil
	}
	if t.Result.State != nil {
		return *agent.CloneTaskState(t.Result.State), nil
	}
	instructions, err := m.instructionChain(ctx, taskID)
	if err != nil {
		return agent.TaskState{}, err
	}
	return agent.TaskState{Instructions: instructions}, nil
}

// markStateInitialized 记录初始化事件，使公共事件流能看到初始计划与指令。
func (m *Manager) markStateInitialized(ctx context.Context, taskID int64, state agent.TaskState) error {
	return m.store.record(ctx, taskID, agent.Event{Kind: agent.TaskStateUpdated, State: agent.CloneTaskState(&state)})
}
