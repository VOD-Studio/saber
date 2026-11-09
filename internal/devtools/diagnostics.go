package devtools

import (
	"encoding/json"
	"sort"
	"strings"
)

type lineAnchor struct{ before, after int }

// IntroducedDiagnostics 过滤预先存在的错误，同时跟踪插入/删除导致的行偏移。
func IntroducedDiagnostics(before, after []byte, baseline, current []Diagnostic) []Diagnostic {
	return introducedDiagnosticsForFile(before, after, baseline, current, "")
}
func introducedDiagnosticsForFile(before, after []byte, baseline, current []Diagnostic, editedFile string) []Diagnostic {
	pre, post := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	mapping := make([]int, len(pre))
	for i := range mapping {
		mapping[i] = -1
	}
	mapUnchangedLines(pre, post, 0, len(pre), 0, len(post), mapping, 0)
	seen := make(map[string]bool, len(baseline))
	for _, d := range baseline {
		if d.File != "" && d.File != editedFile {
			seen[diagnosticKey(d)] = true
			continue
		}
		start, end := d.Range.Start.Line, d.Range.End.Line
		if start < 0 || start >= len(mapping) || mapping[start] < 0 {
			continue
		}
		d.Range.Start.Line = mapping[start]
		if end >= 0 && end < len(mapping) && mapping[end] >= 0 {
			d.Range.End.Line = mapping[end]
		} else {
			d.Range.End.Line = d.Range.Start.Line
		}
		seen[diagnosticKey(d)] = true
	}
	out := make([]Diagnostic, 0, len(current))
	for _, d := range current {
		if !seen[diagnosticKey(d)] {
			out = append(out, d)
		}
	}
	return out
}
func diagnosticKey(d Diagnostic) string {
	key := struct {
		File            string
		Range           Range
		Severity        int
		Code            any
		Source, Message string
	}{d.File, d.Range, d.Severity, d.Code, d.Source, d.Message}
	data, _ := json.Marshal(key)
	return string(data)
}

// mapUnchangedLines 用 patience 锚点和首尾相等段建立映射；线性内存，不对大文件分配二次矩阵。
func mapUnchangedLines(pre, post []string, a0, a1, b0, b1 int, mapping []int, depth int) {
	for a0 < a1 && b0 < b1 && pre[a0] == post[b0] {
		mapping[a0] = b0
		a0++
		b0++
	}
	for a0 < a1 && b0 < b1 && pre[a1-1] == post[b1-1] {
		a1--
		b1--
		mapping[a1] = b1
	}
	if a0 == a1 || b0 == b1 || depth > 64 {
		return
	}
	preCount, postCount := make(map[string]int, a1-a0), make(map[string]int, b1-b0)
	positions := make(map[string]int, b1-b0)
	for i := a0; i < a1; i++ {
		preCount[pre[i]]++
	}
	for i := b0; i < b1; i++ {
		postCount[post[i]]++
		positions[post[i]] = i
	}
	anchors := make([]lineAnchor, 0)
	for i := a0; i < a1; i++ {
		if preCount[pre[i]] == 1 && postCount[pre[i]] == 1 {
			anchors = append(anchors, lineAnchor{i, positions[pre[i]]})
		}
	}
	if len(anchors) == 0 {
		return
	}
	tails := make([]int, 0, len(anchors))
	previous := make([]int, len(anchors))
	for i, anchor := range anchors {
		j := sort.Search(len(tails), func(j int) bool { return anchors[tails[j]].after >= anchor.after })
		previous[i] = -1
		if j > 0 {
			previous[i] = tails[j-1]
		}
		if j == len(tails) {
			tails = append(tails, i)
		} else {
			tails[j] = i
		}
	}
	chain := make([]lineAnchor, len(tails))
	index := tails[len(tails)-1]
	for i := len(chain) - 1; i >= 0; i-- {
		chain[i] = anchors[index]
		index = previous[index]
	}
	for _, anchor := range chain {
		mapUnchangedLines(pre, post, a0, anchor.before, b0, anchor.after, mapping, depth+1)
		mapping[anchor.before] = anchor.after
		a0, b0 = anchor.before+1, anchor.after+1
	}
	mapUnchangedLines(pre, post, a0, a1, b0, b1, mapping, depth+1)
}
