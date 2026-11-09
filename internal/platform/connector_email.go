package platform

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"rua.plus/saber/internal/chat"
)

type imapClient struct {
	conn   net.Conn
	reader *bufio.Reader
	seq    int
	limit  int64
}

func (c *imapClient) command(ctx context.Context, command string) ([]string, [][]byte, error) {
	if strings.ContainsAny(command, "\r\n") {
		return nil, nil, errors.New("IMAP command 含换行")
	}
	c.seq++
	tag := fmt.Sprintf("S%06d", c.seq)
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if _, err := io.WriteString(c.conn, tag+" "+command+"\r\n"); err != nil {
		return nil, nil, err
	}
	var lines []string
	var literals [][]byte
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, tag+" ") {
			if !strings.HasPrefix(line, tag+" OK") {
				return nil, nil, errors.New("IMAP 拒绝操作")
			}
			return lines, literals, nil
		}
		lines = append(lines, line)
		if strings.HasSuffix(line, "}") {
			open := strings.LastIndexByte(line, '{')
			if open >= 0 {
				n, err := strconv.ParseInt(strings.TrimSuffix(line[open+1:], "}"), 10, 64)
				if err != nil || n < 0 || n > c.limit {
					return nil, nil, errors.New("IMAP literal 超限")
				}
				data := make([]byte, n)
				if _, err = io.ReadFull(c.reader, data); err != nil {
					return nil, nil, err
				}
				literals = append(literals, data)
			}
		}
	}
}
func imapQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
func (p *connector) receiveEmail(ctx context.Context) error {
	address := p.cfg.IMAPAddress
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(&dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.conn = nil; p.mu.Unlock() }()
	reader := bufio.NewReader(conn)
	if _, err = reader.ReadString('\n'); err != nil {
		return err
	}
	client := imapClient{conn: conn, reader: reader, limit: p.cfg.MaxMediaBytes + 4<<20}
	if _, _, err = client.command(ctx, "LOGIN "+imapQuote(p.cfg.Username)+" "+imapQuote(p.cfg.Password)); err != nil {
		return err
	}
	if _, _, err = client.command(ctx, "SELECT "+imapQuote(p.cfg.Mailbox)); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Duration(p.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		lines, _, err := client.command(ctx, "UID SEARCH UNSEEN")
		if err != nil {
			return err
		}
		for _, line := range lines {
			if !strings.HasPrefix(line, "* SEARCH") {
				continue
			}
			for _, uid := range strings.Fields(strings.TrimPrefix(line, "* SEARCH")) {
				if _, err = strconv.ParseUint(uid, 10, 64); err != nil {
					return err
				}
				_, literals, err := client.command(ctx, "UID FETCH "+uid+" (BODY.PEEK[])")
				if err != nil {
					return err
				}
				for _, data := range literals {
					msg, err := p.decodeEmail(data)
					if err != nil {
						if isDenied(err) {
							continue
						}
						return err
					}
					if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
						return err
					}
				}
				if _, _, err = client.command(ctx, "UID STORE "+uid+" +FLAGS.SILENT (\\Seen)"); err != nil {
					return err
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}
func (p *connector) decodeEmail(data []byte) (chat.Message, error) {
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return chat.Message{}, err
	}
	address, err := mail.ParseAddress(message.Header.Get("From"))
	if err != nil {
		return chat.Message{}, err
	}
	sender := strings.ToLower(address.Address)
	if !p.cfg.TrustFromHeader {
		auth := message.Header.Get("Authentication-Results")
		server, rest, ok := strings.Cut(auth, ";")
		if !ok || strings.TrimSpace(server) != p.cfg.AuthServID || p.cfg.AuthServID == "" || !strings.Contains(rest, "dmarc=pass") {
			return chat.Message{}, errors.New("邮件发送者未通过可信 MTA DMARC 授权")
		}
		_, domain, _ := strings.Cut(sender, "@")
		if !strings.Contains(rest, "header.from="+domain) {
			return chat.Message{}, errors.New("邮件 DMARC From 作用域不匹配，拒绝授权")
		}
	}
	id := strings.TrimSpace(message.Header.Get("Message-ID"))
	if id == "" {
		return chat.Message{}, errors.New("邮件缺少 Message-ID")
	}
	msg := chat.Message{ID: id, SenderID: sender, Direct: true, Session: chat.Session{Conversation: sender, Thread: firstEmailReference(message.Header.Get("References"), id)}, ReplyTo: id}
	if err = p.emailParts(message.Header, message.Body, &msg, 0); err != nil {
		return chat.Message{}, err
	}
	return msg, nil
}
func firstEmailReference(references, fallback string) string {
	parts := strings.Fields(references)
	if len(parts) > 0 {
		return parts[0]
	}
	return fallback
}
func (p *connector) emailParts(header mail.Header, reader io.Reader, msg *chat.Message, depth int) error {
	if depth > 8 {
		return errors.New("邮件 MIME 嵌套超限")
	}
	kind, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || kind == "" {
		kind = "text/plain"
	}
	if strings.HasPrefix(kind, "multipart/") {
		parts := multipart.NewReader(reader, params["boundary"])
		for {
			part, err := parts.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err = p.emailParts(mail.Header(part.Header), part, msg, depth+1); err != nil {
				_ = part.Close()
				return err
			}
			_ = part.Close()
		}
	}
	switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, reader)
	case "quoted-printable":
		reader = quotedprintable.NewReader(reader)
	}
	data, err := io.ReadAll(io.LimitReader(reader, p.cfg.MaxMediaBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > p.cfg.MaxMediaBytes {
		return errors.New("邮件附件超限")
	}
	_, disposition, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	name := disposition["filename"]
	if name == "" {
		name = params["name"]
	}
	if kind == "text/plain" && name == "" {
		msg.Text += string(data) + "\n"
		return nil
	}
	if kind == "text/html" && name == "" {
		if msg.Text == "" {
			msg.Text = string(data)
		}
		return nil
	}
	msg.Attachments = append(msg.Attachments, mediaAttachment(data, kind, name, ""))
	return nil
}
func (p *connector) sendEmail(ctx context.Context, reply chat.Reply) (string, error) {
	return p.sendEmailMedia(ctx, reply, uploadedMedia{})
}
func (p *connector) sendEmailMedia(ctx context.Context, reply chat.Reply, attachment uploadedMedia) (string, error) {
	recipient, err := mail.ParseAddress(reply.Session.Conversation)
	if err != nil {
		return "", err
	}
	from, err := mail.ParseAddress(p.cfg.Username)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(reply.ReplyTo, "\r\n") {
		return "", errors.New("邮件引用头含换行")
	}
	id := "<" + uuid.NewString() + "@saber.local>"
	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n", from.String(), recipient.String(), mime.QEncoding.Encode("utf-8", "Saber 回复"), id, time.Now().Format(time.RFC1123Z))
	if reply.ReplyTo != "" {
		fmt.Fprintf(&body, "In-Reply-To: %s\r\nReferences: %s\r\n", reply.ReplyTo, reply.ReplyTo)
	}
	if len(attachment.Data) == 0 {
		body.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		writer := quotedprintable.NewWriter(&body)
		_, _ = writer.Write([]byte(reply.Text))
		_ = writer.Close()
	} else {
		writer := multipart.NewWriter(&body)
		fmt.Fprintf(&body, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", writer.Boundary())
		text, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return "", err
		}
		encoded := quotedprintable.NewWriter(text)
		_, _ = encoded.Write([]byte(reply.Text))
		_ = encoded.Close()
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(attachment.MIME, map[string]string{"name": attachment.Name})}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name})}, "Content-Transfer-Encoding": {"base64"}})
		if err != nil {
			return "", err
		}
		value := base64.StdEncoding.EncodeToString(attachment.Data)
		for len(value) > 0 {
			n := min(76, len(value))
			_, _ = io.WriteString(part, value[:n]+"\r\n")
			value = value[n:]
		}
		if err = writer.Close(); err != nil {
			return "", err
		}
	}
	host, port, err := net.SplitHostPort(p.cfg.SMTPAddress)
	if err != nil {
		return "", err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if port == "465" {
		conn, err = tls.DialWithDialer(&dialer, "tcp", p.cfg.SMTPAddress, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", p.cfg.SMTPAddress)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	if port != "465" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return "", errors.New("SMTP 服务器不支持 STARTTLS，拒绝发送凭据")
		}
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return "", err
		}
	}
	if err = client.Auth(smtp.PlainAuth("", p.cfg.Username, p.cfg.Password, host)); err != nil {
		return "", err
	}
	if err = client.Mail(from.Address); err != nil {
		return "", err
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return "", err
	}
	writer, err := client.Data()
	if err != nil {
		return "", err
	}
	if _, err = writer.Write(body.Bytes()); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	if err = client.Quit(); err != nil {
		return "", err
	}
	return id, nil
}
