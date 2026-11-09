// Package toolkit 定义可选能力共用的工具端口；权限与模型身份仍由 Saber 宿主控制。
package toolkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
)

// Host 提供已经绑定任务、身份和授权的宿主操作；扩展不能自行构造另一位用户的身份。
type Host struct {
	StateDir     string
	Call         func(context.Context, string, map[string]any) (any, error)
	StartProcess func(context.Context, []string, map[string]string) (Process, error)
	SaveOutput   func(context.Context, []byte) (string, error)
	Check        func(context.Context, string, bool) error
	Workspace    func(context.Context) (string, error)
	Complete     func(context.Context, string, []openai.ChatCompletionMessage) (string, error)
	IsAdmin      func(context.Context) bool
}

// Process 是后端内的双向进程流；宿主负责任务权限、取消与进程回收。
type Process interface {
	io.Reader
	io.Writer
	Close() error
	CloseWrite() error
	Wait() error
}

// Definition 把模型工具定义和程序检查的权限绑定；Requires 中的权限必须全部满足。
type Definition struct {
	Tool       openai.Tool
	Permission string
	Requires   []string
	External   bool
}

// Result 携带文本数据与模型可见图片；图片由提供者生成或校验，不从不可信结果中猜测。
type Result struct {
	Data     any      `json:"data,omitempty"`
	Images   []string `json:"images,omitempty"`
	Files    []string `json:"files,omitempty"`
	IsError  bool     `json:"is_error,omitempty"`
	OutputID string   `json:"output_id,omitempty"`
}

// Provider 实现一组实际工具；关闭时必须释放其持有的进程、连接和临时资源。
type Provider interface {
	Definitions() []Definition
	Execute(context.Context, string, map[string]any) (any, error)
	Close() error
}

type entry struct {
	definition Definition
	provider   Provider
}

// Registry 统一可选工具的发现与调用；列举和执行复用同一权限检查。
type Registry struct {
	mu        sync.RWMutex
	host      Host
	entries   map[string]entry
	order     []string
	providers []Provider
	closed    bool
}

// New 创建尚未登记工具的注册表。
func New(host Host) *Registry {
	return &Registry{host: host, entries: make(map[string]entry)}
}

// Register 原子登记一组工具；任何重复或非法定义都使整组登记失败。
func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return errors.New("tool provider is nil")
	}
	definitions := provider.Definitions()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("tool registry is closed")
	}
	seen := make(map[string]bool, len(definitions))
	for _, d := range definitions {
		if d.Tool.Function == nil || d.Tool.Function.Name == "" || d.Tool.Type != openai.ToolTypeFunction {
			return errors.New("tool provider has an invalid function definition")
		}
		name := d.Tool.Function.Name
		if seen[name] {
			return fmt.Errorf("tool %q is repeated by its provider", name)
		}
		if _, exists := r.entries[name]; exists {
			return fmt.Errorf("tool %q is already registered", name)
		}
		seen[name] = true
	}
	for _, d := range definitions {
		name := d.Tool.Function.Name
		r.entries[name] = entry{definition: d, provider: provider}
		r.order = append(r.order, name)
	}
	r.providers = append(r.providers, provider)
	return nil
}

func (r *Registry) authorize(ctx context.Context, d Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("tool requires trusted identity")
	}
	if d.Permission == "" && !d.External && len(d.Requires) == 0 {
		return nil
	}
	if r.host.Check == nil {
		return errors.New("tool authorization is unavailable")
	}
	permission := d.Permission
	if permission == "" && d.External {
		permission = d.Tool.Function.Name
	}
	if permission != "" {
		if err := r.host.Check(ctx, permission, d.External); err != nil {
			return err
		}
	}
	for _, name := range d.Requires {
		if err := r.host.Check(ctx, name, false); err != nil {
			return err
		}
	}
	return nil
}

// Contains 判断工具是否由此注册表拥有，不表示调用者已经获得授权。
func (r *Registry) Contains(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.entries[name]
	return exists && !r.closed
}

// Tools 仅返回当前可信身份实际获准使用的工具。
func (r *Registry) Tools(ctx context.Context) []openai.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil
	}
	var tools []openai.Tool
	for _, name := range r.order {
		d := r.entries[name].definition
		if r.authorize(ctx, d) == nil {
			tools = append(tools, d.Tool)
		}
	}
	return tools
}

// Execute 在每次实际调用前重新授权；已关闭、未知或越权的工具不会被派发。
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	r.mu.RLock()
	e, exists := r.entries[name]
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return nil, errors.New("tool registry is closed")
	}
	if !exists {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if err := r.authorize(ctx, e.definition); err != nil {
		return nil, err
	}
	return e.provider.Execute(ctx, name, args)
}

// Close 停止后续派发并关闭所有提供者，保留每一个清理错误。
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	providers := r.providers
	r.mu.Unlock()
	var failures []error
	for i := len(providers) - 1; i >= 0; i-- {
		if err := providers[i].Close(); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Definitions 返回当前身份获准使用的完整定义，供延迟发现保留原始权限约束。
func (r *Registry) Definitions(ctx context.Context) []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil
	}
	var definitions []Definition
	for _, name := range r.order {
		d := r.entries[name].definition
		if r.authorize(ctx, d) == nil {
			definitions = append(definitions, d)
		}
	}
	return definitions
}

// Authorized 复核指定工具的当前授权，不执行工具，也不依赖旧的工具快照。
func (r *Registry) Authorized(ctx context.Context, name string) bool {
	r.mu.RLock()
	e, exists := r.entries[name]
	closed := r.closed
	r.mu.RUnlock()
	return exists && !closed && r.authorize(ctx, e.definition) == nil
}
