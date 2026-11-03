//go:build unix

package execution

import "syscall"

// openNoFollow 禁止末组件为链接；openNonBlock 防止 FIFO 等特殊文件阻塞打开。
const (
	openNoFollow = syscall.O_NOFOLLOW
	openNonBlock = syscall.O_NONBLOCK
)
