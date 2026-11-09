package model

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

func awsCredentials(cfg config.AWSProviderConfig) (config.AWSProviderConfig, error) {
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
			return cfg, errors.New("AWS credential pair is incomplete")
		}
		return cfg, nil
	}
	if cfg.Profile != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cfg, err
		}
		path := filepath.Join(home, ".aws", "credentials")
		info, err := os.Lstat(path)
		if err != nil {
			return cfg, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return cfg, errors.New("AWS credential file must be private (0600)")
		}
		f, err := os.Open(path)
		if err != nil {
			return cfg, err
		}
		defer func() { _ = f.Close() }()
		section := ""
		scanner := bufio.NewScanner(io.LimitReader(f, 1<<20))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.TrimSpace(line[1 : len(line)-1])
				continue
			}
			if section != cfg.Profile {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "aws_access_key_id":
				cfg.AccessKeyID = value
			case "aws_secret_access_key":
				cfg.SecretAccessKey = value
			case "aws_session_token":
				cfg.SessionToken = value
			}
		}
		if err = scanner.Err(); err != nil {
			return cfg, err
		}
	} else {
		// 环境变量只在管理员未指定任何其他凭据源时使用，不访问实例元数据或外部进程。
		cfg.AccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
		cfg.SecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
		cfg.SessionToken = os.Getenv("AWS_SESSION_TOKEN")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return cfg, errors.New("bedrock requires aws.access_key_id+secret_access_key, explicit aws.profile, or AWS credential environment variables")
	}
	return cfg, nil
}

func shaHex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func hmacSHA(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func signAWS(req *http.Request, body []byte, cfg config.AWSProviderConfig, now time.Time) error {
	if cfg.Region == "" {
		return errors.New("bedrock requires aws.region")
	}
	date := now.UTC().Format("20060102")
	stamp := now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", shaHex(body))
	if cfg.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cfg.SessionToken)
	}
	headers := map[string]string{"host": req.URL.Host, "content-type": req.Header.Get("Content-Type"), "x-amz-date": stamp, "x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256")}
	if cfg.SessionToken != "" {
		headers["x-amz-security-token"] = cfg.SessionToken
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key + ":" + strings.Join(strings.Fields(headers[key]), " ") + "\n")
	}
	signed := strings.Join(keys, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	path = strings.ReplaceAll(url.PathEscape(path), "%2F", "/")
	query := strings.ReplaceAll(req.URL.Query().Encode(), "+", "%20")
	request := req.Method + "\n" + path + "\n" + query + "\n" + canonical.String() + "\n" + signed + "\n" + shaHex(body)
	scope := date + "/" + cfg.Region + "/bedrock/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + shaHex([]byte(request))
	key := hmacSHA([]byte("AWS4"+cfg.SecretAccessKey), date)
	key = hmacSHA(key, cfg.Region)
	key = hmacSHA(key, "bedrock")
	key = hmacSHA(key, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cfg.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA(key, toSign)))
	return nil
}

