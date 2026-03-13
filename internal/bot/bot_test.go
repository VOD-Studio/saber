// Package bot_test 包含机器人初始化的单元测试。
package bot

import (
	"testing"
)

func TestSetupLoggingNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("setupLogging panic: %v", r)
		}
	}()

	setupLogging(false)
	setupLogging(true)
}
