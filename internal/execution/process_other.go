//go:build !unix

package execution

import "os/exec"

// 非 Unix 平台不提供 local 执行；这些桩仅保证二进制可交叉编译。
func setProcessGroup(*exec.Cmd) {}

func processGroup(*exec.Cmd) int { return 0 }

func terminateGroup(int) error { return nil }

func killGroup(int) error { return nil }

func groupExists(int) bool { return false }