func (c *Client) bedrockRequest(req agent.Request) (map[string]any, error) {
	var system []any
	var messages []map[string]any
	appendMessage := func(role string, content []any) {
		if len(content) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			last := messages[len(messages)-1]
			last["content"] = append(last["content"].([]any), content...)
		} else {
			messages = append(messages, map[string]any{"role": role, "content": content})
		}
	}
	for _, msg := range req.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			if msg.Content != "" {
				system = append(system, map[string]any{"text": msg.Content})
			}
			continue
		}
		if msg.Role == "tool" {
			if msg.ToolCallID == "" {
				return nil, errors.New("bedrock tool result requires call ID")
			}
			appendMessage("user", []any{map[string]any{"toolResult": map[string]any{"toolUseId": msg.ToolCallID, "content": []any{map[string]any{"text": msg.Content}}}}})
			continue
		}
		if msg.Role != "user" && msg.Role != "assistant" {
			return nil, errors.New("unsupported Bedrock message role")
		}
		if raw, ok := c.nativeStored(req, msg, "bedrock"); ok {
			var content []any
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			appendMessage(msg.Role, content)
			continue
		}
		if msg.Content != "" && len(msg.MultiContent) > 0 {
			return nil, openai.ErrContentFieldsMisused
		}
		var content []any
		if msg.Content != "" {
			content = append(content, map[string]any{"text": msg.Content})
		}
		for _, part := range msg.MultiContent {
			switch part.Type {
			case openai.ChatMessagePartTypeText:
				content = append(content, map[string]any{"text": part.Text})
			case openai.ChatMessagePartTypeImageURL:
				if part.ImageURL == nil || !strings.HasPrefix(part.ImageURL.URL, "data:image/") {
					return nil, errors.New("bedrock requires inline base64 image data, not a remote URL")
				}
				header, data, ok := strings.Cut(part.ImageURL.URL, ",")
				if !ok || !strings.HasSuffix(header, ";base64") {
					return nil, errors.New("invalid Bedrock inline image")
				}
				if _, err := base64.StdEncoding.DecodeString(data); err != nil {
					return nil, errors.New("invalid base64 image")
				}
				format := strings.TrimSuffix(strings.TrimPrefix(header, "data:image/"), ";base64")
				if format == "jpg" {
					format = "jpeg"
				}
				content = append(content, map[string]any{"image": map[string]any{"format": format, "source": map[string]any{"bytes": data}}})
			default:
				return nil, errors.New("unsupported Bedrock message part")
			}
		}
		for _, call := range msg.ToolCalls {
			args, err := decodeArguments(call.Function.Arguments)
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]any{"toolUse": map[string]any{"toolUseId": call.ID, "name": call.Function.Name, "input": args}})
		}
		appendMessage(msg.Role, content)
	}
	inference := map[string]any{}
	if req.MaxTokens > 0 {
		inference["maxTokens"] = req.MaxTokens
	}
	if !strings.Contains(strings.ToLower(c.getModelName(req.Model)), "xai.grok") {
		inference["temperature"] = req.Temperature
	}
	body := map[string]any{"messages": messages, "inferenceConfig": inference}
	if len(system) > 0 {
		body["system"] = system
	}
	if len(req.Tools) > 0 {
		var tools []any
		for _, tool := range req.Tools {
			if tool.Function == nil || tool.Type != openai.ToolTypeFunction {
				return nil, errors.New("bedrock requires function tools")
			}
			tools = append(tools, map[string]any{"toolSpec": map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "inputSchema": map[string]any{"json": tool.Function.Parameters}}})
		}
		toolConfig := map[string]any{"tools": tools}
		if req.ToolChoice != nil {
			mode, name, err := modelToolChoice(req.ToolChoice)
			if err != nil {
				return nil, err
			}
			switch mode {
			case "auto":
				toolConfig["toolChoice"] = map[string]any{"auto": map[string]any{}}
			case "required":
				toolConfig["toolChoice"] = map[string]any{"any": map[string]any{}}
			case "none":
				toolConfig = nil
			case "named":
				toolConfig["toolChoice"] = map[string]any{"tool": map[string]any{"name": name}}
			}
		}
		if toolConfig != nil {
			body["toolConfig"] = toolConfig
		}
	}
	if fields, ok := c.provider.Extra["additional_model_request_fields"].(map[string]any); ok {
		body["additionalModelRequestFields"] = fields
	}
	if c.provider.AWS.GuardrailID != "" {
		if c.provider.AWS.GuardrailVersion == "" {
			return nil, errors.New("bedrock guardrail requires a version")
		}
		body["guardrailConfig"] = map[string]any{"guardrailIdentifier": c.provider.AWS.GuardrailID, "guardrailVersion": c.provider.AWS.GuardrailVersion}
	}
	return body, nil
}

