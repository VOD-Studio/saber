package conversation_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCoreDependencies 防止聊天核心通过间接依赖重新引入平台 SDK。
func TestCoreDependencies(t *testing.T) {
	command := exec.Command("go", "list", "-tags", "goolm", "-deps", "rua.plus/saber/internal/agent", "rua.plus/saber/internal/chat/...", "rua.plus/saber/internal/conversation", "rua.plus/saber/internal/model", "rua.plus/saber/internal/mcp", "rua.plus/saber/internal/task")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency inspection: %v\n%s", err, output)
	}
	for _, name := range strings.Fields(string(output)) {
		if strings.HasPrefix(name, "maunium.net/go/mautrix") || strings.HasPrefix(name, "rua.plus/saber/internal/matrix") || name == "rua.plus/saber/internal/ai" {
			t.Errorf("core imports platform adapter: %s", name)
		}
	}
}
