package automation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

func (s *Service) control(ctx context.Context, i chat.Identity, kind string, args map[string]any) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, rev, err := s.load(ctx, i, kind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	action := text(args, "action")
	if action == "status" || action == "show" {
		if rev == 0 {
			return nil, errors.New("no automation set")
		}
		return st, nil
	}
	if action == "set" || action == "draft" {
		prompt := strings.TrimSpace(text(args, "prompt"))
		if prompt == "" || len(prompt) > 64000 {
			return nil, errors.New("prompt must contain 1..64000 bytes")
		}
		if action == "draft" {
			contract, draftErr := s.draft(ctx, prompt)
			if draftErr == nil {
				args["contract"] = contract
			} else {
				prompt += "\n[Completion-contract draft unavailable: " + draftErr.Error() + "]"
			}
		}
		if st.LastTask > 0 {
			t, e := s.tasks.Get(ctx, i.Session, st.LastTask)
			if e != nil {
				return nil, e
			}
			if t.Status == "queued" || t.Status == "running" {
				return nil, errors.New("stop current automation task before replacing objective")
			}
		}
		id, e := newID()
		if e != nil {
			return nil, e
		}
		generation, e := s.tasks.ContextGeneration(ctx, i.Session)
		if e != nil {
			return nil, e
		}
		dir, e := s.host.Workspace(ctx)
		if e != nil {
			return nil, e
		}
		skills, e := stringsArg(args, "skills")
		if e != nil {
			return nil, e
		}
		st = State{ID: id, Kind: kind, Owner: i, Generation: generation, WorkDir: dir, Message: identityMessage(i, prompt), Prompt: prompt, Status: "active", Created: time.Now(), Next: time.Now(), Model: text(args, "model"), Skills: skills, MaxTicks: s.cfg.MaxTicks, Times: int(number(args, "times")), Until: text(args, "until")}
		if st.Times < 0 {
			return nil, errors.New("times must be nonnegative")
		}
		if kind == "goal" {
			st.MaxTicks = s.cfg.GoalMaxTurns
			st.Contract = parseContract(prompt)
			if raw, ok := args["contract"].(map[string]any); ok {
				for k, v := range raw {
					if allowedContract(k) {
						if text, ok := v.(string); ok {
							st.Contract[k] = text
						}
					}
				}
			}
			if raw, ok := args["contract"].(map[string]string); ok {
				st.Contract = raw
			}
		} else {
			durationText := text(args, "interval")
			if durationText == "" && kind == "loop" {
				st.SelfPaced = true
				st.Delay = time.Duration(s.cfg.SelfPacedFloorSeconds) * time.Second
			} else {
				duration, e := parseInterval(durationText)
				if e != nil {
					return nil, e
				}
				floor := time.Duration(s.cfg.MinIntervalSeconds) * time.Second
				if kind == "heartbeat" {
					floor = time.Minute
				}
				if duration < floor || duration > 365*24*time.Hour {
					return nil, errors.New("interval outside allowed range")
				}
				st.Interval, st.Delay = duration, duration
			}
			if kind == "heartbeat" {
				st.Next = time.Now().Add(st.Delay)
			}
		}
		if s.bindings.Prepare == nil {
			return nil, errors.New("automation request preparation unavailable")
		}
		if _, e = s.bindings.Prepare(ctx, st.Message, st.Skills, st.Model); e != nil {
			return nil, e
		}
		if e = s.save(ctx, st, rev); e != nil {
			return nil, e
		}
		return st, nil
	}
	if rev == 0 {
		return nil, errors.New("no automation set")
	}
	switch action {
	case "pause":
		if st.WaitingProcess != nil && st.WaitingProcess.TargetTask > 0 {
			if _, e := s.tasks.Cancel(ctx, i, st.WaitingProcess.TargetTask); e != nil {
				return nil, e
			}
		}
		st.WaitingProcess = nil
		st.Status, st.Reason = "paused", "user-paused"
	case "resume":
		st.Status, st.Reason = "active", ""
		if kind == "goal" {
			st.Ticks = 0
			st.WaitingTask = 0
			st.WaitingProcess = nil
			st.WaitingUntil = time.Time{}
			st.Next = time.Now()
		} else {
			st.Next = time.Now().Add(st.Delay)
		}
	case "clear", "stop":
		if st.WaitingProcess != nil && st.WaitingProcess.TargetTask > 0 {
			if _, e := s.tasks.Cancel(ctx, i, st.WaitingProcess.TargetTask); e != nil {
				return nil, e
			}
		}
		st.WaitingProcess = nil
		st.Status, st.Reason = "cleared", "user-cleared"
	case "wait":
		if kind != "goal" {
			return nil, errors.New("wait requires goal")
		}
		processID := text(args, "process_id")
		if processID != "" {
			source := task.ID(ctx)
			if source <= 0 {
				return nil, errors.New("process wait requires a live source task, never host PID")
			}
			status, e := s.processStatus(ctx, processID)
			if e != nil {
				return nil, e
			}
			if status["status"] != "running" {
				return nil, errors.New("background process already exited")
			}
			st.WaitingProcess = &ProcessBarrier{ID: processID, SourceTask: source}
		} else {
			id := number(args, "task_id")
			if id > 0 {
				t, e := s.tasks.Get(ctx, i.Session, id)
				if e != nil {
					return nil, e
				}
				if t.Message.SenderID != i.SenderID {
					return nil, errors.New("cannot wait on another user's task")
				}
				st.WaitingTask = id
			} else {
				seconds := number(args, "seconds")
				if seconds < 1 || seconds > 1800 {
					return nil, errors.New("wait seconds must be 1..1800")
				}
				st.WaitingUntil = time.Now().Add(time.Duration(seconds) * time.Second)
			}
		}
	case "unwait":
		if st.WaitingProcess != nil && st.WaitingProcess.TargetTask > 0 {
			if _, e := s.tasks.Cancel(ctx, i, st.WaitingProcess.TargetTask); e != nil {
				return nil, e
			}
		}
		st.WaitingProcess = nil
		st.WaitingTask = 0
		st.WaitingUntil = time.Time{}
		st.Next = time.Now()
	case "subgoal":
		if kind != "goal" {
			return nil, errors.New("subgoal requires goal")
		}
		switch text(args, "operation") {
		case "clear":
			st.Subgoals = nil
		case "remove":
			index := int(number(args, "index"))
			if index < 1 || index > len(st.Subgoals) {
				return nil, errors.New("invalid subgoal index")
			}
			st.Subgoals = append(st.Subgoals[:index-1], st.Subgoals[index:]...)
		case "list":
			return st.Subgoals, nil
		default:
			p := strings.TrimSpace(text(args, "prompt"))
			if p == "" || len(st.Subgoals) >= 100 {
				return nil, errors.New("subgoal empty or limit reached")
			}
			st.Subgoals = append(st.Subgoals, p)
		}
	case "gate":
		if kind != "goal" {
			return nil, errors.New("gate requires goal")
		}
		switch text(args, "operation") {
		case "list":
			return st.Gates, nil
		case "clear":
			st.Gates = nil
		case "remove":
			index := int(number(args, "index"))
			if index < 1 || index > len(st.Gates) {
				return nil, errors.New("invalid gate index")
			}
			st.Gates = append(st.Gates[:index-1], st.Gates[index:]...)
		default:
			if s.host.IsAdmin == nil || !s.host.IsAdmin(ctx) {
				return nil, errors.New("adding execution quality gates requires configured administrator")
			}
			command := text(args, "command")
			if command == "" || len(command) > 16000 || len(st.Gates) >= 32 {
				return nil, errors.New("invalid gate command/count")
			}
			if e := s.host.Check(ctx, "exec", false); e != nil {
				return nil, e
			}
			st.Gates = append(st.Gates, Gate{Command: command})
		}
	default:
		return nil, fmt.Errorf("unsupported %s action %q", kind, action)
	}
	if err = s.save(ctx, st, rev); err != nil {
		return nil, err
	}
	return st, nil
}

func parseInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "every "))
	if strings.HasSuffix(raw, "d") {
		d, e := time.ParseDuration(strings.TrimSuffix(raw, "d") + "h")
		return d * 24, e
	}
	return time.ParseDuration(raw)
}
func allowedContract(k string) bool {
	return k == "outcome" || k == "verification" || k == "constraints" || k == "boundaries" || k == "stop_when"
}
func parseContract(prompt string) map[string]string {
	out := map[string]string{}
	aliases := map[string]string{"verify": "verification", "verified by": "verification", "verification": "verification", "constraints": "constraints", "preserve": "constraints", "boundaries": "boundaries", "scope": "boundaries", "stop when": "stop_when", "blocked": "stop_when", "outcome": "outcome"}
	for _, line := range strings.Split(prompt, "\n") {
		prefix, value, found := strings.Cut(line, ":")
		if k, ok := aliases[strings.ToLower(strings.TrimSpace(prefix))]; found && ok {
			out[k] = strings.TrimSpace(value)
		}
	}
	return out
}
func (s *Service) draft(ctx context.Context, prompt string) (map[string]string, error) {
	if s.host.Complete == nil {
		return nil, errors.New("goal judge unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.JudgeTimeoutSeconds)*time.Second)
	defer cancel()
	raw, err := s.host.Complete(ctx, s.cfg.JudgeModel, []openai.ChatCompletionMessage{{Role: "system", Content: "Draft a reviewable completion contract. Return only JSON with string fields outcome,verification,constraints,boundaries,stop_when. Do not invent user authorization or broaden scope."}, {Role: "user", Content: prompt}})
	if err != nil {
		return nil, err
	}
	var contract map[string]string
	err = json.Unmarshal([]byte(stripFence(raw)), &contract)
	return contract, err
}
func stripFence(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		_, raw, _ = strings.Cut(raw, "\n")
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "```"))
	}
	return raw
}