func bedrockResult(model, stop string, content []map[string]any, usage openai.Usage) (*agent.Response, error) {
	result := &agent.Response{Model: model, Usage: usage, FinishReason: anthropicFinish(stop)}
	if stop == "end_turn" {
		result.FinishReason = "stop"
	}
	if stop == "guardrail_intervened" || stop == "content_filtered" {
		result.FinishReason = "content_filter"
	}
	var text strings.Builder
	for _, block := range content {
		if value, ok := block["text"].(string); ok {
			text.WriteString(value)
		}
		if tool, ok := block["toolUse"].(map[string]any); ok {
			args, err := json.Marshal(tool["input"])
			if err != nil {
				return result, err
			}
			id, _ := tool["toolUseId"].(string)
			name, _ := tool["name"].(string)
			result.ToolCalls = append(result.ToolCalls, openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: string(args)}})
		}
	}
	result.Content = text.String()
	if len(result.ToolCalls) > 0 {
		raw, err := json.Marshal(content)
		if err != nil {
			return result, err
		}
		envelope, err := json.Marshal(nativeHistory{Protocol: "bedrock", Content: raw})
		if err != nil {
			return result, err
		}
		result.ResponsesOutput = []json.RawMessage{envelope}
	}
	return result, agent.ValidateResponse(*result)
}

