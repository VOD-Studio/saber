// Package batch 从持久任务导出真实 ShareGPT 轨迹、检查点及评估；重启不重放未知副作用。
package batch

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// Options 是可信 CLI/API 的批量参数；不含 provider URL、凭据或任意镜像配置。
type Options struct {
	DatasetFile       string   `json:"dataset_file"`
	Dataset           []Sample `json:"dataset,omitempty"`
	RunName           string   `json:"run_name"`
	Model             string   `json:"model,omitempty"`
	Distribution      string   `json:"distribution,omitempty"`
	BatchSize         int      `json:"batch_size,omitempty"`
	MaxTurns          int      `json:"max_turns,omitempty"`
	ReasoningEffort   string   `json:"reasoning_effort,omitempty"`
	Workers           int      `json:"workers,omitempty"`
	ListDistributions bool     `json:"list_distributions,omitempty"`
	Resume            bool     `json:"resume,omitempty"`
	RetryFailed       bool     `json:"retry_failed,omitempty"`
	Evaluate          bool     `json:"evaluate,omitempty"`
	Compress          bool     `json:"compress,omitempty"`
	MaxSamples        int      `json:"max_samples,omitempty"`
}

// Sample 是一条数据集输入；prompt 或 ShareGPT/messages 中首条 human/user 消息必须存在。
type Sample struct {
	Prompt        string                         `json:"prompt"`
	Messages      []openai.ChatCompletionMessage `json:"messages,omitempty"`
	Conversations []Conversation                 `json:"conversations,omitempty"`
	CWD           string                         `json:"cwd,omitempty"`
	Image         string                         `json:"image,omitempty"`
	DockerImage   string                         `json:"docker_image,omitempty"`
	Expected      string                         `json:"expected,omitempty"`
	Rubric        string                         `json:"rubric,omitempty"`
}

// Conversation 保留 ShareGPT 角色、工具调用和公开推理摘要。
type Conversation struct {
	From       string            `json:"from"`
	Value      string            `json:"value"`
	ToolCalls  []openai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Reasoning  string            `json:"reasoning,omitempty"`
}

// ToolStats 统计实际工具记录，不从回复文本猜测执行成功。
type ToolStats struct {
	Count   int `json:"count"`
	Success int `json:"success"`
	Failure int `json:"failure"`
}

// Evaluation 保存真实规则/辅助模型评估以及失败原因。
type Evaluation struct {
	Score  float64 `json:"score"`
	Passed bool    `json:"passed"`
	Reason string  `json:"reason"`
	Error  string  `json:"error,omitempty"`
}

// Trajectory 是可用于训练和评估的完整任务轨迹。
type Trajectory struct {
	PromptIndex    int                  `json:"prompt_index"`
	Prompt         string               `json:"prompt"`
	Key            string               `json:"key"`
	TaskID         int64                `json:"task_id"`
	Conversations  []Conversation       `json:"conversations"`
	Completed      bool                 `json:"completed"`
	Partial        bool                 `json:"partial"`
	Failed         bool                 `json:"failed"`
	Error          string               `json:"error,omitempty"`
	APICalls       int                  `json:"api_calls"`
	ToolsUsed      []string             `json:"toolsets_used"`
	ToolStats      map[string]ToolStats `json:"tool_stats"`
	ToolErrors     map[string]int       `json:"tool_error_counts"`
	AssistantTurns int                  `json:"total_assistant_turns"`
	ReasoningTurns int                  `json:"turns_with_reasoning"`
	Discarded      string               `json:"discarded,omitempty"`
	Evaluation     *Evaluation          `json:"evaluation,omitempty"`
	Checkpoint     *agent.Checkpoint    `json:"compression_checkpoint,omitempty"`
	Metadata       map[string]any       `json:"metadata"`
}

