package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

const (
	// approvalCodeBytes 是审批码的随机字节数；64 位随机量足以阻止在线猜测。
	approvalCodeBytes = 8
	// maxApprovalCommandDisplay 限制批准回执里展示的命令长度。
	maxApprovalCommandDisplay = 512
)

// approvalRequest 是一次性命令审批的内存记录。
// 审批不落盘：Saber 重启后所有待审批与已批准请求立即失效。
type approvalRequest struct {
	code        string
	fingerprint string
	platform    string
	account     string
	room        string
	sender      string
	dir         string
	tool        string
	command     string
	rule        string
	expires     time.Time
	approved    bool
}

// approvalFingerprint 把工具、命令正文、工作区目录与可信身份绑定为不可逆摘要。
// 批准只对完全相同的组合生效，任何一项变化都需要重新审批。
func approvalFingerprint(identity chat.Identity, tool, command, dir string) string {
	hash := sha256.New()
	for _, part := range []string{tool, command, dir, identityKey(identity)} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// newApprovalCode 生成不可猜测的十六进制审批码。
func newApprovalCode() (string, error) {
	var raw [approvalCodeBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// pruneApprovalsLocked 删除已过期请求；调用方必须持有 approvalMu。
func (e *Executor) pruneApprovalsLocked(now time.Time) {
	for code, req := range e.approvals {
		if now.Before(req.expires) {
			continue
		}
		delete(e.approvals, code)
		if e.approvalFingerprints[req.fingerprint] == code {
			delete(e.approvalFingerprints, req.fingerprint)
		}
	}
}

// deleteApprovalLocked 同时移除审批码与指纹索引；调用方必须持有 approvalMu。
func (e *Executor) deleteApprovalLocked(req *approvalRequest) {
	delete(e.approvals, req.code)
	if e.approvalFingerprints[req.fingerprint] == req.code {
		delete(e.approvalFingerprints, req.fingerprint)
	}
}

// gateCommand 在打开执行环境或创建进程之前检查危险命令。
//
// block 直接拒绝命中规则的命令；ask 有已批准记录时先消费再放行，
// 否则登记（或复用）待审批请求并返回只含规则名与审批码的普通工具错误，
// 不回显命令正文或环境变量；off 由 nil guard 直接放行。
func (e *Executor) gateCommand(ctx context.Context, tool string, args map[string]any) error {
	if e == nil || e.guard == nil {
		return nil
	}
	command, _ := args["command"].(string)
	if command == "" {
		return nil
	}
	rule, hit := e.guard.check(command)
	if !hit {
		return nil
	}
	if e.cfg.CommandGuard != "ask" {
		return fmt.Errorf("命令被安全策略拒绝（命中规则 %q），未执行", rule)
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("命令审批需要可信的接入身份")
	}
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.dir == "" {
		return errors.New("命令审批需要已授权的工作区")
	}
	fingerprint := approvalFingerprint(identity, tool, command, run.dir)
	code, approved, err := e.requestApproval(identity, tool, command, run.dir, rule, fingerprint)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}
	return fmt.Errorf("命令命中安全策略规则 %q，需要本群授权成员执行 !approve %s（或 /approve %s）；审批码 %d 分钟内有效，批准后重新执行同一命令才会生效",
		rule, code, code, e.cfg.ApprovalTTLSeconds/60)
}

// requestApproval 消费已批准的请求或登记新的待审批请求。
// 同一身份、工具、命令与目录重复触发时复用原审批码，不占用新的名额。
func (e *Executor) requestApproval(identity chat.Identity, tool, command, dir, rule, fingerprint string) (string, bool, error) {
	e.approvalMu.Lock()
	defer e.approvalMu.Unlock()
	now := time.Now()
	e.pruneApprovalsLocked(now)
	if code, ok := e.approvalFingerprints[fingerprint]; ok {
		if req, exists := e.approvals[code]; exists {
			if req.approved {
				// 消费必须先于进程创建，避免并发重放同一批准。
				e.deleteApprovalLocked(req)
				return "", true, nil
			}
			return req.code, false, nil
		}
		delete(e.approvalFingerprints, fingerprint)
	}
	if len(e.approvals) >= e.cfg.ApprovalMaxPending {
		return "", false, fmt.Errorf("待审批命令已达上限 %d，请先批准、拒绝或等待过期", e.cfg.ApprovalMaxPending)
	}
	var code string
	for {
		generated, err := newApprovalCode()
		if err != nil {
			return "", false, err
		}
		if _, exists := e.approvals[generated]; !exists {
			code = generated
			break
		}
	}
	e.approvals[code] = &approvalRequest{
		code:        code,
		fingerprint: fingerprint,
		platform:    identity.Session.Platform,
		account:     identity.Session.Account,
		room:        identity.Session.Conversation,
		sender:      identity.SenderID,
		dir:         dir,
		tool:        tool,
		command:     command,
		rule:        rule,
		expires:     now.Add(time.Duration(e.cfg.ApprovalTTLSeconds) * time.Second),
	}
	e.approvalFingerprints[fingerprint] = code
	return code, false, nil
}

// ResolveApproval 批准或拒绝一次待审批命令，返回给审批者的回执。
//
// 只有同一机器人账号下该工作区的有效授权成员或同群任务管理员可以操作。
// 拒绝立即删除请求，批准后的请求保持待消费状态，直到对应命令执行一次。
func (e *Executor) ResolveApproval(identity chat.Identity, code string, approve bool) (string, error) {
	if e == nil {
		return "", errors.New("执行器未启用，无法审批命令")
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "", errors.New("审批码不能为空")
	}
	e.approvalMu.Lock()
	defer e.approvalMu.Unlock()
	e.pruneApprovalsLocked(time.Now())
	req, ok := e.approvals[code]
	if !ok {
		return "", errors.New("审批码不存在、已过期或已被使用")
	}
	if !e.canApproveLocked(identity, req) {
		return "", errors.New("只有该工作区的授权成员或任务管理员可以审批")
	}
	if !approve {
		rule := req.rule
		e.deleteApprovalLocked(req)
		return fmt.Sprintf("已拒绝审批码 %s（命中规则 %q），该命令不会执行。", code, rule), nil
	}
	req.approved = true
	return fmt.Sprintf("已批准审批码 %s：%s `%s`（命中规则 %q）。批准只对完全相同的命令、身份与目录生效一次，%d 分钟内未执行则失效。",
		code, req.tool, displayApprovalCommand(req.command), req.rule, e.cfg.ApprovalTTLSeconds/60), nil
}

// canApproveLocked 判断审批者是否是同一机器人账号下该工作区的授权成员，
// 或配置中的精确任务管理员；调用方必须持有 approvalMu。
func (e *Executor) canApproveLocked(identity chat.Identity, req *approvalRequest) bool {
	if e.admins[identityKey(identity)] {
		return true
	}
	if identity.Session.Platform != req.platform || identity.Session.Account != req.account {
		return false
	}
	grant, ok := e.grant(identity)
	if !ok {
		return false
	}
	workspace, ok := e.cfg.Workspaces[grant.Workspace]
	return ok && workspace.Path == req.dir
}

// displayApprovalCommand 截断超长命令，保留 UTF-8 边界。
func displayApprovalCommand(command string) string {
	if len(command) <= maxApprovalCommandDisplay {
		return command
	}
	cut := maxApprovalCommandDisplay
	for cut > 0 && !utf8.RuneStart(command[cut]) {
		cut--
	}
	return command[:cut] + "…"
}
