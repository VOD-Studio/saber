package devtools

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rua.plus/saber/internal/config"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(config.DefaultDevToolsConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func documentZIP(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for name, content := range parts {
		file, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestDOCXExtractsBaseTextWithoutRubyAnnotations(t *testing.T) {
	m := testManager(t)
	data := documentZIP(t, map[string]string{"word/document.xml": `<w:document xmlns:w="urn:word"><w:body><w:p><w:r><w:t>Hello</w:t><w:tab/></w:r><w:ruby><w:rt><w:r><w:t>トウキョウ</w:t></w:r></w:rt><w:rubyBase><w:r><w:t>東京</w:t></w:r></w:rubyBase></w:ruby></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Table cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`})
	result, err := m.Extract(context.Background(), "", "report.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Hello\t東京") || !strings.Contains(result.Content, "Table cell") || strings.Contains(result.Content, "トウキョウ") {
		t.Fatalf("wrong extracted values: %q", result.Content)
	}
	if err := m.CheckOverwrite("task", "report.docx", data, true); err == nil {
		t.Fatal("extracted document was allowed to be replaced with text")
	}
}
func TestXLSXResolvesSheetsSparseCellsAndPhoneticGuides(t *testing.T) {
	m := testManager(t)
	data := documentZIP(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="urn:rel"><sheets><sheet name="Visible" r:id="r1"/><sheet name="Secret" r:id="r2" state="hidden"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/><Relationship Id="r2" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>東京</t><rPh><t>トウキョウ</t></rPh></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="inlineStr"><is><r><t>value</t></r></is></c><c r="D1" t="b"><v>1</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row><c><v>hidden value</v></c></row></sheetData></worksheet>`,
	})
	result, err := m.Extract(context.Background(), "", "book.xlsx", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "東京\t\tvalue\tTRUE") || strings.Contains(result.Content, "トウキョウ") || strings.Contains(result.Content, "hidden value") {
		t.Fatalf("wrong workbook values: %q", result.Content)
	}
}
func TestNotebookOutputsAreBoundedAndRecoverable(t *testing.T) {
	cfg := config.DefaultDevToolsConfig()
	cfg.NotebookOutputCharacters = 12
	m, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	filename := "a notebook's output.ipynb"
	data := []byte(`{"nbformat":4,"cells":[{"cell_type":"code","source":["print('result')\n"],"outputs":[{"output_type":"stream","text":"abcdefghijklmnopqrstuvwxyz"}]},{"cell_type":"markdown","source":"Actual notes"}]}`)
	result, err := m.Extract(context.Background(), "", filename, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "abcdefghijkl") || strings.Contains(result.Content, "abcdefghijklmnop") || !strings.Contains(result.Content, ".cells[0].outputs") || !strings.Contains(result.Content, shellQuote(filename)) || !strings.Contains(result.Content, "Actual notes") {
		t.Fatalf("wrong notebook output: %q", result.Content)
	}
}
func TestSQLiteExtractionReadsSchemaAndActualRowsWithoutMutatingSource(t *testing.T) {
	m := testManager(t)
	filename := filepath.Join(t.TempDir(), "data.sqlite")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE "a quoted table" (name TEXT, data BLOB); INSERT INTO "a quoted table" VALUES ('東京', x'0102'); CREATE INDEX by_name ON "a quoted table"(name)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Extract(context.Background(), "", filename, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "東京") || !strings.Contains(result.Content, "Rows: 1") || !strings.Contains(result.Content, "[BLOB 2 bytes]") || !strings.Contains(result.Content, "by_name") {
		t.Fatalf("wrong SQLite preview: %q", result.Content)
	}
	current, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, current) {
		t.Fatal("extraction changed the source database")
	}
}
func TestMalformedDocumentsAndScannedPDFCoverage(t *testing.T) {
	m := testManager(t)
	for _, name := range []string{"broken.docx", "broken.xlsx", "broken.ipynb", "broken.sqlite"} {
		if _, err := m.Extract(context.Background(), "", name, []byte("not a document")); err == nil {
			t.Fatalf("malformed %s was accepted", name)
		}
	}
	text, pages, warnings := pdfText([]byte("Substantial first page text describing a divider.\f\f\fLast page contains a sufficiently long body.\f"))
	if pages != 4 || len(warnings) != 1 || !strings.Contains(text, "pages 2-3") {
		t.Fatalf("coverage gap missing: pages=%d warning=%v", pages, warnings)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Extract(ctx, "", "test.docx", nil); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestOfficeDecompressionAndConfigurationLimitsAreEnforced(t *testing.T) {
	cfg := config.DefaultDevToolsConfig()
	cfg.MaxDocumentBytes = 1024
	m, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	data := documentZIP(t, map[string]string{"word/document.xml": "<document><p><t>" + strings.Repeat("A", 5000) + "</t></p></document>"})
	if len(data) >= 1024 {
		t.Fatal("fixture must fit the compressed-byte limit")
	}
	if _, err := m.Extract(context.Background(), "", "compressed.docx", data); err == nil {
		t.Fatal("compressed Office document bypassed its inflated-byte limit")
	}
	invalid := config.DefaultDevToolsConfig()
	invalid.TrustedWorkspaces = []string{"relative/project"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("relative trust root accepted")
	}
	invalid = config.DefaultDevToolsConfig()
	invalid.Servers = map[string]config.LanguageServerConfig{"custom": {Command: []string{"tool", "\x00"}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("NUL program argument accepted")
	}
	invalid = config.DefaultDevToolsConfig()
	invalid.Servers = map[string]config.LanguageServerConfig{"custom": {RootMarkers: []string{"../outside"}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("project marker traversal accepted")
	}
}
