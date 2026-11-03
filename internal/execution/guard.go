package execution

import (
	"fmt"
	"regexp"
	"strings"

	"rua.plus/saber/internal/config"
)

// commandGuard 是危险命令绊线：内置规则与管理员追加的字面量子串按小写匹配，
// 删除根目录用单独的正则识别。它只拦常见误操作，不是安全边界：sh -c 可以通过
// 引号、变量展开、命令替换或编码绕过字符串匹配。
type commandGuard struct {
	deny   []string
	rmRoot *regexp.Regexp
}

const (
	// maxGuardRules 是管理员追加规则的数量上限。
	maxGuardRules = 64
	// maxGuardRuleLength 是单条规则的字节上限。
	maxGuardRuleLength = 256
	// rmRootLabel 是命中根目录删除正则时对外暴露的规则名，不回显被拒绝的命令。
	rmRootLabel = "rm -rf /"
)

// builtinDenyRules 是随二进制固定发布的字面量规则，全部为小写形式。
var builtinDenyRules = []string{
	"/var/run/docker.sock",
	"--privileged",
	"--cap-add",
	"--pid=host",
	"--net=host",
	"--userns=host",
	"--security-opt",
	"/etc/shadow",
	"/etc/sudoers",
	"/.ssh/",
	"/.aws/",
	".git-credentials",
	".netrc",
	":(){",
}

// rmRootRule 匹配带 -r/-f 的 rm 直接删除根目录；/tmp 等子目录不命中。
var rmRootRule = regexp.MustCompile(`(?i)\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/(\s|$|\*)`)

// newCommandGuard 编译一次规则；CommandGuard 为 off 时返回 nil，所有检查直接放行。
func newCommandGuard(cfg config.ExecutionConfig) *commandGuard {
	if cfg.CommandGuard == "off" {
		return nil
	}
	rules := make([]string, 0, len(builtinDenyRules)+len(cfg.CommandGuardDeny))
	rules = append(rules, builtinDenyRules...)
	for _, rule := range cfg.CommandGuardDeny {
		rules = append(rules, strings.ToLower(rule))
	}
	return &commandGuard{deny: rules, rmRoot: rmRootRule}
}

// check 返回命中的规则名；调用方只回传规则名，不回显命令正文或环境变量。
func (g *commandGuard) check(command string) (string, bool) {
	if g == nil || command == "" {
		return "", false
	}
	lower := strings.ToLower(command)
	for _, rule := range g.deny {
		if strings.Contains(lower, rule) {
			return rule, true
		}
	}
	if g.rmRoot != nil && g.rmRoot.MatchString(command) {
		return rmRootLabel, true
	}
	return "", false
}

// ensureCommandAllowed 在任一后端启动命令前检查绊线规则。
// 拒绝是普通工具错误，既不 panic，也不返回空结果冒充成功。
func (e *Executor) ensureCommandAllowed(args map[string]any) error {
	if e == nil || e.guard == nil {
		return nil
	}
	command, _ := args["command"].(string)
	if rule, blocked := e.guard.check(command); blocked {
		return fmt.Errorf("命令被安全策略拒绝（命中规则 %q），未执行", rule)
	}
	return nil
}
