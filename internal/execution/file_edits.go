package execution

import "unicode/utf8"

// FileEdit 保存真实写前和写后文本；Truncated 为真时只能展示局部预览，不得假装完整 diff。
type FileEdit struct {
	Path      string `json:"path"`
	OldText   string `json:"old_text"`
	NewText   string `json:"new_text"`
	Created   bool   `json:"created"`
	OldBytes  int    `json:"old_bytes"`
	NewBytes  int    `json:"new_bytes"`
	Truncated bool   `json:"truncated"`
}

func editPrefix(data []byte) (string, bool) {
	if len(data) <= 64<<10 {
		return string(data), false
	}
	end := 64 << 10
	for end > 0 && !utf8.RuneStart(data[end]) {
		end--
	}
	return string(data[:end]), true
}
func newFileEdit(path string, before, after []byte, created bool) FileEdit {
	old, oldTrimmed := editPrefix(before)
	next, newTrimmed := editPrefix(after)
	return FileEdit{Path: path, OldText: old, NewText: next, Created: created, OldBytes: len(before), NewBytes: len(after), Truncated: oldTrimmed || newTrimmed}
}