func (c *Client) createBedrock(ctx context.Context, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	defer func() {
		if err != nil && handler != nil {
			handler.OnError(ctx, err)
		}
	}()
	body, err := c.bedrockRequest(req)
	if err != nil {
		return nil, err
	}
	cfg, err := awsCredentials(c.provider.AWS)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	method := "/converse"
	if req.Stream {
		method = "/converse-stream"
	}
	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/model/" + url.PathEscape(c.getModelName(req.Model)) + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if err = signAWS(request, payload, cfg, time.Now()); err != nil {
		return nil, err
	}
	resp, err := requestClient{client: c.httpClient}.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, err := readBoundedBody(resp.Body)
		if err != nil {
			return nil, err
		}
		return nil, protocolError(resp, data)
	}
	var content []map[string]any
	var usage openai.Usage
	stop := ""
	if !req.Stream {
		var raw struct {
			Output struct {
				Message struct {
					Content []map[string]any `json:"content"`
				} `json:"message"`
			} `json:"output"`
			Stop  string `json:"stopReason"`
			Usage struct {
				Input  int `json:"inputTokens"`
				Output int `json:"outputTokens"`
				Total  int `json:"totalTokens"`
			} `json:"usage"`
		}
		data, err := readBoundedBody(resp.Body)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &raw); err != nil {
			return nil, errors.New("invalid Bedrock response")
		}
		content = raw.Output.Message.Content
		stop = raw.Stop
		usage = openai.Usage{PromptTokens: raw.Usage.Input, CompletionTokens: raw.Usage.Output, TotalTokens: raw.Usage.Total}
	} else {
		blocks := map[int]map[string]any{}
		arguments := map[int]*strings.Builder{}
		maxIndex := -1
		err = readAWSEvents(resp.Body, func(event string, data []byte) error {
			var raw struct {
				Index int            `json:"contentBlockIndex"`
				Start map[string]any `json:"start"`
				Delta struct {
					Text    string `json:"text"`
					ToolUse struct {
						Input string `json:"input"`
					} `json:"toolUse"`
					ReasoningContent struct {
						Text      string `json:"text"`
						Signature string `json:"signature"`
					} `json:"reasoningContent"`
				} `json:"delta"`
				Stop  string `json:"stopReason"`
				Usage struct {
					Input  int `json:"inputTokens"`
					Output int `json:"outputTokens"`
					Total  int `json:"totalTokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				return errors.New("invalid Bedrock stream JSON")
			}
			switch event {
			case "messageStart":
			case "contentBlockStart":
				if _, exists := blocks[raw.Index]; exists {
					return errors.New("duplicate Bedrock stream block")
				}
				blocks[raw.Index] = raw.Start
				if blocks[raw.Index] == nil {
					blocks[raw.Index] = map[string]any{}
				}
				arguments[raw.Index] = &strings.Builder{}
				maxIndex = max(maxIndex, raw.Index)
			case "contentBlockDelta":
				if blocks[raw.Index] == nil {
					blocks[raw.Index] = map[string]any{}
					arguments[raw.Index] = &strings.Builder{}
					maxIndex = max(maxIndex, raw.Index)
				}
				block := blocks[raw.Index]
				if raw.Delta.Text != "" {
					previous, _ := block["text"].(string)
					block["text"] = previous + raw.Delta.Text
					if handler != nil {
						handler.OnChunk(ctx, raw.Delta.Text)
					}
				}
				if raw.Delta.ToolUse.Input != "" {
					arguments[raw.Index].WriteString(raw.Delta.ToolUse.Input)
				}
				if raw.Delta.ReasoningContent.Text != "" || raw.Delta.ReasoningContent.Signature != "" {
					reasoning, _ := block["reasoningContent"].(map[string]any)
					if reasoning == nil {
						reasoning = map[string]any{"reasoningText": map[string]any{"text": "", "signature": ""}}
					}
					text := reasoning["reasoningText"].(map[string]any)
					previous, _ := text["text"].(string)
					signature, _ := text["signature"].(string)
					text["text"] = previous + raw.Delta.ReasoningContent.Text
					text["signature"] = signature + raw.Delta.ReasoningContent.Signature
					block["reasoningContent"] = reasoning
				}
			case "contentBlockStop":
				if tool, ok := blocks[raw.Index]["toolUse"].(map[string]any); ok {
					var input any
					if arguments[raw.Index] == nil || json.Unmarshal([]byte(arguments[raw.Index].String()), &input) != nil {
						return errors.New("invalid Bedrock stream tool arguments")
					}
					tool["input"] = input
				}
			case "messageStop":
				stop = raw.Stop
			case "metadata":
				usage = openai.Usage{PromptTokens: raw.Usage.Input, CompletionTokens: raw.Usage.Output, TotalTokens: raw.Usage.Total}
			default:
				return &ProtocolError{Status: 502, Code: event}
			}
			return ctx.Err()
		})
		for i := range maxIndex + 1 {
			if blocks[i] == nil {
				return nil, errors.New("missing Bedrock stream block")
			}
			content = append(content, blocks[i])
		}
	}
	result, validationErr := bedrockResult(c.getModelName(req.Model), stop, content, usage)
	if err != nil {
		if result != nil {
			result.FinishReason = ""
		}
		return result, err
	}
	if stop == "" {
		if result != nil {
			result.FinishReason = ""
		}
		return result, errors.New("bedrock response ended without messageStop")
	}
	if validationErr != nil {
		return result, validationErr
	}
	if req.Stream {
		notifyNative(ctx, result, handler)
	}
	return result, nil
}

func readAWSEvents(reader io.Reader, consume func(string, []byte) error) error {
	total := 0
	for {
		var prelude [12]byte
		_, err := io.ReadFull(reader, prelude[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		length := int(binary.BigEndian.Uint32(prelude[0:4]))
		headersLength := int(binary.BigEndian.Uint32(prelude[4:8]))
		total += length
		if length < 16 || length > maxProtocolBody || total > maxProtocolBody || headersLength > length-16 {
			return errors.New("invalid AWS event-stream length")
		}
		if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
			return errors.New("invalid AWS event-stream prelude CRC")
		}
		frame := make([]byte, length)
		copy(frame, prelude[:])
		if _, err = io.ReadFull(reader, frame[12:]); err != nil {
			return err
		}
		if crc32.ChecksumIEEE(frame[:length-4]) != binary.BigEndian.Uint32(frame[length-4:]) {
			return errors.New("invalid AWS event-stream CRC")
		}
		headers := frame[12 : 12+headersLength]
		event, kind := "", ""
		for len(headers) > 0 {
			n := int(headers[0])
			if len(headers) < n+2 {
				return errors.New("invalid AWS event header")
			}
			name := string(headers[1 : 1+n])
			typ := headers[1+n]
			headers = headers[2+n:]
			if typ != 7 || len(headers) < 2 {
				return errors.New("unsupported AWS event header type")
			}
			size := int(binary.BigEndian.Uint16(headers[:2]))
			headers = headers[2:]
			if len(headers) < size {
				return errors.New("truncated AWS event header")
			}
			value := string(headers[:size])
			headers = headers[size:]
			switch name {
			case ":event-type", ":exception-type":
				event = value
			case ":message-type":
				kind = value
			}
		}
		if kind == "exception" || kind == "error" {
			return &ProtocolError{Status: 502, Code: event}
		}
		if err = consume(event, frame[12+headersLength:length-4]); err != nil {
			return err
		}
	}
}
