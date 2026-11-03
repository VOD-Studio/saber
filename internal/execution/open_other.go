//go:build !unix

package execution

// 非 Unix 平台没有对应的打开标志；os.Root 与 Lstat 仍阻止越根和链接末组件。
const (
	openNoFollow = 0
	openNonBlock = 0
)
