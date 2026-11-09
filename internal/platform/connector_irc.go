package platform

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

func (p *connector) receiveIRC(ctx context.Context) error {
	address := p.cfg.ServerAddress
	if address == "" {
		address = p.cfg.IMAPAddress
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if p.cfg.TLS {
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		if p.cfg.Password != "" {
			return errors.New("IRC 明文连接不能发送密码")
		}
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.conn = nil; p.mu.Unlock() }()
	write := func(line string) error {
		if strings.ContainsAny(line, "\r\n") {
			return errors.New("IRC command 含换行")
		}
		p.sendMu.Lock()
		defer p.sendMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		_, err := io.WriteString(conn, line+"\r\n")
		return err
	}
	if p.cfg.Password != "" {
		if err = write("PASS " + p.cfg.Password); err != nil {
			return err
		}
	}
	if err = write("CAP REQ :account-tag server-time message-tags"); err != nil {
		return err
	}
	if err = write("NICK " + p.cfg.Username); err != nil {
		return err
	}
	if err = write("USER " + p.cfg.Username + " 0 * :Saber"); err != nil {
		return err
	}
	if err = write("CAP END"); err != nil {
		return err
	}
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 1024), 8192)
	for reader.Scan() {
		line := reader.Text()
		if strings.HasPrefix(line, "PING ") {
			if err = write("PONG " + strings.TrimPrefix(line, "PING ")); err != nil {
				return err
			}
			continue
		}
		tags, prefix, command, params, trailing := ircParse(line)
		if command == "001" {
			for _, channel := range p.cfg.Channels {
				if err = write("JOIN " + channel); err != nil {
					return err
				}
			}
			continue
		}
		if command != "PRIVMSG" || len(params) == 0 {
			continue
		}
		nick, _, _ := strings.Cut(prefix, "!")
		if strings.EqualFold(nick, p.cfg.Username) {
			continue
		}
		sender := tags["account"]
		if sender == "" || sender == "*" {
			if !p.cfg.AllowAllUsers {
				continue
			}
			sender = nick
		}
		target := params[0]
		direct := strings.EqualFold(target, p.cfg.Username)
		if direct {
			target = nick
		}
		id := tags["msgid"]
		if id == "" {
			stamp := tags["time"]
			if stamp == "" {
				return errors.New("IRC 服务端未提供 msgid/server-time，无法安全恢复去重")
			}
			sum := sha256.Sum256([]byte(stamp + "\x00" + prefix + "\x00" + target + "\x00" + trailing))
			id = hex.EncodeToString(sum[:])
		}
		msg := chat.Message{ID: id, SenderID: sender, Direct: direct, Text: trailing, Session: chat.Session{Conversation: target}}
		if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
			return err
		}
	}
	if err = reader.Err(); err != nil {
		return err
	}
	return io.EOF
}
func ircParse(line string) (map[string]string, string, string, []string, string) {
	tags := make(map[string]string)
	if strings.HasPrefix(line, "@") {
		raw, rest, _ := strings.Cut(line, " ")
		line = rest
		for _, item := range strings.Split(strings.TrimPrefix(raw, "@"), ";") {
			key, value, _ := strings.Cut(item, "=")
			tags[key] = value
		}
	}
	prefix := ""
	if strings.HasPrefix(line, ":") {
		prefix, line, _ = strings.Cut(line, " ")
		prefix = strings.TrimPrefix(prefix, ":")
	}
	head, trailing, _ := strings.Cut(line, " :")
	parts := strings.Fields(head)
	if len(parts) == 0 {
		return tags, prefix, "", nil, trailing
	}
	return tags, prefix, parts[0], parts[1:], trailing
}
func (p *connector) sendIRC(ctx context.Context, reply chat.Reply) (string, error) {
	if strings.ContainsAny(reply.Session.Conversation, " \r\n,:\x00") {
		return "", errors.New("IRC target 无效")
	}
	p.mu.Lock()
	closer := p.conn
	p.mu.Unlock()
	conn, ok := closer.(net.Conn)
	if !ok {
		return "", errors.New("IRC 尚未连接")
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	for _, line := range strings.Split(reply.Text, "\n") {
		for _, chunk := range splitRunes(line, 100) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if strings.ContainsAny(chunk, "\r\x00") {
				return "", errors.New("IRC 消息含控制字符")
			}
			_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := fmt.Fprintf(conn, "PRIVMSG %s :%s\r\n", reply.Session.Conversation, chunk); err != nil {
				return "", err
			}
		}
	}
	return reply.TransactionID, nil
}
