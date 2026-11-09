package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxProtocolBody = 32 << 20

// ProtocolError 保留状态及重试窗口，避免依靠错误字符串中的数字误判。
type ProtocolError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("model provider HTTP %d (%s)", e.Status, e.Code)
}

func parseRetryAfter(raw string) time.Duration {
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return max(time.Until(when), 0)
	}
	return 0
}

func protocolError(resp *http.Response, body []byte) error {
	var raw struct {
		Error json.RawMessage `json:"error"`
		Type  string          `json:"type"`
		Code  string          `json:"code"`
	}
	_ = json.Unmarshal(body, &raw)
	var nested struct {
		Type   string `json:"type"`
		Code   string `json:"code"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw.Error, &nested)
	code := nested.Code
	if code == "" {
		code = nested.Type
	}
	if code == "" {
		code = nested.Status
	}
	if code == "" {
		code = raw.Code
	}
	if code == "" {
		_ = json.Unmarshal(raw.Error, &code)
	}
	if code == "" {
		code = http.StatusText(resp.StatusCode)
	}
	// 不回显 provider 正文，错误响应可能包含密钥或用户提示。
	return &ProtocolError{Status: resp.StatusCode, Code: code, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
}

func readBoundedBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxProtocolBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxProtocolBody {
		return nil, errors.New("model response exceeds 32 MiB")
	}
	return data, nil
}

func formJSON(ctx context.Context, endpoint string, values url.Values, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: sharedTransport, Timeout: 30 * time.Second, CheckRedirect: rejectProviderRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBoundedBody(resp.Body)
	if err != nil {
		return err
	}
	var auth struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &auth)
	if auth.Error != "" {
		return &oauthError{Code: auth.Error}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocolError(resp, data)
	}
	if err = json.Unmarshal(data, dst); err != nil {
		return errors.New("invalid provider JSON response")
	}
	return nil
}

func rejectProviderRedirect(_ *http.Request, _ []*http.Request) error {
	return errors.New("provider redirect refused; configure the final trusted endpoint")
}

func (c *Client) postProtocol(ctx context.Context, endpoint string, payload any, headers http.Header) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return requestClient{client: c.httpClient}.Do(req)
}
