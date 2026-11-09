package devtools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWholeFileOverwriteRequiresCurrentCompleteRead(t *testing.T) {
	m := testManager(t)
	original := []byte("first\nsecond\n")
	digest := Digest(original)
	if err := m.CheckOverwrite("task", "file.go", original, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatalf("blind overwrite allowed: %v", err)
	}
	m.ObserveRead("task", "file.go", digest, 0, 6, int64(len(original)), false)
	if err := m.CheckOverwrite("task", "file.go", original, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("partial read authorized replacement")
	}
	m.ObserveRead("task", "file.go", digest, 6, int64(len(original)), int64(len(original)), false)
	if err := m.CheckOverwrite("task", "file.go", original, true); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckOverwrite("other-task", "file.go", original, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("another task inherited baseline")
	}
	changed := []byte("first\nexternal\n")
	if err := m.CheckOverwrite("task", "file.go", changed, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("same-length external edit was accepted")
	}
	if err := m.CheckOverwrite("task", "new.go", nil, false); err != nil {
		t.Fatal("new-file creation was blocked")
	}
}
func TestRedactedReadAndBlindPatchCannotAuthorizeReplacement(t *testing.T) {
	m := testManager(t)
	before := []byte("before")
	after := []byte("after")
	m.ObserveRead("task", "file.txt", Digest(before), 0, int64(len(before)), int64(len(before)), true)
	if err := m.CheckOverwrite("task", "file.txt", before, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("redacted read authorized overwrite")
	}
	m.Written("task", "file.txt", before, after, false)
	if err := m.CheckOverwrite("task", "file.txt", after, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("blind patch granted whole-file baseline")
	}
	if err := m.Snapshot(context.Background(), "task", "file.txt", after); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckOverwrite("task", "file.txt", after, true); err != nil {
		t.Fatal("fresh full read did not restore authorization", err)
	}
	m.Written("task", "file.txt", after, before, false)
	if err := m.CheckOverwrite("task", "file.txt", before, true); err != nil {
		t.Fatal("known patch lost full baseline", err)
	}
	m.ForgetScope("task")
	if err := m.CheckOverwrite("task", "file.txt", before, true); !errors.Is(err, ErrStaleOverwrite) {
		t.Fatal("forgotten context retained overwrite authorization")
	}
}
func TestTextToolsRefuseBinaryContainersAndSQLiteSidecars(t *testing.T) {
	for _, filename := range []string{"document.docx", "sheet.xlsx", "db.sqlite-wal", "db.sqlite3-shm", "db.db-journal"} {
		if err := CheckTextWrite(filename, false, nil); err == nil {
			t.Fatalf("text creation would corrupt %s", filename)
		}
	}
	if err := CheckTextWrite("fixture.pdf", false, nil); err != nil {
		t.Fatal("new raw PDF fixture refused", err)
	}
	if err := CheckTextWrite("existing.pdf", true, []byte("%PDF")); err == nil {
		t.Fatal("existing PDF text overwrite accepted")
	}
	if ConflictBlocks("<<<<<<< ours\na\n=======\nb\n>>>>>>> theirs\n") != 1 || ConflictBlocks("<<<<<<< string literal\n") != 0 {
		t.Fatal("balanced conflict detection failed")
	}
}
func TestDiagnosticDeltaPreservesLineShiftsAndNewErrors(t *testing.T) {
	before := []byte("header\nold error\ntail\n")
	after := []byte("new line\nheader\nold error\nnew error\ntail\n")
	baseline := []Diagnostic{{Range: Range{Start: Position{1, 0}, End: Position{1, 9}}, Severity: 1, Message: "old problem", Source: "real-server"}}
	current := []Diagnostic{{Range: Range{Start: Position{2, 0}, End: Position{2, 9}}, Severity: 1, Message: "old problem", Source: "real-server"}, {Range: Range{Start: Position{3, 0}, End: Position{3, 9}}, Severity: 1, Message: "new problem", Source: "real-server"}}
	introduced := IntroducedDiagnostics(before, after, baseline, current)
	if len(introduced) != 1 || introduced[0].Message != "new problem" {
		t.Fatalf("wrong diagnostic delta: %#v", introduced)
	}
}
func TestReferencesParseQuotedPathsRangesAndBalancedURLs(t *testing.T) {
	refs := ParseReferences("email a@file:no @file:'a b.go':2-4, @folder:src/ @diff @staged @git:20 @url:https://example.test/a_(b).")
	if len(refs) != 6 {
		t.Fatalf("wrong reference count: %#v", refs)
	}
	if refs[0].Target != "a b.go" || refs[0].LineStart != 2 || refs[0].LineEnd != 4 {
		t.Fatalf("quoted range lost: %#v", refs[0])
	}
	if refs[5].Target != "https://example.test/a_(b)" {
		t.Fatalf("balanced URL damaged: %#v", refs[5])
	}
	args, err := GitReferenceArgs(refs[4])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--max-count=10") || !strings.Contains(strings.Join(args, " "), "--no-textconv") {
		t.Fatalf("unsafe Git args: %v", args)
	}
}
func TestReferenceExpansionUsesSourceAndRefusesOversizedContext(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	filename := filepath.Join(root, "real.txt")
	if err := os.WriteFile(filename, []byte("alpha\nbeta\ngamma\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := ReferenceSource{Read: func(ctx context.Context, target string, start, end, limit int) (string, error) {
		if target != "real.txt" {
			return "", errors.New("permission denied")
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if start > 0 {
			return strings.Join(lines[start-1:end], "\n"), nil
		}
		return string(data), nil
	}}
	result, err := m.ExpandReferences(context.Background(), "Read @file:real.txt:2-2", source, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "\nbeta\n") || strings.Contains(result.Message, "gamma") {
		t.Fatalf("wrong line range: %s", result.Message)
	}
	original := "@file:real.txt"
	result, err = m.ExpandReferences(context.Background(), original, source, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked || result.Message != original {
		t.Fatalf("hard limit did not preserve original: %#v", result)
	}
	result, err = m.ExpandReferences(context.Background(), "@file:denied.txt", source, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "permission denied") {
		t.Fatal("permission denial hidden", result.Warnings)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = m.ExpandReferences(ctx, original, source, 4096); err != context.Canceled {
		t.Fatal("reference cancellation lost", err)
	}
}

func TestStructuredPreflightRejectsActualParseErrors(t *testing.T) {
	m := testManager(t)
	for filename, content := range map[string]string{"broken.json": "{not json}", "broken.yaml": "key: [unterminated"} {
		if err := m.Preflight(context.Background(), "", filename, []byte(content)); err == nil {
			t.Fatalf("invalid %s was allowed before write", filename)
		}
	}
	if err := m.Preflight(context.Background(), "", "valid.json", []byte(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
}
func TestCustomReferencesConsumeCurrentRegistrationAndCannotOverrideBuiltins(t *testing.T) {
	m := testManager(t)
	filename := filepath.Join(t.TempDir(), "plugin-context.txt")
	if err := os.WriteFile(filename, []byte("stored plugin context"), 0600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	provider := func(ctx context.Context, target string, limit int) (string, error) {
		if target != "record" {
			return "", errors.New("unknown record")
		}
		data, err := os.ReadFile(filename)
		return string(data), err
	}
	m.SetReferenceProviders(func(context.Context) map[string]func(context.Context, string, int) (string, error) {
		if !enabled {
			return nil
		}
		return map[string]func(context.Context, string, int) (string, error){"plugin-records": provider, "file": provider}
	})
	result, err := m.ExpandReferences(context.Background(), "@plugin-records:record", ReferenceSource{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "stored plugin context") {
		t.Fatal("registered context was not consumed")
	}
	result, err = m.ExpandReferences(context.Background(), "@file:record", ReferenceSource{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Message, "stored plugin context") || len(result.Warnings) != 1 {
		t.Fatal("plugin replaced builtin file authorization")
	}
	enabled = false
	original := "@plugin-records:record"
	result, err = m.ExpandReferences(context.Background(), original, ReferenceSource{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != original || len(result.References) != 0 {
		t.Fatal("disabled provider remained callable")
	}
}

func TestReferenceKindsCompleteAndPlainMessagesKeepTheirContextBudget(t *testing.T) {
	m := testManager(t)
	suggestions, err := m.ContextCompletions(context.Background(), "Review @fi", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) != 1 || suggestions[0].Value != "@file:" {
		t.Fatalf("wrong reference kind completion: %#v", suggestions)
	}
	git, err := m.ContextCompletions(context.Background(), "@git:1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(git) != 2 || git[0].Value != "@git:1" || git[1].Value != "@git:10" {
		t.Fatalf("wrong bounded Git target suggestions: %#v", git)
	}
	message := strings.Repeat("ordinary text ", 100)
	result, err := m.ExpandReferences(context.Background(), message, ReferenceSource{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked || result.Message != message {
		t.Fatal("plain text was blocked by reference-only bounds")
	}
}
