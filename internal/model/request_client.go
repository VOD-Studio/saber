package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// requestClient 补齐 go-openai 在 Chat Completions 中因 omitempty 丢失的零温度。
// Saber 已在请求构造时解析默认温度，省略零值会错误地恢复为上游采样默认值。
type requestClient struct{ client *http.Client }

func (c requestClient) Do(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/chat/completions") && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		var payload map[string]json.RawMessage
		err = json.NewDecoder(body).Decode(&payload)
		if err = errors.Join(err, body.Close()); err != nil {
			return nil, err
		}
		if _, present := payload["temperature"]; !present {
			payload["temperature"] = json.RawMessage("0")
			data, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			if err = req.Body.Close(); err != nil {
				return nil, err
			}
			req = req.Clone(req.Context())
			req.Body = io.NopCloser(bytes.NewReader(data))
			req.ContentLength = int64(len(data))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
		}
	}
	return c.client.Do(req)
}