// Tick 恢复已派发的发生，合并错过的定时点，并只在会话空闲时申请普通任务。
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks == nil || s.bindings.Prepare == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revision,data FROM automation_states WHERE kind IN ('goal','loop','heartbeat') ORDER BY CASE kind WHEN 'goal' THEN 0 WHEN 'loop' THEN 1 ELSE 2 END`)
	if err != nil {
		return err
	}
	type item struct {
		st  State
		rev int64
	}
	var items []item
	for rows.Next() {
		var it item
		var data []byte
		if err = rows.Scan(&it.rev, &data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &it.st); err != nil {
			break
		}
		items = append(items, it)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var result error
	goals := map[string]bool{}
	var owners []chat.Identity
	seenOwners := map[string]bool{}
	for _, it := range items {
		st := it.st
		if st.Status != "active" {
			continue
		}
		runCtx := execution.WithTask(chat.WithIdentity(ctx, st.Owner), 0, st.WorkDir)
		if !seenOwners[st.Owner.UserKey()] {
			owners = append(owners, st.Owner)
			seenOwners[st.Owner.UserKey()] = true
		}
		generation, e := s.tasks.ContextGeneration(runCtx, st.Owner.Session)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if generation != st.Generation {
			st.Status, st.Reason = "cleared", "conversation reset or switched"
			result = errors.Join(result, s.save(runCtx, st, it.rev))
			continue
		}
		if st.Attempt != "" && st.LastTask == 0 {
			t, e := s.tasks.TaskForMessage(runCtx, st.Owner.Session, "saber-automation:"+st.ID+":"+st.Attempt)
			if e != nil {
				st.Status, st.Reason = "paused", "prepared occurrence cannot be reconciled; operator review required: "+e.Error()
				result = errors.Join(result, s.save(runCtx, st, it.rev))
				continue
			}
			st.LastTask = t.ID
			if e = s.save(runCtx, st, it.rev); e != nil {
				result = errors.Join(result, e)
				continue
			}
			it.rev++
		}
		if st.LastTask > 0 {
			t, e := s.tasks.Get(runCtx, st.Owner.Session, st.LastTask)
			if e != nil {
				result = errors.Join(result, e)
				continue
			}
			if st.Attempt != "" {
				if t.Status == "queued" || t.Status == "running" || t.Status == "waiting_input" || t.Status == "blocked" || t.Status == "review" {
					if st.Kind == "goal" {
						goals[stateKey(st.Owner)] = true
					}
					continue
				}
				s.complete(runCtx, &st, t, now, false)
				st.Attempt = ""
				if e = s.save(runCtx, st, it.rev); e != nil {
					result = errors.Join(result, e)
					continue
				}
				it.rev++
			}
		}
		if st.Status != "active" {
			continue
		}
		if st.WaitingTask > 0 {
			t, e := s.tasks.Get(runCtx, st.Owner.Session, st.WaitingTask)
			if e != nil {
				st.Status, st.Reason = "paused", e.Error()
				result = errors.Join(result, s.save(runCtx, st, it.rev))
				continue
			}
			if t.Status == "queued" || t.Status == "running" || t.Status == "waiting_input" || t.Status == "blocked" || t.Status == "review" {
				continue
			}
			st.WaitingTask = 0
		}
		if !st.WaitingUntil.IsZero() {
			if now.Before(st.WaitingUntil) {
				continue
			}
			st.WaitingUntil = time.Time{}
		}
		if st.Kind == "goal" {
			goals[stateKey(st.Owner)] = true
		} else if goals[stateKey(st.Owner)] {
			continue
		}
		if now.Before(st.Next) {
			continue
		}
		idle, e := s.tasks.SessionIdle(runCtx, st.Owner.Session)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if !idle {
			continue
		}
		permission := "automation_" + st.Kind
		if e = s.host.Check(runCtx, permission, false); e != nil {
			st.Status, st.Reason = "paused", e.Error()
			result = errors.Join(result, s.save(runCtx, st, it.rev))
			continue
		}
		dir, e := s.host.Workspace(runCtx)
		if e != nil || dir != st.WorkDir {
			st.Status, st.Reason = "paused", "workspace authorization changed"
			result = errors.Join(result, s.save(runCtx, st, it.rev))
			continue
		}
		msg := st.Message
		msg.Text = s.render(st)
		msg.ID = ""
		msg.ReplyTo = ""
		msg.ControlText = nil
		msg.Attachments = nil
		req, e := s.bindings.Prepare(runCtx, msg, st.Skills, st.Model)
		if e != nil {
			st.Status, st.Reason = "paused", e.Error()
			result = errors.Join(result, s.save(runCtx, st, it.rev))
			continue
		}
		if st.Kind == "loop" && (strings.HasPrefix(strings.TrimSpace(st.Prompt), "/") || strings.HasPrefix(strings.TrimSpace(st.Prompt), "!")) {
			if recursiveCommand(st.Prompt) || s.bindings.Command == nil {
				st.Status, st.Reason = "paused", "recurring command is recursive or dispatcher unavailable"
				result = errors.Join(result, s.save(runCtx, st, it.rev))
				continue
			}
			req.Messages = append([]openai.ChatCompletionMessage{{Role: "system", Name: "saber_session_command", Content: st.Prompt}}, req.Messages...)
		}
		attempt, e := newID()
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		st.Attempt = attempt
		st.LastTask = 0
		st.Ticks++
		st.Next = now.Add(st.Delay)
		if e = s.save(runCtx, st, it.rev); e != nil {
			result = errors.Join(result, e)
			continue
		}
		it.rev++
		msg.ID = "saber-automation:" + st.ID + ":" + attempt
		t, e := s.tasks.SubmitAutomationAtGeneration(runCtx, msg, st.WorkDir, req, true, st.Generation)
		if e != nil {
			st.Attempt = ""
			st.Ticks--
			st.Status, st.Reason = "paused", e.Error()
			result = errors.Join(result, s.save(runCtx, st, it.rev))
			continue
		}
		st.LastTask = t.ID
		result = errors.Join(result, s.save(runCtx, st, it.rev))
	}
	result = errors.Join(result, s.tickCron(ctx), s.tickCurator(ctx, now, owners))
	return result
}

func (s *Service) render(st State) string {
	switch st.Kind {
	case "heartbeat":
		return "[Heartbeat: recurring instruction; check fresh state, report only meaningful changes. Do not invent work. If nothing to report return NO_REPLY.]\n" + st.Prompt
	case "loop":
		return fmt.Sprintf("[Loop wakeup %d: check fresh state. When finished or moot, end with LOOP_COMPLETE on its own line.]\n%s\nStop condition: %s", st.Ticks+1, st.Prompt, st.Until)
	default:
		contract, _ := json.Marshal(st.Contract)
		return fmt.Sprintf("[Continue standing goal; preserve scope and verify with concrete evidence.]\n%s\nCompletion contract: %s\nAdditional criteria: %s\nPrevious judge/gate evidence: %s", st.Prompt, contract, strings.Join(st.Subgoals, "\n"), st.Reason)
	}
}

var digestTimes = regexp.MustCompile(`\d{1,2}:\d{2}(:\d{2})?|\d{4}-\d{2}-\d{2}|\b\d+(\.\d+)?\s*(s|sec|secs|seconds|m|min|mins|minutes|h|hr|hrs|hours)\b`)

func responseDigest(raw string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(digestTimes.ReplaceAllString(strings.ToLower(raw), "")), " ")))
	return hex.EncodeToString(sum[:])
}
func completeMarker(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if strings.Trim(strings.TrimSpace(line), ".!") == "LOOP_COMPLETE" {
			return true
		}
	}
	return false
}

func (s *Service) complete(ctx context.Context, st *State, t task.Task, now time.Time, live bool) {
	if t.Status != "completed" {
		st.Status, st.Reason = "paused", t.Status+": "+t.Error
		return
	}
	if st.Kind == "heartbeat" {
		st.Next = now.Add(st.Delay)
		return
	}
	if st.Kind == "loop" && completeMarker(t.Result.Content) {
		st.Status, st.Reason = "done", "agent signaled completion"
		return
	}
	if st.Kind == "goal" && len(st.Gates) > 0 && !live {
		st.Reason = "recovered task result; quality gates deferred to next live authorized turn"
		st.Next = now
		return
	}
	if st.Kind == "goal" || st.Until != "" {
		ctx = execution.WithTask(ctx, t.ID, t.WorkDir)
		if st.Kind == "goal" {
			for index := range st.Gates {
				g := &st.Gates[index]
				gctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				value, err := s.host.Call(gctx, "exec", map[string]any{"command": g.Command, "timeout": 300})
				cancel()
				g.Passed = err == nil && successfulTool(value)
				encoded, _ := json.Marshal(value)
				g.Output = tail(string(encoded), 3000)
				if !g.Passed {
					g.Failures++
					st.Reason = "quality gate failed: " + g.Output
					if err != nil {
						st.Reason += " " + err.Error()
					}
					if g.Failures >= 3 {
						st.Status = "paused"
					}
					st.Next = now
					return
				}
			}
		}
		verdict, reason, wait, parseFail, transportFail := s.judge(ctx, *st, t, live)
		if parseFail {
			st.ParseFailures++
		} else {
			st.ParseFailures = 0
		}
		if transportFail {
			st.TransportFailures++
		} else {
			st.TransportFailures = 0
		}
		st.Reason = reason
		if st.ParseFailures >= 3 || st.TransportFailures >= 5 {
			st.Status = "paused"
			return
		}
		switch verdict {
		case "done":
			st.Status = "done"
			return
		case "blocked":
			st.Status = "paused"
			return
		case "wait":
			if st.Kind == "goal" {
				if wait.Process != "" && live {
					status, e := s.processStatus(ctx, wait.Process)
					if e == nil && status["status"] == "running" {
						st.WaitingProcess = &ProcessBarrier{ID: wait.Process, SourceTask: t.ID}
					} else {
						st.Reason = "judge requested unavailable process; continuing"
					}
				} else if wait.Task > 0 {
					target, e := s.tasks.Get(ctx, st.Owner.Session, wait.Task)
					if e == nil && target.Message.SenderID == st.Owner.SenderID {
						st.WaitingTask = wait.Task
					}
				} else if wait.Seconds > 0 && wait.Seconds <= 1800 {
					st.WaitingUntil = now.Add(time.Duration(wait.Seconds) * time.Second)
				}
			}
		}
	}
	if st.Times > 0 && st.Ticks >= st.Times {
		st.Status, st.Reason = "done", "requested run count reached"
		return
	}
	if st.MaxTicks > 0 && st.Ticks >= st.MaxTicks {
		st.Status, st.Reason = "paused", "turn/tick budget exhausted"
		return
	}
	if st.Kind == "loop" && st.SelfPaced {
		digest := responseDigest(t.Result.Content)
		floor := time.Duration(s.cfg.SelfPacedFloorSeconds) * time.Second
		if digest == st.Digest {
			st.Delay = min(max(st.Delay, floor)*2, time.Duration(s.cfg.SelfPacedCeilingSeconds)*time.Second)
		} else {
			st.Delay = floor
		}
		st.Digest = digest
	}
	st.Next = now.Add(st.Delay)
}
func tail(raw string, n int) string {
	if len(raw) > n {
		return raw[len(raw)-n:]
	}
	return raw
}
func successfulTool(value any) bool {
	m, err := toolObject(value)
	if err != nil {
		return false
	}
	exit, valid := m["exit_code"].(float64)
	if !valid || exit != 0 {
		return false
	}
	if v, ok := m["success"].(bool); ok && !v {
		return false
	}
	if v := m["error"]; v != nil && v != "" {
		return false
	}
	if v, ok := m["is_error"].(bool); ok && v {
		return false
	}
	return true
}

type waitDirective struct {
	Task    int64  `json:"wait_on_task"`
	Seconds int    `json:"wait_for_seconds"`
	Process string `json:"wait_on_process"`
}

func (s *Service) judge(ctx context.Context, st State, t task.Task, live bool) (string, string, waitDirective, bool, bool) {
	var wait waitDirective
	if s.host.Complete == nil {
		return "continue", "judge unavailable", wait, false, true
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.JudgeTimeoutSeconds)*time.Second)
	defer cancel()
	objective := st.Prompt
	if st.Kind == "loop" {
		objective = st.Until
	}
	evidence, _ := json.Marshal(t.Result.Rounds)
	contract, _ := json.Marshal(st.Contract)
	var processes any
	if live {
		processes, _ = s.liveProcesses(ctx)
	}
	processJSON, _ := json.Marshal(processes)
	raw, err := s.host.Complete(ctx, s.cfg.JudgeModel, []openai.ChatCompletionMessage{{Role: "system", Content: "Judge actual evidence against every goal criterion. Return JSON: {\"verdict\":\"done|blocked|continue|wait\",\"reason\":\"...\",\"wait_on_task\":0,\"wait_for_seconds\":0,\"wait_on_process\":\"\"}. done requires concrete verification evidence, never a claim alone. blocked means impossible or needs user decision; wait requires an authorized live process id from the supplied list, an authorized task, or <=1800 seconds. Never invent a PID or process."}, {Role: "user", Content: fmt.Sprintf("Objective: %s\nContract: %s\nExtra criteria: %s\nFinal response: %s\nActual tool/model evidence: %s\nOwned live processes: %s", objective, contract, strings.Join(st.Subgoals, "\n"), tail(t.Result.Content, 4000), tail(string(evidence), 16000), processJSON)}})
	if err != nil {
		return "continue", "judge transport error: " + err.Error(), wait, false, true
	}
	var out struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
		waitDirective
	}
	if err = json.Unmarshal([]byte(stripFence(raw)), &out); err != nil {
		return "continue", "judge parse error: " + err.Error(), wait, true, false
	}
	switch out.Verdict {
	case "done", "blocked", "continue":
		return out.Verdict, out.Reason, out.waitDirective, false, false
	case "wait":
		if out.Task > 0 || out.Seconds > 0 || out.Process != "" {
			return out.Verdict, out.Reason, out.waitDirective, false, false
		}
	}
	return "continue", "invalid judge verdict", wait, true, false
}
