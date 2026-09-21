package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rua.plus/saber/internal/task"
)

// Client 只访问 Saber 的会话接口，不直接访问模型或任务数据库。
type Client struct {
	endpoint, token string
	http            *http.Client
}

// NewClient 创建可取消的本机客户端，避免将本机令牌发往其他主机。
func NewClient(endpoint, token string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return nil, errors.New("服务地址必须是本机 HTTP 地址，例如 http://127.0.0.1:8320")
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, input any, cursor int64) (*http.Response, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cursor > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 Saber，请先运行 saber serve: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		detail, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		closeErr := resp.Body.Close()
		return nil, errors.Join(fmt.Errorf("saber (%d): %s", resp.StatusCode, strings.TrimSpace(string(detail))), readErr, closeErr)
	}
	return resp, nil
}

// JSON 完成一次有限时的会话查询或控制操作。
func (c *Client) JSON(ctx context.Context, method, path string, input, output any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.request(ctx, method, path, input, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output)
}

// Stream 是单个任务的事件订阅；关闭只断开订阅，不取消任务。
type Stream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

// Update 是流中的增量或任务状态快照。
type Update struct {
	Event *task.Record
	Task  *Turn
}

// Subscribe 从指定持久化游标继续读取任务事件。
func (c *Client) Subscribe(ctx context.Context, session string, taskID, cursor int64) (*Stream, error) {
	resp, err := c.request(ctx, "GET", fmt.Sprintf("/v1/sessions/%s/tasks/%d/events", url.PathEscape(session), taskID), nil, cursor)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	return &Stream{body: resp.Body, scanner: scanner}, nil
}

// Next 返回下一个完整事件；调用方必须串行读取。
func (s *Stream) Next() (Update, error) {
	kind, data := "", ""
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			switch kind {
			case "event":
				event := new(task.Record)
				err := json.Unmarshal([]byte(data), event)
				return Update{Event: event}, err
			case "task":
				turn := new(Turn)
				err := json.Unmarshal([]byte(data), turn)
				return Update{Task: turn}, err
			}
			kind, data = "", ""
		} else if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data += strings.TrimPrefix(line, "data: ")
		}
	}
	if err := s.scanner.Err(); err != nil {
		return Update{}, err
	}
	return Update{}, io.EOF
}

// Close 释放连接；请求取消也会解除正在进行的 Next 读取。
func (s *Stream) Close() error { return s.body.Close() }