// Summary 是真实导出计数与产物位置，不将后台派发计为成功。
type Summary struct {
	RunName          string                        `json:"run_name"`
	Total            int                           `json:"total"`
	Completed        int                           `json:"completed"`
	Failed           int                           `json:"failed"`
	Discarded        int                           `json:"discarded"`
	ToolStats        map[string]ToolStats          `json:"tool_stats"`
	AssistantTurns   int                           `json:"assistant_turns"`
	ReasoningTurns   int                           `json:"reasoning_turns"`
	Evaluated        int                           `json:"evaluated"`
	EvaluationErrors int                           `json:"evaluation_errors"`
	Passed           int                           `json:"passed"`
	Files            []string                      `json:"files"`
	Distributions    map[string]map[string]float64 `json:"distributions,omitempty"`
}

type admission struct {
	Event      string      `json:"event"`
	TaskID     int64       `json:"task_id"`
	State      string      `json:"state"`
	Trajectory *Trajectory `json:"trajectory,omitempty"`
}
type checkpoint struct {
	Owner        chat.Identity        `json:"owner"`
	Model        string               `json:"model"`
	Distribution string               `json:"distribution"`
	ToolNames    []string             `json:"tool_names"`
	Admissions   map[string]admission `json:"admissions"`
	Updated      time.Time            `json:"updated"`
}

// Runner 复用任务管理器与宿主授权；同一进程每个 run 只允许一个消费者。
type Runner struct {
	cfg     config.BatchConfig
	host    toolkit.Host
	tasks   *task.Manager
	prepare func(context.Context, chat.Message, string) (agent.Request, error)
	mu      sync.Mutex
	active  map[string]bool
}

// New 装配真实任务和请求端口，不自行启动 worker 或备用模型。
func New(cfg config.BatchConfig, host toolkit.Host, m *task.Manager, prepare func(context.Context, chat.Message, string) (agent.Request, error)) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if host.StateDir == "" || m == nil || prepare == nil {
		return nil, errors.New("batch requires private state, task manager and request preparation")
	}
	return &Runner{cfg: cfg, host: host, tasks: m, prepare: prepare, active: map[string]bool{}}, nil
}

var runPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,80}$`)

func hash(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func privateWrite(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicBytes(path, data)
}
func atomicBytes(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".batch-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func loadSamples(reader io.Reader, maxBytes int64, maxSamples int) ([]Sample, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), int(min(maxBytes+1, 64<<20)))
	var out []Sample
	var bytes int64
	for scanner.Scan() {
		line := scanner.Bytes()
		bytes += int64(len(line)) + 1
		if bytes > maxBytes {
			return nil, errors.New("dataset exceeds input limit")
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var sample Sample
		if err := json.Unmarshal(line, &sample); err != nil {
			return nil, fmt.Errorf("dataset row %d: %w", len(out)+1, err)
		}
		out = append(out, sample)
		if len(out) > maxSamples {
			return nil, errors.New("dataset exceeds sample limit")
		}
	}
	return out, scanner.Err()
}

// ReadDataset 供可信 CLI 从用户明确给出的文件读取；模型入口必须另经 workspace read_file 授权。
func ReadDataset(path string, maxBytes int64, maxSamples int) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	samples, err := loadSamples(f, maxBytes, maxSamples)
	return samples, errors.Join(err, f.Close())
}

func promptOf(sample Sample) string {
	if p := strings.TrimSpace(sample.Prompt); p != "" {
		return p
	}
	for _, m := range sample.Messages {
		if m.Role == openai.ChatMessageRoleUser && strings.TrimSpace(m.Content) != "" {
			return strings.TrimSpace(m.Content)
		}
	}
	for _, m := range sample.Conversations {
		if m.From == "human" || m.From == "user" {
			return strings.TrimSpace(m.Value)
		}
	}
	return ""
}
func filterTools(tools []openai.Tool, weights map[string]float64) ([]openai.Tool, error) {
	if len(weights) == 0 {
		return tools, nil
	}
	var selected []openai.Tool
	var eligible []openai.Tool
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		p, ok := weights[tool.Function.Name]
		if !ok {
			continue
		}
		eligible = append(eligible, tool)
		n, err := rand.Int(rand.Reader, big.NewInt(1<<53))
		if err != nil {
			return nil, err
		}
		if float64(n.Int64())/float64(1<<53) < p {
			selected = append(selected, tool)
		}
	}
	if len(eligible) == 0 {
		return nil, errors.New("distribution has no currently authorized tools")
	}
	if len(selected) == 0 {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(eligible))))
		if err != nil {
			return nil, err
		}
		selected = append(selected, eligible[index.Int64()])
	}
	return selected, nil
}

// Run 使用稳定内容键恢复已知任务；未知 prepared 发生暂停，不以 resume 自动重放。
func (r *Runner) Run(ctx context.Context, opts Options) (summary Summary, runErr error) {
	if !r.cfg.Enabled {
		return summary, errors.New("batch disabled")
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return summary, errors.New("batch requires trusted identity")
	}
	if r.host.Check == nil {
		return summary, errors.New("batch authorization unavailable")
	}
	if err := r.host.Check(ctx, "batch_run", false); err != nil {
		return summary, err
	}
	if opts.ListDistributions {
		return Summary{Distributions: r.cfg.Distributions}, nil
	}
	if !runPattern.MatchString(opts.RunName) {
		return summary, errors.New("invalid run_name")
	}
	if opts.Distribution == "" {
		opts.Distribution = "default"
	}
	weights, ok := r.cfg.Distributions[opts.Distribution]
	if !ok {
		return summary, errors.New("unknown batch tool distribution")
	}
	dir, err := r.host.Workspace(ctx)
	if err != nil {
		return summary, err
	}
	runDir := filepath.Join(r.host.StateDir, "batches", hash(i.UserKey() + string(i.Session.Key()))[:32], opts.RunName)
	r.mu.Lock()
	if r.active[runDir] {
		r.mu.Unlock()
		return summary, errors.New("batch run already active")
	}
	r.active[runDir] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.active, runDir); r.mu.Unlock() }()
	if err = os.MkdirAll(runDir, 0700); err != nil {
		return summary, err
	}
	lock, err := os.OpenFile(filepath.Join(runDir, ".lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return summary, errors.New("batch run locked; after verifying old process stopped, remove private .lock and resume")
	}
	if err = lock.Close(); err != nil {
		return summary, err
	}
	defer func() { _ = os.Remove(filepath.Join(runDir, ".lock")) }()
	samples := opts.Dataset
	if len(samples) == 0 {
		if opts.DatasetFile == "" {
			return summary, errors.New("dataset required")
		}
		if err = r.host.Check(ctx, "read_file", false); err != nil {
			return summary, err
		}
		root, e := os.OpenRoot(dir)
		if e != nil {
			return summary, e
		}
		relative := opts.DatasetFile
		if filepath.IsAbs(relative) {
			relative, e = filepath.Rel(dir, relative)
		}
		if e != nil {
			_ = root.Close()
			return summary, e
		}
		f, e := root.Open(relative)
		if e != nil {
			_ = root.Close()
			return summary, e
		}
		samples, err = loadSamples(f, r.cfg.MaxInputBytes, r.cfg.MaxSamples)
		err = errors.Join(err, f.Close(), root.Close())
		if err != nil {
			return summary, err
		}
	}
	if len(samples) > r.cfg.MaxSamples {
		return summary, errors.New("too many samples")
	}
	if opts.MaxSamples > 0 && len(samples) > opts.MaxSamples {
		samples = samples[:opts.MaxSamples]
	}
	if len(samples) == 0 {
		return summary, errors.New("dataset empty")
	}
	input, e := json.Marshal(samples)
	if e != nil {
		return summary, e
	}
	if int64(len(input)) > r.cfg.MaxInputBytes {
		return summary, errors.New("dataset exceeds input size limit")
	}
	if opts.MaxSamples < 0 {
		return summary, errors.New("max_samples must be nonnegative")
	}
	cpPath := filepath.Join(runDir, "checkpoint.json")
	cp := checkpoint{Owner: i, Model: opts.Model, Distribution: opts.Distribution, Admissions: map[string]admission{}}
	defer func() {
		if ctx.Err() != nil {
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			for _, record := range cp.Admissions {
				if record.TaskID == 0 {
					continue
				}
				t, e := r.tasks.Get(cancelCtx, i.Session, record.TaskID)
				if e != nil {
					runErr = errors.Join(runErr, e)
					continue
				}
				if t.Status == "queued" || t.Status == "running" {
					_, e = r.tasks.Cancel(cancelCtx, i, record.TaskID)
					runErr = errors.Join(runErr, e)
				}
			}
		}
	}()
	data, readErr := os.ReadFile(cpPath)
	if readErr == nil {
		if !opts.Resume {
			return summary, errors.New("run exists; use resume or a new run_name")
		}
		if err = json.Unmarshal(data, &cp); err != nil {
			return summary, err
		}
		if cp.Owner != i {
			return summary, errors.New("batch owner mismatch")
		}
		if cp.Model != opts.Model || cp.Distribution != opts.Distribution {
			return summary, errors.New("resume model/distribution must match original run")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return summary, readErr
	}
	if err = r.recoverOutputs(runDir, &cp); err != nil {
		return summary, err
	}
	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = r.cfg.BatchSize
	}
	if batchSize < 1 || batchSize > 1000 {
		return summary, errors.New("invalid batch size")
	}
	workerCount := opts.Workers
	if workerCount == 0 {
		workerCount = r.cfg.MaxConcurrent
	}
	if workerCount < 1 || workerCount > r.cfg.MaxConcurrent {
		return summary, errors.New("workers exceeds configured concurrent task limit")
	}
	counts := map[string]int{}
	keys := make([]string, len(samples))
	for index, sample := range samples {
		prompt := promptOf(sample)
		if prompt == "" {
			return summary, fmt.Errorf("sample %d has no prompt", index)
		}
		if sample.CWD != "" && sample.CWD != dir {
			return summary, fmt.Errorf("sample %d cwd is not authorized workspace", index)
		}
		if sample.Image != "" || sample.DockerImage != "" {
			return summary, fmt.Errorf("sample %d cannot override administrator-owned execution image", index)
		}
		fingerprint := hash(prompt)
		occurrence := counts[fingerprint]
		counts[fingerprint]++
		keys[index] = fmt.Sprintf("%s:%d", fingerprint, occurrence)
	}
	if cp.Admissions == nil {
		cp.Admissions = map[string]admission{}
	}
	for offset := 0; offset < len(samples); offset += batchSize {
		end := min(offset+batchSize, len(samples))
		for cursor := offset; cursor < end; {
			limit := min(cursor+workerCount, end)
			for index := cursor; index < limit; index++ {
				key := keys[index]
				sample := samples[index]
				record, exists := cp.Admissions[key]
				if exists && record.Trajectory != nil && record.Trajectory.Completed {
					continue
				}
				if exists && record.TaskID == 0 && record.State == "prepared" {
					t, e := r.tasks.TaskForMessage(ctx, i.Session, record.Event)
					if e != nil {
						return summary, fmt.Errorf("sample %d prepared admission unknown; operator review required: %w", index, e)
					}
					record.TaskID = t.ID
					record.State = "admitted"
					cp.Admissions[key] = record
				}
				if exists && record.TaskID > 0 {
					t, e := r.tasks.Get(ctx, i.Session, record.TaskID)
					if e != nil {
						return summary, e
					}
					if t.Status == "interrupted" || t.Status == "cancelled" || t.Status == "failed" {
						if !opts.RetryFailed {
							continue
						}
						record = admission{}
						exists = false
					}
				}
				if exists {
					continue
				}
				msg := chat.Message{Session: i.Session, SenderID: i.SenderID, Direct: i.Direct, Text: promptOf(sample)}
				req, e := r.prepare(ctx, msg, opts.Model)
				if e != nil {
					return summary, e
				}
				if !r.cfg.IncludeMemory {
					req.Tools = withoutMemoryTools(req.Tools)
				}
				req.Tools, e = filterTools(req.Tools, weights)
				if e != nil {
					return summary, e
				}
				turns := opts.MaxTurns
				if turns == 0 {
					turns = r.cfg.MaxTurns
				}
				if req.RunLimits == nil {
					return summary, errors.New("batch host run limits unavailable")
				}
				if turns < 1 || turns > r.cfg.MaxTurns || turns > req.RunLimits.MaxRounds {
					return summary, errors.New("max_turns exceeds configured batch/runtime limit")
				}
				limits := *req.RunLimits
				limits.MaxRounds = turns
				req.RunLimits = &limits
				if opts.ReasoningEffort != "" {
					switch opts.ReasoningEffort {
					case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
						req.ReasoningEffort = opts.ReasoningEffort
					default:
						return summary, errors.New("invalid batch reasoning effort")
					}
				}
				known := map[string]bool{}
				for _, name := range cp.ToolNames {
					known[name] = true
				}
				for _, tool := range req.Tools {
					if tool.Function != nil && !known[tool.Function.Name] {
						cp.ToolNames = append(cp.ToolNames, tool.Function.Name)
						known[tool.Function.Name] = true
					}
				}
				sort.Strings(cp.ToolNames)
				req.ToolScope = make([]string, 0, len(req.Tools))
				for _, tool := range req.Tools {
					if tool.Function != nil {
						req.ToolScope = append(req.ToolScope, tool.Function.Name)
					}
				}
				if len(req.ToolScope) == 0 {
					req.ToolScope = []string{"__batch_no_tools__"}
				}
				// 预填只接受普通对话；工具响应必须属于实际任务，不能从数据集伪造执行证据。
				for _, prefill := range sample.Messages {
					if prefill.Role == "tool" || len(prefill.ToolCalls) > 0 {
						return summary, errors.New("dataset prefill cannot forge tool calls/results")
					}
				}
				if len(sample.Messages) > 0 {
					req.Messages = append(req.Messages[:len(req.Messages)-1], sample.Messages...)
				} else if len(sample.Conversations) > 0 {
					req.Messages = req.Messages[:len(req.Messages)-1]
					for _, prefill := range sample.Conversations {
						role := ""
						switch prefill.From {
						case "human", "user":
							role = "user"
						case "gpt", "assistant":
							role = "assistant"
						case "system":
							role = "system"
						default:
							return summary, errors.New("invalid prefill role")
						}
						if len(prefill.ToolCalls) > 0 {
							return summary, errors.New("prefill tool calls forbidden")
						}
						req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: role, Content: prefill.Value})
					}
				}
				attempt := hash(fmt.Sprintf("%s:%d", key, time.Now().UnixNano()))[:16]
				record = admission{Event: "saber-batch:" + opts.RunName + ":" + key + ":" + attempt, State: "prepared"}
				cp.Admissions[key] = record
				cp.Updated = time.Now()
				if e = privateWrite(cpPath, cp); e != nil {
					return summary, e
				}
				msg.ID = record.Event
				t, e := r.tasks.SubmitSilent(ctx, msg, dir, req)
				if e != nil {
					return summary, e
				}
				record.TaskID = t.ID
				record.State = "admitted"
				cp.Admissions[key] = record
				if e = privateWrite(cpPath, cp); e != nil {
					return summary, e
				}
			}
			for index := cursor; index < limit; index++ {
				key := keys[index]
				record := cp.Admissions[key]
				if record.Trajectory != nil && record.Trajectory.Completed {
					continue
				}
				if record.TaskID == 0 {
					continue
				}
				t, e := r.waitTask(ctx, i, record.TaskID)
				if e != nil {
					return summary, e
				}
				trajectory := exportTrajectory(t, index, key)
				if r.cfg.RequireReasoning && trajectory.ReasoningTurns == 0 && trajectory.Completed {
					trajectory.Discarded = "no reasoning"
				}
				if opts.Evaluate {
					evaluation, e := r.evaluate(ctx, samples[index], trajectory)
					trajectory.Evaluation = &evaluation
					if e != nil {
						trajectory.Evaluation.Error = e.Error()
					}
				}
				record.Trajectory = &trajectory
				record.State = "exported"
				cp.Admissions[key] = record
				if e = appendRow(filepath.Join(runDir, fmt.Sprintf("batch_%d.jsonl", offset/batchSize)), trajectory); e != nil {
					return summary, e
				}
				if e = privateWrite(cpPath, cp); e != nil {
					return summary, e
				}
			}
			cursor = limit
		}
	}
	summary = Summary{RunName: opts.RunName, Total: len(samples), ToolStats: map[string]ToolStats{}}
	var trajectories []Trajectory
	for index, key := range keys {
		record := cp.Admissions[key]
		if record.Trajectory == nil {
			return summary, fmt.Errorf("sample %d has no terminal trajectory", index)
		}
		t := *record.Trajectory
		t.PromptIndex = index
		normalizeTrajectory(&t, cp.ToolNames)
		if opts.Evaluate && t.Completed && (t.Evaluation == nil || t.Evaluation.Error != "") {
			evaluation, e := r.evaluate(ctx, samples[index], t)
			if e != nil {
				evaluation.Error = e.Error()
			}
			t.Evaluation = &evaluation
			record.Trajectory = &t
			cp.Admissions[key] = record
			if e = privateWrite(cpPath, cp); e != nil {
				return summary, e
			}
		}
		if t.Completed {
			summary.Completed++
		} else {
			summary.Failed++
		}
		summary.AssistantTurns += t.AssistantTurns
		summary.ReasoningTurns += t.ReasoningTurns
		if t.Discarded != "" {
			summary.Discarded++
			continue
		}
		if t.Evaluation != nil {
			summary.Evaluated++
			if t.Evaluation.Error != "" {
				summary.EvaluationErrors++
			}
			if t.Evaluation.Passed {
				summary.Passed++
			}
		}
		for name, stat := range t.ToolStats {
			total := summary.ToolStats[name]
			total.Count += stat.Count
			total.Success += stat.Success
			total.Failure += stat.Failure
			summary.ToolStats[name] = total
		}
		trajectories = append(trajectories, t)
	}
	file := filepath.Join(runDir, "trajectories.jsonl")
	if err = writeTrajectories(file, trajectories, false); err != nil {
		return summary, err
	}
	summary.Files = []string{file, cpPath, filepath.Join(runDir, "statistics.json")}
	if opts.Compress {
		compressed := file + ".gz"
		if err = writeTrajectories(compressed, trajectories, true); err != nil {
			return summary, err
		}
		summary.Files = append(summary.Files, compressed)
	}
	if err = privateWrite(filepath.Join(runDir, "statistics.json"), summary); err != nil {
		return summary, err
	}
	if summary.Failed > 0 || summary.EvaluationErrors > 0 {
		return summary, fmt.Errorf("%d batch tasks and %d evaluations failed/interrupted; results preserved", summary.Failed, summary.EvaluationErrors)
	}
	return summary, nil
}

func (r *Runner) waitTask(ctx context.Context, i chat.Identity, id int64) (task.Task, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := r.tasks.Get(ctx, i.Session, id)
		if err != nil {
			return t, err
		}
		switch t.Status {
		case "queued", "running":
		case "blocked", "review", "waiting_input":
			return t, errors.New("batch task requires operator input/review; resume after resolving task")
		default:
			return t, nil
		}
		select {
		case <-ctx.Done():
			_, cancelErr := r.tasks.Cancel(context.WithoutCancel(ctx), i, id)
			return task.Task{}, errors.Join(ctx.Err(), cancelErr)
		case <-ticker.C:
		}
	}
}
func appendRow(path string, value any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func writeTrajectories(path string, values []Trajectory, compressed bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".trajectories-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err != nil {
		return errors.Join(err, f.Close())
	}
	var w io.Writer = f
	var gz *gzip.Writer
	if compressed {
		gz = gzip.NewWriter(f)
		w = gz
	}
	encoder := json.NewEncoder(w)
	for _, value := range values {
		if err = encoder.Encode(value); err != nil {
			break
		}
	}
	if gz != nil {
		err = errors.Join(err, gz.Close())
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (r *Runner) recoverOutputs(dir string, cp *checkpoint) error {
	files, err := filepath.Glob(filepath.Join(dir, "batch_*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, path := range files {
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), 64<<20)
		for scanner.Scan() {
			var trajectory Trajectory
			if json.Unmarshal(scanner.Bytes(), &trajectory) != nil {
				continue
			}
			if trajectory.Key == "" || !trajectory.Completed {
				continue
			}
			record := cp.Admissions[trajectory.Key]
			record.TaskID = trajectory.TaskID
			record.State = "exported"
			record.Trajectory = &trajectory
			cp.Admissions[trajectory.Key] = record
		}
		e = errors.Join(scanner.Err(), f.Close())
		if e != nil {
			return e
		}
	}
	return nil
}
