package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

type pluginHookKey struct{}

func intersectTools(current, saved []openai.Tool) []openai.Tool {
	allowed := make(map[string]bool, len(saved))
	for _, t := range saved {
		if t.Function != nil {
			allowed[t.Function.Name] = true
		}
	}
	out := current[:0]
	for _, t := range current {
		if t.Function != nil && allowed[t.Function.Name] {
			out = append(out, t)
		}
	}
	return out
}

func scopedTools(ctx context.Context, tools []openai.Tool) []openai.Tool {
	out := tools[:0]
	for _, t := range tools {
		if t.Function != nil && agent.ToolAllowed(ctx, t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) authorizeControlWorkspace(identity chat.Identity, dir string) error {
	if !s.IsEnabled() {
		return errors.New("AI is disabled")
	}
	if s.executor != nil {
		if granted, err := s.executor.Workspace(identity); err == nil {
			if granted != dir {
				return errors.New("controlled task workspace is not currently authorized")
			}
			return nil
		}
	}
	if dir != s.taskDir {
		return errors.New("controlled chat task must use the service chat directory")
	}
	return nil
}

func (s *Service) authorizeControlledTask(ctx context.Context, t task.Task) error {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("controlled task requires trusted identity")
	}
	if s.executor != nil {
		if _, err := s.executor.Workspace(identity); err == nil {
			return s.executor.ValidateTaskWorkspace(execution.WithTask(ctx, t.ID, t.WorkDir), t.ID, t.WorkDir)
		}
	}
	if t.Request.SubagentDepth > 0 || t.WorkDir != s.taskDir {
		return errors.New("controlled task has no authorized execution workspace")
	}
	return nil
}

func (s *Service) prepareTaskMedia(ctx context.Context, req agent.Request) (agent.Request, error) {
	if s.featureMedia == nil || s.tasks == nil {
		return req, nil
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, errors.New("media preparation requires trusted identity")
	}
	t, err := s.tasks.Get(ctx, identity.Session, task.ID(ctx))
	if err != nil {
		return req, err
	}
	needed := false
	for _, a := range t.Message.Attachments {
		if a.Kind != "image" {
			needed = true
			break
		}
	}
	if !needed {
		return req, nil
	}
	message, err := s.featureMedia.PrepareMessage(ctx, t.Message)
	if err != nil {
		return req, err
	}
	input := taskInput(message)
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == openai.ChatMessageRoleUser {
			req.Messages[i] = input
			return req, nil
		}
	}
	req.Messages = append(req.Messages, input)
	return req, nil
}

func (s *Service) prepareTaskReferences(ctx context.Context, req agent.Request) (agent.Request, error) {
	if s.executor == nil || s.tasks == nil {
		return req, nil
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, errors.New("context reference requires trusted identity")
	}
	if s.executor.Check(ctx, "read_file") != nil {
		return req, nil
	}
	t, err := s.tasks.Get(ctx, identity.Session, task.ID(ctx))
	if err != nil {
		return req, err
	}
	result, err := s.executor.ExpandReferences(ctx, t.Message.Text, s.config.Agent.Context.MaxTokens, s.readReferenceURL)
	if err != nil {
		return req, err
	}
	if result.Blocked {
		return req, fmt.Errorf("context references exceed input budget: %s", strings.Join(result.Warnings, "; "))
	}
	if len(result.References) == 0 {
		return req, nil
	}
	// 这里只附加新格式引用的展开部分，不改写逐字保存的用户指令和已有媒体输入。
	addition := strings.TrimPrefix(result.Message, t.Message.Text)
	if addition != "" {
		req = appendRuntimeContext(req, addition)
	}
	return req, nil
}

func (s *Service) readReferenceURL(ctx context.Context, url string, maxBytes int) (string, error) {
	if s.mcpManager == nil || s.executor == nil {
		return "", errors.New("authorized web fetch is unavailable")
	}
	server := s.mcpManager.GetServerForTool("fetch_url")
	if server == "" {
		return "", errors.New("web fetch is not configured")
	}
	if err := s.executor.CheckMCP(ctx, server, "fetch_url"); err != nil {
		return "", err
	}
	value, err := s.toolExecutor.ExecuteToolCall(ctx, "fetch_url", map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	result, ok := value.(*mcpsdk.CallToolResult)
	if !ok || result == nil {
		return "", errors.New("web fetch returned an invalid result")
	}
	if result.IsError {
		return "", errors.New("web fetch refused the context URL")
	}
	var content strings.Builder
	for _, part := range result.Content {
		if text, ok := part.(*mcpsdk.TextContent); ok {
			content.WriteString(text.Text)
			content.WriteByte('\n')
			if content.Len() > maxBytes {
				return "", errors.New("URL reference exceeds context limit")
			}
		}
	}
	return content.String(), nil
}

// StartVoiceTask 为实时音频创建无模型生命周期，执行身份和目录仍由当前授权决定。
func (s *Service) StartVoiceTask(ctx context.Context, message chat.Message) (task.Task, error) {
	if s.tasks == nil || s.executor == nil || s.featureMedia == nil {
		return task.Task{}, errors.New("voice task service is unavailable")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || identity.Session != message.Session || identity.SenderID != message.SenderID {
		return task.Task{}, errors.New("voice task identity mismatch")
	}
	dir, err := s.executor.Workspace(identity)
	if err != nil {
		return task.Task{}, err
	}
	return s.tasks.StartExternal(ctx, message, dir, "voice")
}
