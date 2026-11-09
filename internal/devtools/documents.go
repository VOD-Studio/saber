package devtools

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// Extraction 保存实际提取正文与覆盖警告；Extracted 内容不能作为覆盖原二进制文件的基线。
type Extraction struct {
	Content     string   `json:"content"`
	Format      string   `json:"format"`
	Extracted   bool     `json:"extracted"`
	SourceBytes int      `json:"source_bytes"`
	Pages       int      `json:"pages,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// IsDocument 判断内置或明确配置的文档扩展名，不探测宿主文件系统。
func (m *Manager) IsDocument(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".docx" || ext == ".xlsx" || ext == ".ipynb" || ext == ".pdf" || sqliteExtension(ext) || opaqueExtensions[ext] || len(m.cfg.DocumentConverters[ext]) > 0
}
func sqliteExtension(ext string) bool { return ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" }

// Extract 只消费经过执行器安全读取的原始字节，Office/Notebook/SQLite 不运行文档中的代码。
// PDF 和外部格式转换命令仍只能在同一授权任务后端执行，绝不偷偷安装或访问宿主路径。
func (m *Manager) Extract(ctx context.Context, root, filename string, data []byte) (Extraction, error) {
	result := Extraction{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), "."), Extracted: true, SourceBytes: len(data)}
	if !m.cfg.Enabled {
		return result, errors.New("document extraction disabled")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if int64(len(data)) > m.cfg.MaxDocumentBytes {
		return result, fmt.Errorf("document exceeds %d-byte extraction limit", m.cfg.MaxDocumentBytes)
	}
	var err error
	switch "." + result.Format {
	case ".docx":
		result.Content, err = m.extractDOCX(data)
	case ".xlsx":
		result.Content, err = m.extractXLSX(data)
	case ".ipynb":
		result.Content, err = m.extractNotebook(data, filename)
	case ".db", ".sqlite", ".sqlite3":
		result.Content, err = m.extractSQLite(ctx, data)
	case ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return result, errors.New("file is not a PDF document")
		}
		timeout, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		var output []byte
		output, err = m.runProcess(timeout, root, m.cfg.PDFCommand, data, m.cfg.MaxExtractedBytes)
		if err != nil {
			return result, fmt.Errorf("PDF text extraction requires a working pdftotext command in the authorized backend (install Poppler or configure devtools.pdf_command): %w", err)
		}
		result.Content, result.Pages, result.Warnings = pdfText(output)
	default:
		command := m.cfg.DocumentConverters["."+result.Format]
		if len(command) == 0 {
			return result, fmt.Errorf("%s extraction requires devtools.document_converters.%s: configure an installed converter accepting raw bytes on stdin and UTF-8 text on stdout", result.Format, "."+result.Format)
		}
		var output []byte
		output, err = m.runProcess(ctx, root, command, data, m.cfg.MaxExtractedBytes)
		result.Content = string(output)
	}
	if err != nil {
		return result, err
	}
	if !utf8.ValidString(result.Content) || strings.IndexByte(result.Content, 0) >= 0 {
		return result, errors.New("converter did not return UTF-8 text")
	}
	if int64(len(result.Content)) > m.cfg.MaxExtractedBytes {
		return result, fmt.Errorf("extracted document exceeds %d-byte limit", m.cfg.MaxExtractedBytes)
	}
	if strings.TrimSpace(result.Content) == "" {
		return result, errors.New("document contains no extractable text")
	}
	result.Content = strings.TrimRight(result.Content, "\n") + "\n"
	return result, nil
}

type xmlNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []*xmlNode
}

func (n *xmlNode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	return n.decode(decoder, start, 0)
}
func (n *xmlNode) decode(d *xml.Decoder, start xml.StartElement, depth int) error {
	if depth > 64 {
		return errors.New("document XML nesting exceeds limit")
	}
	n.Name, n.Attrs = start.Name, start.Attr
	var text strings.Builder
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			child := new(xmlNode)
			if err := child.decode(d, t, depth+1); err != nil {
				return err
			}
			n.Children = append(n.Children, child)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			n.Text = text.String()
			return nil
		}
	}
}
func (n *xmlNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.Children {
		if c.Name.Local == name {
			return c
		}
	}
	return nil
}
func (n *xmlNode) text() string { var out strings.Builder; n.writeText(&out); return out.String() }
func (n *xmlNode) writeText(out *strings.Builder) {
	if n == nil || n.Name.Local == "rt" || n.Name.Local == "rPh" {
		return
	}
	out.WriteString(n.Text)
	for _, child := range n.Children {
		child.writeText(out)
	}
}
func (n *xmlNode) find(name string, out *[]*xmlNode) {
	if n.Name.Local == name {
		*out = append(*out, n)
	}
	for _, c := range n.Children {
		c.find(name, out)
	}
}

type documentPackage struct {
	*zip.Reader
	remaining int64
}

func openDocumentZIP(data []byte, limit int64) (*documentPackage, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid Office ZIP document: %w", err)
	}
	if len(z.File) > 10000 {
		return nil, errors.New("document package has too many parts")
	}
	return &documentPackage{Reader: z, remaining: limit}, nil
}
func (m *Manager) zipXML(z *documentPackage, name string, optional bool) (*xmlNode, error) {
	var file *zip.File
	for _, f := range z.File {
		if f.Name == name {
			file = f
			break
		}
	}
	if file == nil {
		if optional {
			return &xmlNode{}, nil
		}
		return nil, fmt.Errorf("document is missing %s", name)
	}
	if file.UncompressedSize64 > uint64(z.remaining) {
		return nil, errors.New("document exceeds aggregate decompression limit")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, z.remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > z.remaining {
		return nil, errors.New("document XML exceeds aggregate decompression limit")
	}
	z.remaining -= int64(len(data))
	var node xmlNode
	if err := xml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("invalid document XML %s: %w", name, err)
	}
	return &node, nil
}
func (m *Manager) extractDOCX(data []byte) (string, error) {
	z, err := openDocumentZIP(data, m.cfg.MaxDocumentBytes)
	if err != nil {
		return "", err
	}
	root, err := m.zipXML(z, "word/document.xml", false)
	if err != nil {
		return "", err
	}
	var paragraphs []*xmlNode
	root.find("p", &paragraphs)
	var out strings.Builder
	var render func(*xmlNode)
	render = func(n *xmlNode) {
		switch n.Name.Local {
		case "rt":
			return
		case "t":
			out.WriteString(n.Text)
		case "tab":
			out.WriteByte('\t')
		case "br", "cr":
			out.WriteByte('\n')
		}
		for _, c := range n.Children {
			render(c)
		}
	}
	for _, p := range paragraphs {
		render(p)
		out.WriteByte('\n')
		if int64(out.Len()) > m.cfg.MaxExtractedBytes {
			return "", errors.New("DOCX text exceeds extraction limit")
		}
	}
	return out.String(), nil
}
func (m *Manager) extractXLSX(data []byte) (string, error) {
	z, err := openDocumentZIP(data, m.cfg.MaxDocumentBytes)
	if err != nil {
		return "", err
	}
	workbook, err := m.zipXML(z, "xl/workbook.xml", false)
	if err != nil {
		return "", err
	}
	relations, err := m.zipXML(z, "xl/_rels/workbook.xml.rels", false)
	if err != nil {
		return "", err
	}
	sharedXML, err := m.zipXML(z, "xl/sharedStrings.xml", true)
	if err != nil {
		return "", err
	}
	var stringsXML []*xmlNode
	sharedXML.find("si", &stringsXML)
	shared := make([]string, len(stringsXML))
	for i, s := range stringsXML {
		shared[i] = s.text()
	}
	rels := make(map[string]string)
	var relationNodes []*xmlNode
	relations.find("Relationship", &relationNodes)
	for _, r := range relationNodes {
		if r.attr("TargetMode") != "External" {
			rels[r.attr("Id")] = r.attr("Target")
		}
	}
	var sheets []*xmlNode
	workbook.find("sheet", &sheets)
	var out strings.Builder
	for _, sheet := range sheets {
		if sheet.attr("state") == "hidden" || sheet.attr("state") == "veryHidden" {
			continue
		}
		target := rels[sheet.attr("id")]
		if target == "" {
			return "", errors.New("XLSX sheet relationship is missing")
		}
		part := path.Clean(path.Join("xl", target))
		if strings.HasPrefix(target, "/") {
			part = strings.TrimPrefix(path.Clean(target), "/")
		} else if strings.HasPrefix(target, "xl/") {
			part = path.Clean(target)
		}
		if !strings.HasPrefix(part, "xl/") {
			return "", errors.New("XLSX relationship escapes package workbook")
		}
		root, err := m.zipXML(z, part, false)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "# Sheet: %s\n", sheet.attr("name"))
		var rows []*xmlNode
		root.find("row", &rows)
		for ri, row := range rows {
			if ri >= 5000 {
				out.WriteString("[remaining rows omitted; inspect the workbook with a spreadsheet-aware tool]\n")
				break
			}
			values := make(map[int]string)
			maximum := -1
			for _, cell := range row.Children {
				if cell.Name.Local != "c" {
					continue
				}
				column := columnIndex(cell.attr("r"))
				if cell.attr("r") == "" {
					column = maximum + 1
				}
				if column >= 256 {
					continue
				}
				value := cell.child("v").text()
				switch cell.attr("t") {
				case "s":
					i, e := strconv.Atoi(value)
					if e != nil || i < 0 || i >= len(shared) {
						return "", errors.New("XLSX shared string index is invalid")
					}
					value = shared[i]
				case "inlineStr":
					value = cell.child("is").text()
				case "b":
					if value == "1" || strings.EqualFold(value, "true") {
						value = "TRUE"
					} else {
						value = "FALSE"
					}
				}
				values[column] = value
				if column > maximum {
					maximum = column
				}
			}
			for column := range maximum + 1 {
				if column > 0 {
					out.WriteByte('\t')
				}
				out.WriteString(values[column])
			}
			out.WriteByte('\n')
			if int64(out.Len()) > m.cfg.MaxExtractedBytes {
				return "", errors.New("XLSX text exceeds extraction limit")
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}
func columnIndex(reference string) int {
	index := 0
	for _, c := range strings.ToUpper(reference) {
		if c < 'A' || c > 'Z' {
			break
		}
		index = index*26 + int(c-'A') + 1
		if index > 256 {
			return 256
		}
	}
	if index == 0 {
		return 0
	}
	return index - 1
}

func sourceText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if list, ok := value.([]any); ok {
		var out strings.Builder
		for _, part := range list {
			if text, ok := part.(string); ok {
				out.WriteString(text)
			}
		}
		return out.String()
	}
	return ""
}
func (m *Manager) extractNotebook(data []byte, filename string) (string, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return "", fmt.Errorf("invalid notebook: %w", err)
	}
	type cellAt struct {
		pointer string
		cell    map[string]any
	}
	var cells []cellAt
	if raw, ok := notebook["cells"].([]any); ok {
		for i, value := range raw {
			if cell, ok := value.(map[string]any); ok {
				cells = append(cells, cellAt{fmt.Sprintf(".cells[%d].outputs", i), cell})
			}
		}
	} else if worksheets, ok := notebook["worksheets"].([]any); ok {
		for wi, w := range worksheets {
			worksheet, ok := w.(map[string]any)
			if !ok {
				continue
			}
			raw, _ := worksheet["cells"].([]any)
			for ci, value := range raw {
				if cell, ok := value.(map[string]any); ok {
					cells = append(cells, cellAt{fmt.Sprintf(".worksheets[%d].cells[%d].outputs", wi, ci), cell})
				}
			}
		}
	}
	if len(cells) == 0 {
		return "", errors.New("notebook contains no readable cells")
	}
	var out strings.Builder
	for i, item := range cells {
		kind, _ := item.cell["cell_type"].(string)
		if kind != "code" && kind != "markdown" && kind != "raw" {
			continue
		}
		fmt.Fprintf(&out, "# %s cell %d\n%s\n\n", kind, i+1, sourceText(item.cell["source"]))
		if kind == "code" {
			outputs, _ := item.cell["outputs"].([]any)
			var parts []string
			for _, raw := range outputs {
				output, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if text := notebookOutput(output); text != "" {
					parts = append(parts, text)
				}
			}
			rendered := strings.Join(parts, "\n")
			characters := utf8.RuneCountInString(rendered)
			if characters > m.cfg.NotebookOutputCharacters {
				rendered = prefixCharacters(rendered, m.cfg.NotebookOutputCharacters) + fmt.Sprintf("\n[output truncated by %d characters; full output: jq -r '%s' %s]", characters-m.cfg.NotebookOutputCharacters, item.pointer, shellQuote(filename))
			}
			if rendered != "" {
				fmt.Fprintf(&out, "## Output\n%s\n\n", rendered)
			}
		}
		if int64(out.Len()) > m.cfg.MaxExtractedBytes {
			return "", errors.New("notebook text exceeds extraction limit")
		}
	}
	return out.String(), nil
}
func shellQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'" }
func notebookOutput(output map[string]any) string {
	kind, _ := output["output_type"].(string)
	switch kind {
	case "stream":
		return cleanOutput(sourceText(output["text"]))
	case "error", "pyerr":
		trace, _ := output["traceback"].([]any)
		var lines []string
		for _, line := range trace {
			if text, ok := line.(string); ok {
				lines = append(lines, text)
			}
		}
		return fmt.Sprintf("Error: %v: %v\n%s", output["ename"], output["evalue"], cleanOutput(strings.Join(lines, "\n")))
	case "execute_result", "display_data", "pyout":
	default:
		return ""
	}
	data, ok := output["data"].(map[string]any)
	if !ok {
		data = make(map[string]any)
		if text := sourceText(output["text"]); text != "" {
			data["text/plain"] = text
		}
		for key, mime := range map[string]string{"png": "image/png", "jpeg": "image/jpeg", "svg": "image/svg+xml", "html": "text/html"} {
			if value, ok := output[key]; ok {
				data[mime] = value
			}
		}
	}
	if _, ok := data["application/vnd.jupyter.widget-view+json"]; ok {
		return "[interactive widget omitted]"
	}
	for _, mime := range []string{"text/plain", "text/markdown"} {
		if text := sourceText(data[mime]); text != "" {
			return cleanOutput(text)
		}
	}
	var keys []string
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasPrefix(key, "image/") {
			return fmt.Sprintf("[%s output, %d encoded characters; image omitted]", key, len(sourceText(data[key])))
		}
	}
	if text := sourceText(data["text/html"]); text != "" {
		return fmt.Sprintf("[text/html output, %d characters; markup omitted]", utf8.RuneCountInString(text))
	}
	return "[" + strings.Join(keys, ", ") + " output omitted]"
}
func cleanOutput(text string) string {
	var out strings.Builder
	escape := false
	for _, c := range text {
		if escape {
			if c >= '@' && c <= '~' && c != '[' {
				escape = false
			}
			continue
		}
		if c == 27 {
			escape = true
			continue
		}
		if c >= 32 || c == '\n' || c == '\t' || c == '\r' {
			out.WriteRune(c)
		}
	}
	lines := strings.Split(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if p := strings.LastIndexByte(line, '\r'); p >= 0 {
			lines[i] = line[p+1:]
		}
	}
	return strings.Join(lines, "\n")
}
func pdfText(data []byte) (string, int, []string) {
	pages := strings.Split(string(data), "\f")
	if len(pages) > 1 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	empty := make([]bool, len(pages))
	missing := 0
	for i, p := range pages {
		if utf8.RuneCountInString(strings.TrimSpace(p)) < 20 {
			empty[i] = true
			missing++
		}
	}
	var warnings []string
	if missing >= 10 || missing >= 2 && float64(missing)/float64(len(pages)) > 0.2 {
		var gaps strings.Builder
		last := "start of document"
		entries := 0
		for i := 0; i < len(pages); i++ {
			if !empty[i] {
				text := strings.TrimSpace(pages[i])
				lines := strings.Split(text, "\n")
				last = lines[len(lines)-1]
				if utf8.RuneCountInString(last) > 60 {
					last = string([]rune(last)[:60])
				}
				continue
			}
			start := i
			for i+1 < len(pages) && empty[i+1] {
				i++
			}
			if entries < 20 {
				fmt.Fprintf(&gaps, " pages %d-%d after %q;", start+1, i+1, last)
			}
			entries++
		}
		warnings = append(warnings, fmt.Sprintf("EXTRACTION COVERAGE WARNING: %d of %d PDF pages have no meaningful text layer.%s Render only needed pages with pdftoppm -f FIRST -l LAST or use an explicitly configured OCR provider; do not interpret missing text as empty source pages.", missing, len(pages), gaps.String()))
	}
	if missing == len(pages) && len(warnings) == 0 {
		warnings = append(warnings, "NEEDS OCR: PDF has no meaningful text layer; render selected pages or use an explicitly configured OCR provider")
	}
	var out strings.Builder
	for _, w := range warnings {
		fmt.Fprintf(&out, "[%s]\n", w)
	}
	for i, p := range pages {
		fmt.Fprintf(&out, "## Page %d\n%s\n", i+1, strings.TrimSpace(p))
	}
	return out.String(), len(pages), warnings
}
func (m *Manager) extractSQLite(ctx context.Context, data []byte) (content string, resultErr error) {
	if !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return "", errors.New("file does not contain a SQLite database")
	}
	file, err := os.CreateTemp("", "saber-document-*.sqlite")
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer func() { resultErr = errors.Join(resultErr, os.Remove(name)) }()
	if _, err = file.Write(data); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	uri := (&url.URL{Scheme: "file", Path: name, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	db.SetMaxOpenConns(1)
	timeout, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	rows, err := db.QueryContext(timeout, "SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name LIMIT 201")
	if err != nil {
		return "", err
	}
	type schema struct {
		kind, name string
		statement  sql.NullString
	}
	var schemas []schema
	for rows.Next() {
		var s schema
		if err := rows.Scan(&s.kind, &s.name, &s.statement); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		schemas = append(schemas, s)
	}
	if err := rows.Err(); err != nil {
		return "", errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	var out strings.Builder
	for i, s := range schemas {
		if i >= 200 {
			out.WriteString("[remaining schema objects omitted]\n")
			break
		}
		fmt.Fprintf(&out, "# %s: %s\n%s\n", s.kind, s.name, s.statement.String)
		if s.kind == "table" {
			if err := writeSQLiteTablePreview(timeout, db, s.name, &out); err != nil {
				return "", err
			}
		}
		if int64(out.Len()) > m.cfg.MaxExtractedBytes {
			return "", errors.New("SQLite schema preview exceeds extraction limit")
		}
	}
	return out.String(), nil
}
func sqliteCell(value any) string {
	if value == nil {
		return "NULL"
	}
	if blob, ok := value.([]byte); ok {
		return fmt.Sprintf("[BLOB %d bytes]", len(blob))
	}
	text, ok := value.(string)
	if !ok {
		text = fmt.Sprint(value)
	}
	if utf8.RuneCountInString(text) > 80 {
		text = prefixCharacters(text, 79) + "…"
	}
	return strings.ReplaceAll(text, "\n", "\\n")
}

func prefixCharacters(text string, count int) string {
	seen := 0
	for index := range text {
		if seen == count {
			return text[:index]
		}
		seen++
	}
	return text
}

func writeSQLiteTablePreview(ctx context.Context, db *sql.DB, name string, out *strings.Builder) (resultErr error) {
	quoted := "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quoted).Scan(&count); err != nil {
		return err
	}
	fmt.Fprintf(out, "Rows: %d\n", count)
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+quoted+" LIMIT 5")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	out.WriteString(strings.Join(columns, "\t") + "\n")
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		for i, value := range values {
			if i > 0 {
				out.WriteByte('\t')
			}
			out.WriteString(sqliteCell(value))
		}
		out.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.WriteByte('\n')
	return nil
}
