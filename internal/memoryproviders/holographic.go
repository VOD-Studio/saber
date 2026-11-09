package memoryproviders

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// HRR 相位编码与 HRR1 序列化逐式实现 itpartypattaya/hermes-plugin-holographic/holographic.py。
// SQLite 使用同样的 facts/entities/FTS5 基础模型；作用域独立文件，不以 profile 共享隐式合并用户。
type holographicBackend struct {
	nativeLocalState
	root        string
	dim         int
	trust       float64
	minTrust    float64
	hrrWeight   float64
	autoExtract bool
	storesMu    sync.Mutex
	stores      map[string]*sql.DB
}

type holographicFact struct {
	ID         int64    `json:"fact_id"`
	Content    string   `json:"content"`
	Category   string   `json:"category"`
	Tags       string   `json:"tags"`
	Keywords   []string `json:"keywords"`
	Entities   []string `json:"entities"`
	Trust      float64  `json:"trust_score"`
	Retrievals int64    `json:"retrieval_count"`
	Helpful    int64    `json:"helpful_count"`
	Created    string   `json:"created_at"`
	Updated    string   `json:"updated_at"`
	Origin     string   `json:"origin"`
	ValidFrom  string   `json:"valid_from,omitempty"`
	ValidTo    string   `json:"valid_to,omitempty"`
	Active     bool     `json:"active"`
	Score      float64  `json:"score,omitempty"`
	vector     []float64
}

const holographicSchema = `
PRAGMA foreign_keys=ON;
PRAGMA secure_delete=ON;
PRAGMA busy_timeout=1000;
CREATE TABLE IF NOT EXISTS facts (
 fact_id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL UNIQUE,
 category TEXT NOT NULL DEFAULT 'general', tags TEXT NOT NULL DEFAULT '',
 trust_score REAL NOT NULL DEFAULT 0.5, retrieval_count INTEGER NOT NULL DEFAULT 0,
 helpful_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 hrr_vector BLOB NOT NULL, keywords TEXT NOT NULL DEFAULT '[]', origin TEXT NOT NULL DEFAULT '',
 valid_from TEXT NOT NULL DEFAULT '', valid_to TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS entities (
 entity_id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE,
 entity_type TEXT NOT NULL DEFAULT 'declared', aliases TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS entity_aliases (alias_key TEXT PRIMARY KEY, entity_id INTEGER NOT NULL REFERENCES entities(entity_id) ON DELETE CASCADE, alias TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS fact_entities (fact_id INTEGER NOT NULL REFERENCES facts(fact_id) ON DELETE CASCADE, entity_id INTEGER NOT NULL REFERENCES entities(entity_id) ON DELETE CASCADE, PRIMARY KEY(fact_id,entity_id));
CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(content,tags,keywords,content=facts,content_rowid=fact_id);
CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN INSERT INTO facts_fts(rowid,content,tags,keywords) VALUES(new.fact_id,new.content,new.tags,new.keywords); END;
CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN INSERT INTO facts_fts(facts_fts,rowid,content,tags,keywords) VALUES('delete',old.fact_id,old.content,old.tags,old.keywords); END;
CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE OF content,tags,keywords ON facts BEGIN INSERT INTO facts_fts(facts_fts,rowid,content,tags,keywords) VALUES('delete',old.fact_id,old.content,old.tags,old.keywords); INSERT INTO facts_fts(rowid,content,tags,keywords) VALUES(new.fact_id,new.content,new.tags,new.keywords); END;
CREATE TABLE IF NOT EXISTS native_fact_versions (version_id INTEGER PRIMARY KEY AUTOINCREMENT, fact_id INTEGER NOT NULL REFERENCES facts(fact_id) ON DELETE CASCADE, operation TEXT NOT NULL, snapshot TEXT NOT NULL, session_id TEXT NOT NULL, recorded_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS native_deleted_entities (name_key TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS native_mirrors (entry_id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL REFERENCES facts(fact_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS native_checkpoints (checkpoint_id TEXT PRIMARY KEY, payload BLOB NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_facts_trust ON facts(trust_score DESC);
CREATE INDEX IF NOT EXISTS idx_fact_versions ON native_fact_versions(fact_id,version_id);
`

// NewHolographic 创建真正的本地 SQLite/FTS5/HRR 后端，不使用网络或 Python 包装器。
func NewHolographic(cfg config.MemoryBackendConfig, host toolkit.Host) (Backend, error) {
	root := nativeLocalString(cfg.Options, "db_path")
	if root == "" {
		if host.StateDir == "" {
			return nil, errors.New("holographic requires private state_dir or options.db_path")
		}
		root = filepath.Join(host.StateDir, "holographic")
	}
	// db_path 是作用域库的根路径；任何调用仍必须得到独立 scope.db，不能共享一个指定文件。
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dim, err := nativeLocalInt(cfg.Options, "hrr_dim", 1024, 8192)
	if err != nil {
		return nil, err
	}
	readNumber := func(key string, fallback float64) (float64, error) {
		if value, ok := cfg.Options[key]; ok {
			n, err := nativeLocalNumber(value)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1 {
				return 0, fmt.Errorf("%s must be 0..1", key)
			}
			return n, nil
		}
		return fallback, nil
	}
	trust, err := readNumber("default_trust", 0.5)
	if err != nil {
		return nil, err
	}
	minTrust, err := readNumber("min_trust_threshold", 0.3)
	if err != nil {
		return nil, err
	}
	weight, err := readNumber("hrr_weight", 0.3)
	if err != nil {
		return nil, err
	}
	autoExtract := true
	if value, ok := cfg.Options["auto_extract"]; ok {
		var valid bool
		autoExtract, valid = value.(bool)
		if !valid {
			return nil, errors.New("auto_extract must be boolean")
		}
	}
	return &holographicBackend{nativeLocalState: newNativeLocalState(cfg, host), root: root, dim: dim, trust: trust, minTrust: minTrust, hrrWeight: weight, autoExtract: autoExtract, stores: make(map[string]*sql.DB)}, nil
}

func (b *holographicBackend) store(ctx context.Context, scope string) (*sql.DB, error) {
	if db := b.stores[scope]; db != nil {
		return db, nil
	}
	if err := os.MkdirAll(b.root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(b.root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("holographic state directory cannot be a symlink")
	}
	if err := os.Chmod(b.root, 0700); err != nil {
		return nil, err
	}
	filename := filepath.Join(b.root, scope+".db")
	if info, err := os.Lstat(filename); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("holographic database must be a regular private file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(filename, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, holographicSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("holographic SQLite initialization: %w", err)
	}
	b.stores[scope] = db
	return db, nil
}

func (b *holographicBackend) Close() error {
	_ = b.nativeLocalState.Close()
	b.storesMu.Lock()
	defer b.storesMu.Unlock()
	var errs []error
	for _, db := range b.stores {
		if err := db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	b.stores = make(map[string]*sql.DB)
	return errors.Join(errs...)
}

func (b *holographicBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if operation == "shutdown" {
		return nil, b.Close()
	}
	scope, _ := ScopedIdentity(ctx)
	session, err := nativeLocalSession(ctx)
	if err != nil {
		return nil, err
	}
	payload := nativeLocalPayload(args)
	operation, payload, err = nativeLocalToolCall(operation, payload)
	if err != nil {
		return nil, err
	}
	if operation == "tools" {
		return holographicTools(), nil
	}
	b.storesMu.Lock()
	defer b.storesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := b.store(ctx, scope)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "initialize":
		return b.stats(ctx, db)
	case "context", "prefetch", "turn_start":
		query := nativeLocalString(payload, "query")
		if query == "" {
			query = nativeLocalString(payload, "message")
		}
		facts, err := b.search(ctx, db, query, payload, 5)
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, fact := range facts {
			_, _ = fmt.Fprintf(&text, "#%d [trust %.2f] %s\n", fact.ID, fact.Trust, fact.Content)
		}
		return map[string]any{"context": text.String(), "facts": facts}, nil
	case "sync_turn":
		user, _, _, err := nativeLocalTurn(payload)
		if err != nil {
			return nil, err
		}
		return b.extract(ctx, db, session, []string{user})
	case "pre_compress", "session_end":
		data, err := json.Marshal(payload["messages"])
		if err != nil {
			return nil, err
		}
		var messages []map[string]any
		if err = json.Unmarshal(data, &messages); err != nil {
			return nil, errors.New("holographic history must be message array")
		}
		texts := make([]string, 0, len(messages))
		for _, message := range messages {
			if nativeLocalString(message, "role") == "user" {
				texts = append(texts, nativeLocalString(message, "content"))
			}
		}
		result, err := b.extract(ctx, db, session, texts)
		if err != nil {
			return nil, err
		}
		if operation == "pre_compress" {
			if len(data) > 2*1024*1024 {
				return nil, errors.New("holographic checkpoint exceeds 2 MiB")
			}
			digest := sha256.Sum256(data)
			id := scope + ":" + fmt.Sprintf("%x", digest[:])
			if _, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO native_checkpoints(checkpoint_id,payload,created_at) VALUES(?,?,?)", id, data, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return nil, err
			}
			return map[string]any{"checkpointed": true, "checkpoint_id": id, "result": result}, nil
		}
		return result, nil
	case "session_switch":
		return b.stats(ctx, db)
	case "delegation":
		return nil, errors.New("holographic does not automatically attribute delegated transcripts; store explicit facts instead")
	case "memory_write":
		return b.mirror(ctx, db, session, payload)
	case "fact_store":
		return b.factTool(ctx, db, session, payload)
	case "fact_feedback":
		id, err := holographicID(payload, "fact_id")
		if err != nil {
			return nil, err
		}
		fact, err := b.get(ctx, db, id)
		if err != nil {
			return nil, err
		}
		switch nativeLocalString(payload, "action") {
		case "helpful":
			fact.Trust = math.Min(1, fact.Trust+0.05)
			fact.Helpful++
		case "unhelpful":
			fact.Trust = math.Max(0, fact.Trust-0.10)
		case "confirmed":
			fact.Trust = math.Min(1, fact.Trust+0.05)
			fact.Origin = "confirmed"
		default:
			return nil, errors.New("fact_feedback requires helpful, unhelpful or confirmed")
		}
		if err := b.save(ctx, db, session, "feedback", fact); err != nil {
			return nil, err
		}
		return fact, nil
	default:
		return nil, fmt.Errorf("holographic does not support operation %q", operation)
	}
}

func (b *holographicBackend) factTool(ctx context.Context, db *sql.DB, session string, args map[string]any) (any, error) {
	action := nativeLocalString(args, "action")
	switch action {
	case "add":
		return b.add(ctx, db, session, args)
	case "search":
		return b.search(ctx, db, nativeLocalString(args, "query"), args, 10)
	case "list", "export", "review", "enrich":
		limit, err := nativeLocalInt(args, "limit", 50, 1000)
		if err != nil {
			return nil, err
		}
		facts, err := b.list(ctx, db, args, limit)
		if err != nil {
			return nil, err
		}
		if action == "review" {
			filtered := make([]holographicFact, 0)
			for _, fact := range facts {
				if fact.Trust < b.minTrust || fact.Helpful == 0 {
					filtered = append(filtered, fact)
				}
			}
			return filtered, nil
		}
		if action == "enrich" {
			filtered := make([]holographicFact, 0)
			for _, fact := range facts {
				if len(fact.Keywords) == 0 {
					filtered = append(filtered, fact)
				}
			}
			return filtered, nil
		}
		return facts, nil
	case "stats":
		return b.stats(ctx, db)
	case "probe", "reason", "related":
		return b.entityRecall(ctx, db, action, args)
	case "contradict":
		return b.contradictions(ctx, db, args)
	case "entities", "alias", "rename", "delete_entity", "split", "rebuild_entities":
		return b.entityTool(ctx, db, action, args)
	case "history":
		id, err := holographicID(args, "fact_id")
		if err != nil {
			return nil, err
		}
		if _, err = b.get(ctx, db, id); err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, "SELECT version_id,operation,snapshot,recorded_at FROM native_fact_versions WHERE fact_id=? ORDER BY version_id DESC LIMIT 100", id)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		versions := make([]map[string]any, 0)
		for rows.Next() {
			var version int64
			var op, snapshot, recorded string
			if err = rows.Scan(&version, &op, &snapshot, &recorded); err != nil {
				return nil, err
			}
			var fact holographicFact
			if err = json.Unmarshal([]byte(snapshot), &fact); err != nil {
				return nil, err
			}
			versions = append(versions, map[string]any{"version_id": version, "operation": op, "fact": fact, "recorded_at": recorded})
		}
		return versions, rows.Err()
	case "update", "change", "correct", "end", "revert", "forget", "remove", "merge":
		id, err := holographicID(args, "fact_id")
		if err != nil {
			return nil, err
		}
		fact, err := b.get(ctx, db, id)
		if err != nil {
			return nil, err
		}
		if action == "forget" || action == "remove" {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			defer func() { _ = tx.Rollback() }()
			if _, err = tx.ExecContext(ctx, "DELETE FROM native_checkpoints"); err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(ctx, "DELETE FROM facts WHERE fact_id=?", id)
			if err != nil {
				return nil, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			return map[string]any{"fact_id": id, "deleted": n == 1}, nil
		}
		if action == "merge" {
			return b.merge(ctx, db, session, fact, args)
		}
		switch action {
		case "revert":
			var snapshot string
			if err = db.QueryRowContext(ctx, "SELECT snapshot FROM native_fact_versions WHERE fact_id=? AND operation!='feedback' ORDER BY version_id DESC LIMIT 1 OFFSET 1", id).Scan(&snapshot); errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("fact has no previous version")
			} else if err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(snapshot), &fact); err != nil {
				return nil, err
			}
			fact.ID = id
		case "end":
			fact.ValidTo, err = holographicDate(args, "valid_to")
			if err != nil {
				return nil, err
			}
			if fact.ValidTo == "" {
				fact.ValidTo = time.Now().UTC().Format("2006-01-02")
			}
			fact.Active = false
		default:
			if content := nativeLocalString(args, "content"); content != "" {
				if err = holographicValidateFact(content); err != nil {
					return nil, err
				}
				fact.Content = content
				fact.Trust = b.trust
				fact.Retrievals = 0
				fact.Helpful = 0
				fact.Active = true
				fact.ValidTo = ""
				if action == "change" {
					fact.Keywords = nil
				}
			}
			if (action == "change" || action == "correct") && nativeLocalString(args, "content") == "" {
				return nil, errors.New("change/correct requires replacement content")
			}
			if _, ok := args["valid_from"]; ok {
				fact.ValidFrom, err = holographicDate(args, "valid_from")
				if err != nil {
					return nil, err
				}
			}
			if origin := nativeLocalString(args, "origin"); origin != "" {
				if !holographicOrigin(origin) {
					return nil, errors.New("invalid origin")
				}
				fact.Origin = origin
			}
			if _, ok := args["entities"]; ok {
				fact.Entities, err = holographicStrings(args, "entities", 100, 100)
				if err != nil {
					return nil, err
				}
			}
			if _, ok := args["keywords"]; ok {
				fact.Keywords, err = holographicStrings(args, "keywords", 12, 40)
				if err != nil {
					return nil, err
				}
			}
			if _, ok := args["tags"]; ok {
				fact.Tags = nativeLocalString(args, "tags")
				if len(fact.Tags) > 1000 {
					return nil, errors.New("tags exceed 1000 bytes")
				}
			}
			if category := nativeLocalString(args, "category"); category != "" {
				if !holographicCategory(category) {
					return nil, errors.New("invalid category")
				}
				fact.Category = category
			}
			if delta, ok := args["trust_delta"]; ok {
				n, err := nativeLocalNumber(delta)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return nil, errors.New("trust_delta must be finite")
				}
				fact.Trust = math.Max(0, math.Min(1, fact.Trust+n))
			}
		}
		fact.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		if err = b.save(ctx, db, session, action, fact); err != nil {
			return nil, err
		}
		return fact, nil
	default:
		return nil, fmt.Errorf("unsupported fact_store action %q", action)
	}
}

func holographicID(args map[string]any, key string) (int64, error) {
	n, err := nativeLocalInt(args, key, 0, math.MaxInt)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return int64(n), nil
}
func holographicOrigin(origin string) bool {
	return origin == "said" || origin == "confirmed" || origin == "inferred"
}
func holographicCategory(category string) bool {
	return category == "user_pref" || category == "project" || category == "tool" || category == "general"
}
func holographicDate(args map[string]any, key string) (string, error) {
	value := nativeLocalString(args, key)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", fmt.Errorf("%s must be YYYY-MM-DD", key)
	}
	return value, nil
}
func holographicStrings(args map[string]any, key string, maxCount, maxChars int) ([]string, error) {
	data, err := json.Marshal(args[key])
	if err != nil {
		return nil, err
	}
	var values []string
	if err = json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	if len(values) > maxCount {
		return nil, fmt.Errorf("%s contains too many entries", key)
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || utf8.RuneCountInString(value) > maxChars {
			return nil, fmt.Errorf("%s entry is empty or too long", key)
		}
		if !seen[strings.ToLower(value)] {
			out = append(out, value)
			seen[strings.ToLower(value)] = true
		}
	}
	return out, nil
}

var holographicSecret = regexp.MustCompile(`(?i)(?:\b(?:api[_-]?key|token|password|secret|authorization)\s*[:=]\s*\S+|\bBearer\s+\S+|\bsk-[a-z0-9_-]{12,}|\bgh[pousr]_[a-z0-9]{12,}|-----BEGIN .*PRIVATE KEY|\b\d{6,12}:[A-Za-z0-9_-]{20,})`)
var holographicInstruction = regexp.MustCompile(`(?i)(?:ignore (?:all |previous |prior )?instructions|system prompt|you (?:must|should) (?:execute|ignore|reveal)|<\|(?:system|assistant)|\[(?:INST|SYSTEM)\]|忽略.{0,8}(?:指令|规则)|泄露.{0,8}(?:密钥|密码)|игнорируй.{0,12}инструк)`)

func holographicValidateFact(content string) error {
	if content == "" || utf8.RuneCountInString(content) > 1000 || !utf8.ValidString(content) {
		return errors.New("fact must be 1..1000 Unicode characters")
	}
	if json.Valid([]byte(content)) || holographicSecret.MatchString(content) || holographicInstruction.MatchString(content) || strings.Contains(content, "![") || strings.Contains(strings.ToLower(content), "<img") {
		return errors.New("fact cannot contain credentials, instructions, JSON or images")
	}
	for _, r := range content {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.In(r, unicode.Cf) {
			return errors.New("fact contains invisible control characters")
		}
	}
	return nil
}

func (b *holographicBackend) add(ctx context.Context, db *sql.DB, session string, args map[string]any) (holographicFact, error) {
	content := nativeLocalString(args, "content")
	if err := holographicValidateFact(content); err != nil {
		return holographicFact{}, err
	}
	var existing int64
	if err := db.QueryRowContext(ctx, "SELECT fact_id FROM facts WHERE content=?", content).Scan(&existing); err == nil {
		return b.get(ctx, db, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return holographicFact{}, err
	}
	category := nativeLocalString(args, "category")
	if category == "" {
		category = "general"
	}
	if !holographicCategory(category) {
		return holographicFact{}, errors.New("invalid fact category")
	}
	entities, err := holographicStrings(args, "entities", 100, 100)
	if err != nil {
		return holographicFact{}, err
	}
	keywords, err := holographicStrings(args, "keywords", 12, 40)
	if err != nil {
		return holographicFact{}, err
	}
	origin := nativeLocalString(args, "origin")
	if origin != "" && !holographicOrigin(origin) {
		return holographicFact{}, errors.New("invalid fact origin")
	}
	from, err := holographicDate(args, "valid_from")
	if err != nil {
		return holographicFact{}, err
	}
	trust := b.trust
	switch origin {
	case "said", "confirmed":
		trust = math.Min(1, trust+0.1)
	case "inferred":
		trust = math.Max(0, trust-0.1)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fact := holographicFact{Content: content, Category: category, Tags: nativeLocalString(args, "tags"), Keywords: keywords, Entities: entities, Trust: trust, Created: now, Updated: now, Origin: origin, ValidFrom: from, Active: true}
	if len(fact.Tags) > 1000 {
		return holographicFact{}, errors.New("tags exceed 1000 bytes")
	}
	// 已知实体通过正文匹配自动关联；显式 entities 则声明新的实体身份。
	known, err := b.knownEntities(ctx, db)
	if err != nil {
		return holographicFact{}, err
	}
	for name := range known {
		if strings.Contains(strings.ToLower(content), strings.ToLower(name)) {
			fact.Entities = append(fact.Entities, name)
		}
	}
	fact.Entities = holographicUnique(fact.Entities)
	if err = b.save(ctx, db, session, "add", fact); err != nil {
		return holographicFact{}, err
	}
	return b.getByContent(ctx, db, content)
}

func (b *holographicBackend) save(ctx context.Context, db *sql.DB, session, operation string, fact holographicFact) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	keywords, err := json.Marshal(fact.Keywords)
	if err != nil {
		return err
	}
	blob := []byte{}
	if fact.ID == 0 {
		result, err := tx.ExecContext(ctx, "INSERT INTO facts(content,category,tags,trust_score,retrieval_count,helpful_count,created_at,updated_at,hrr_vector,keywords,origin,valid_from,valid_to,active) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", fact.Content, fact.Category, fact.Tags, fact.Trust, fact.Retrievals, fact.Helpful, fact.Created, fact.Updated, blob, string(keywords), fact.Origin, fact.ValidFrom, fact.ValidTo, fact.Active)
		if err != nil {
			return err
		}
		fact.ID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, "UPDATE facts SET content=?,category=?,tags=?,trust_score=?,retrieval_count=?,helpful_count=?,updated_at=?,hrr_vector=?,keywords=?,origin=?,valid_from=?,valid_to=?,active=? WHERE fact_id=?", fact.Content, fact.Category, fact.Tags, fact.Trust, fact.Retrievals, fact.Helpful, fact.Updated, blob, string(keywords), fact.Origin, fact.ValidFrom, fact.ValidTo, fact.Active, fact.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM fact_entities WHERE fact_id=?", fact.ID); err != nil {
		return err
	}
	for _, name := range holographicUnique(fact.Entities) {
		key := strings.ToLower(strings.TrimSpace(name))
		var deleted int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM native_deleted_entities WHERE name_key=?", key).Scan(&deleted); err != nil {
			return err
		}
		if deleted > 0 {
			continue
		}
		var entityID int64
		err = tx.QueryRowContext(ctx, "SELECT entity_id FROM entities WHERE name_key=? UNION SELECT entity_id FROM entity_aliases WHERE alias_key=? LIMIT 1", key, key).Scan(&entityID)
		if errors.Is(err, sql.ErrNoRows) {
			result, err := tx.ExecContext(ctx, "INSERT INTO entities(name,name_key) VALUES(?,?)", name, key)
			if err != nil {
				return err
			}
			entityID, err = result.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO fact_entities(fact_id,entity_id) VALUES(?,?)", fact.ID, entityID); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT e.name FROM entities e JOIN fact_entities fe ON fe.entity_id=e.entity_id WHERE fe.fact_id=? ORDER BY e.name", fact.ID)
	if err != nil {
		return err
	}
	fact.Entities = nil
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		fact.Entities = append(fact.Entities, name)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE facts SET hrr_vector=? WHERE fact_id=?", holographicPhasesToBytes(holographicEncodeFact(fact.Content, fact.Entities, b.dim)), fact.ID); err != nil {
		return err
	}
	snapshot, err := json.Marshal(fact)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO native_fact_versions(fact_id,operation,snapshot,session_id,recorded_at) VALUES(?,?,?,?,?)", fact.ID, operation, string(snapshot), session, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

const holographicColumns = "fact_id,content,category,tags,trust_score,retrieval_count,helpful_count,created_at,updated_at,hrr_vector,keywords,origin,valid_from,valid_to,active"

func (b *holographicBackend) scan(rows *sql.Rows) ([]holographicFact, error) {
	facts := make([]holographicFact, 0)
	for rows.Next() {
		var fact holographicFact
		var blob []byte
		var keywords string
		if err := rows.Scan(&fact.ID, &fact.Content, &fact.Category, &fact.Tags, &fact.Trust, &fact.Retrievals, &fact.Helpful, &fact.Created, &fact.Updated, &blob, &keywords, &fact.Origin, &fact.ValidFrom, &fact.ValidTo, &fact.Active); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(keywords), &fact.Keywords); err != nil {
			return nil, err
		}
		var err error
		fact.vector, err = holographicBytesToPhases(blob, b.dim)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}
func (b *holographicBackend) get(ctx context.Context, db *sql.DB, id int64) (holographicFact, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+holographicColumns+" FROM facts WHERE fact_id=?", id)
	if err != nil {
		return holographicFact{}, err
	}
	facts, err := b.scan(rows)
	closeErr := rows.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return holographicFact{}, err
	}
	if len(facts) == 0 {
		return holographicFact{}, errors.New("fact_id was not found in current trusted scope")
	}
	fact := facts[0]
	fact.Entities, err = b.factEntities(ctx, db, id)
	return fact, err
}
func (b *holographicBackend) getByContent(ctx context.Context, db *sql.DB, content string) (holographicFact, error) {
	var id int64
	if err := db.QueryRowContext(ctx, "SELECT fact_id FROM facts WHERE content=?", content).Scan(&id); err != nil {
		return holographicFact{}, err
	}
	return b.get(ctx, db, id)
}
func (b *holographicBackend) factEntities(ctx context.Context, db *sql.DB, id int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT e.name FROM entities e JOIN fact_entities fe ON fe.entity_id=e.entity_id WHERE fe.fact_id=? ORDER BY e.name", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
func (b *holographicBackend) knownEntities(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, "SELECT name,entity_id FROM entities UNION SELECT alias,entity_id FROM entity_aliases LIMIT 10000")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var name string
		var id int64
		if err = rows.Scan(&name, &id); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func (b *holographicBackend) list(ctx context.Context, db *sql.DB, args map[string]any, limit int) ([]holographicFact, error) {
	minimum := 0.0
	if value, ok := args["min_trust"]; ok {
		var err error
		minimum, err = nativeLocalNumber(value)
		if err != nil || minimum < 0 || minimum > 1 {
			return nil, errors.New("min_trust must be 0..1")
		}
	}
	query := "SELECT " + holographicColumns + " FROM facts WHERE active=1 AND trust_score>=?"
	params := []any{minimum}
	if category := nativeLocalString(args, "category"); category != "" {
		query += " AND category=?"
		params = append(params, category)
	}
	query += " ORDER BY trust_score DESC,fact_id DESC LIMIT ?"
	params = append(params, limit)
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	facts, err := b.scan(rows)
	closeErr := rows.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	for index := range facts {
		facts[index].Entities, err = b.factEntities(ctx, db, facts[index].ID)
		if err != nil {
			return nil, err
		}
	}
	if day, err := holographicDate(args, "as_of"); err != nil {
		return nil, err
	} else if day != "" {
		// 读取版本快照而不是把现在的事实冒充历史事实。
		rows, err := db.QueryContext(ctx, "SELECT snapshot FROM native_fact_versions WHERE recorded_at<=? ORDER BY version_id DESC LIMIT 10000", day+"T23:59:59.999999999Z")
		if err != nil {
			return nil, err
		}
		seen := make(map[int64]bool)
		past := make([]holographicFact, 0)
		for rows.Next() {
			var snapshot string
			if err = rows.Scan(&snapshot); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var fact holographicFact
			if err = json.Unmarshal([]byte(snapshot), &fact); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if seen[fact.ID] {
				continue
			}
			seen[fact.ID] = true
			if fact.Active && fact.Trust >= minimum && (fact.ValidFrom == "" || fact.ValidFrom <= day) && (fact.ValidTo == "" || fact.ValidTo > day) {
				fact.vector = holographicEncodeFact(fact.Content, fact.Entities, b.dim)
				past = append(past, fact)
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
		sort.Slice(past, func(i, j int) bool { return past[i].Trust > past[j].Trust })
		if len(past) > limit {
			past = past[:limit]
		}
		return past, nil
	}
	return facts, nil
}

func (b *holographicBackend) search(ctx context.Context, db *sql.DB, query string, args map[string]any, fallback int) ([]holographicFact, error) {
	if len(query) > 10000 {
		return nil, errors.New("search query exceeds 10000 bytes")
	}
	terms := holographicWords(query)
	if len(terms) == 0 {
		return []holographicFact{}, nil
	}
	limit, err := nativeLocalInt(args, "limit", fallback, 100)
	if err != nil {
		return nil, err
	}
	minimum := b.minTrust
	if value, ok := args["min_trust"]; ok {
		minimum, err = nativeLocalNumber(value)
		if err != nil || minimum < 0 || minimum > 1 {
			return nil, errors.New("min_trust must be 0..1")
		}
	}
	var facts []holographicFact
	if nativeLocalString(args, "as_of") != "" {
		facts, err = b.list(ctx, db, args, 1000)
		if err != nil {
			return nil, err
		}
	} else {
		quoted := make([]string, 0, len(terms))
		for _, term := range terms {
			quoted = append(quoted, "\""+strings.ReplaceAll(term, "\"", "\"\"")+"\"")
		}
		sqlQuery := "SELECT " + holographicColumns + " FROM facts WHERE active=1 AND trust_score>=? AND fact_id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ? ORDER BY bm25(facts_fts) LIMIT 300)"
		params := []any{minimum, strings.Join(quoted, " OR ")}
		if category := nativeLocalString(args, "category"); category != "" {
			sqlQuery += " AND category=?"
			params = append(params, category)
		}
		rows, err := db.QueryContext(ctx, sqlQuery, params...)
		if err != nil {
			return nil, err
		}
		facts, err = b.scan(rows)
		err = errors.Join(err, rows.Close())
		if err != nil {
			return nil, err
		}
		for index := range facts {
			facts[index].Entities, err = b.factEntities(ctx, db, facts[index].ID)
			if err != nil {
				return nil, err
			}
		}
		if len(facts) == 0 {
			facts, err = b.list(ctx, db, map[string]any{"min_trust": minimum, "category": nativeLocalString(args, "category")}, 500)
			if err != nil {
				return nil, err
			}
			filtered := facts[:0]
			for _, fact := range facts {
				for _, term := range terms {
					if strings.Contains(strings.ToLower(fact.Content+" "+strings.Join(fact.Keywords, " ")), term) {
						filtered = append(filtered, fact)
						break
					}
				}
			}
			facts = filtered
		}
	}
	vector := holographicBind(holographicEncodeText(query, b.dim), holographicAtom("__hrr_role_content__", b.dim))
	for index := range facts {
		overlap := holographicJaccard(terms, holographicWords(facts[index].Content+" "+strings.Join(facts[index].Keywords, " ")))
		similarity := (holographicSimilarity(vector, facts[index].vector) + 1) / 2
		facts[index].Score = ((1-b.hrrWeight)*overlap + b.hrrWeight*similarity) * facts[index].Trust
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Score == facts[j].Score {
			return facts[i].ID > facts[j].ID
		}
		return facts[i].Score > facts[j].Score
	})
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (b *holographicBackend) entityRecall(ctx context.Context, db *sql.DB, action string, args map[string]any) ([]holographicFact, error) {
	names := []string{nativeLocalString(args, "entity")}
	if action == "reason" {
		var err error
		names, err = holographicStrings(args, "entities", 100, 100)
		if err != nil {
			return nil, err
		}
	}
	if len(names) == 0 || names[0] == "" {
		return nil, errors.New("entity recall requires names")
	}
	limit, err := nativeLocalInt(args, "limit", 10, 100)
	if err != nil {
		return nil, err
	}
	facts, err := b.list(ctx, db, map[string]any{"min_trust": b.minTrust, "category": nativeLocalString(args, "category")}, 1000)
	if err != nil {
		return nil, err
	}
	known, err := b.knownEntities(ctx, db)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]int64, len(known))
	for name, id := range known {
		keys[strings.ToLower(name)] = id
	}
	out := make([]holographicFact, 0)
	if action == "related" {
		atom := holographicAtom(strings.ToLower(names[0]), b.dim)
		roleEntity := holographicAtom("__hrr_role_entity__", b.dim)
		roleContent := holographicAtom("__hrr_role_content__", b.dim)
		floor := 3 / math.Sqrt(float64(b.dim))
		for _, fact := range facts {
			unbound := holographicUnbind(fact.vector, atom)
			sim := math.Max(holographicSimilarity(unbound, roleEntity), holographicSimilarity(unbound, roleContent))
			if sim >= floor {
				fact.Score = (sim + 1) / 2 * fact.Trust
				out = append(out, fact)
			}
		}
		if len(out) == 0 {
			return b.search(ctx, db, names[0], args, limit)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	} else {
		for _, fact := range facts {
			all := true
			for _, name := range names {
				key := strings.ToLower(name)
				id := keys[key]
				found := false
				for _, entity := range fact.Entities {
					if id != 0 && keys[strings.ToLower(entity)] == id {
						found = true
						break
					}
				}
				if id == 0 {
					found = strings.Contains(strings.ToLower(fact.Content), key)
				}
				if !found {
					all = false
					break
				}
			}
			if all {
				out = append(out, fact)
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (b *holographicBackend) contradictions(ctx context.Context, db *sql.DB, args map[string]any) ([]map[string]any, error) {
	limit, err := nativeLocalInt(args, "limit", 10, 100)
	if err != nil {
		return nil, err
	}
	facts, err := b.list(ctx, db, args, 500)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for i, left := range facts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, right := range facts[i+1:] {
			shared := make([]string, 0)
			for _, name := range left.Entities {
				for _, other := range right.Entities {
					if strings.EqualFold(name, other) {
						shared = append(shared, name)
						break
					}
				}
			}
			if len(shared) == 0 {
				continue
			}
			leftWords, rightWords := holographicWords(left.Content), holographicWords(right.Content)
			entityWords := make(map[string]bool)
			for _, name := range shared {
				for _, word := range holographicWords(name) {
					entityWords[word] = true
				}
			}
			remove := func(words []string) []string {
				out := make([]string, 0, len(words))
				for _, word := range words {
					if !entityWords[word] {
						out = append(out, word)
					}
				}
				return out
			}
			leftWords, rightWords = remove(leftWords), remove(rightWords)
			overlap := holographicJaccard(leftWords, rightWords)
			if overlap < 0.3 || overlap >= 1 || holographicSubset(leftWords, rightWords) || holographicSubset(rightWords, leftWords) {
				continue
			}
			out = append(out, map[string]any{"fact_a": left, "fact_b": right, "shared_entities": shared, "content_similarity": overlap, "contradiction_score": overlap})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["contradiction_score"].(float64) > out[j]["contradiction_score"].(float64)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (b *holographicBackend) stats(ctx context.Context, db *sql.DB) (map[string]any, error) {
	var facts, entities, versions int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM facts WHERE active=1").Scan(&facts); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM entities").Scan(&entities); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM native_fact_versions").Scan(&versions); err != nil {
		return nil, err
	}
	return map[string]any{"facts": facts, "entities": entities, "versions": versions, "hrr_dim": b.dim, "storage": "SQLite/FTS5/HRR"}, nil
}

var holographicPreference = regexp.MustCompile(`(?i)^(?:I (?:prefer|like|love|use|always|never|usually)\b|my (?:favorite|favourite|preferred|default)\b|we (?:decided|agreed|chose)\b|the project (?:uses|needs|requires)\b|я (?:предпочитаю|люблю|использую|обычно|всегда|никогда)\b|мы (?:решили|договорились|выбрали)\b|(?:我|本人)?(?:偏好|习惯|習慣|喜欢|喜歡|爱用|愛用)|(?:我们|我們|团队|團隊)(?:决定|決定)|(?:这个|這個|本)?(?:项目|專案)(?:使用|采用|採用))`)

func (b *holographicBackend) extract(ctx context.Context, db *sql.DB, session string, texts []string) (map[string]any, error) {
	ids := make([]int64, 0)
	if b.autoExtract {
		for _, text := range texts {
			sentences := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '.' || r == '。' || r == ';' || r == '；' })
			for _, sentence := range sentences {
				sentence = strings.TrimSpace(sentence)
				if !holographicPreference.MatchString(sentence) || strings.ContainsAny(sentence, "?？") || utf8.RuneCountInString(sentence) > 300 {
					continue
				}
				if strings.HasPrefix(strings.ToLower(sentence), "i like that") || strings.HasPrefix(strings.ToLower(sentence), "i prefer this") {
					continue
				}
				if holographicValidateFact(sentence) != nil {
					continue
				}
				fact, err := b.add(ctx, db, session, map[string]any{"content": sentence, "category": "user_pref", "origin": "said"})
				if err != nil {
					return nil, err
				}
				ids = append(ids, fact.ID)
			}
		}
	}
	return map[string]any{"stored_fact_ids": ids, "auto_extract": b.autoExtract}, nil
}

func (b *holographicBackend) mirror(ctx context.Context, db *sql.DB, session string, args map[string]any) (any, error) {
	id, err := holographicID(args, "entry_id")
	if err != nil {
		return nil, err
	}
	action := nativeLocalString(args, "action")
	var factID int64
	lookup := db.QueryRowContext(ctx, "SELECT fact_id FROM native_mirrors WHERE entry_id=?", id).Scan(&factID)
	if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
		return nil, lookup
	}
	if action == "delete" || action == "forget" {
		if errors.Is(lookup, sql.ErrNoRows) {
			return nil, errors.New("mirrored entry was not found")
		}
		return b.factTool(ctx, db, session, map[string]any{"action": "forget", "fact_id": factID})
	}
	if lookup == nil {
		fact, err := b.get(ctx, db, factID)
		if err != nil {
			return nil, err
		}
		fact.Content = nativeLocalString(args, "content")
		if err = holographicValidateFact(fact.Content); err != nil {
			return nil, err
		}
		if err = b.save(ctx, db, session, "mirror", fact); err != nil {
			return nil, err
		}
		return b.get(ctx, db, factID)
	}
	fact, err := b.add(ctx, db, session, map[string]any{"content": nativeLocalString(args, "content"), "category": "general"})
	if err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO native_mirrors(entry_id,fact_id) VALUES(?,?)", id, fact.ID); err != nil {
		return nil, err
	}
	return fact, nil
}

func (b *holographicBackend) merge(ctx context.Context, db *sql.DB, session string, keep holographicFact, args map[string]any) (any, error) {
	data, err := json.Marshal(args["from"])
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err = json.Unmarshal(data, &ids); err != nil || len(ids) < 1 || len(ids) > 20 {
		return nil, errors.New("merge requires 1..20 source fact IDs")
	}
	seen := map[int64]bool{keep.ID: true}
	sources := make([]holographicFact, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errors.New("merge source IDs must be unique and exclude target")
		}
		seen[id] = true
		fact, err := b.get(ctx, db, id)
		if err != nil {
			return nil, err
		}
		sources = append(sources, fact)
		keep.Entities = append(keep.Entities, fact.Entities...)
		keep.Keywords = append(keep.Keywords, fact.Keywords...)
	}
	keep.Entities = holographicUnique(keep.Entities)
	keep.Keywords = holographicUnique(keep.Keywords)
	// 来源检查全部完成后再事务迁移；任何失败都不会先删掉另一事实。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, source := range sources {
		if _, err = tx.ExecContext(ctx, "UPDATE native_fact_versions SET fact_id=? WHERE fact_id=?", keep.ID, source.ID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO fact_entities(fact_id,entity_id) SELECT ?,entity_id FROM fact_entities WHERE fact_id=?", keep.ID, source.ID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM facts WHERE fact_id=?", source.ID); err != nil {
			return nil, err
		}
	}
	keywords, err := json.Marshal(keep.Keywords)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE facts SET keywords=?,hrr_vector=? WHERE fact_id=?", string(keywords), holographicPhasesToBytes(holographicEncodeFact(keep.Content, keep.Entities, b.dim)), keep.ID); err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(keep)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO native_fact_versions(fact_id,operation,snapshot,session_id,recorded_at) VALUES(?,?,?,?,?)", keep.ID, "merge", string(snapshot), session, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return b.get(ctx, db, keep.ID)
}

func (b *holographicBackend) entityTool(ctx context.Context, db *sql.DB, action string, args map[string]any) (any, error) {
	known, err := b.knownEntities(ctx, db)
	if err != nil {
		return nil, err
	}
	if action == "entities" {
		names := make([]string, 0, len(known))
		for name := range known {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, nil
	}
	if action == "rebuild_entities" {
		facts, err := b.list(ctx, db, map[string]any{}, 10000)
		if err != nil {
			return nil, err
		}
		for _, fact := range facts {
			for name := range known {
				if strings.Contains(strings.ToLower(fact.Content), strings.ToLower(name)) {
					fact.Entities = append(fact.Entities, name)
				}
			}
			fact.Entities = holographicUnique(fact.Entities)
			if err = b.save(ctx, db, "", "rebuild_entities", fact); err != nil {
				return nil, err
			}
		}
		return map[string]any{"rebuilt_facts": len(facts)}, nil
	}
	name := nativeLocalString(args, "entity")
	var entityID int64
	for candidate, id := range known {
		if strings.EqualFold(candidate, name) {
			entityID = id
			break
		}
	}
	if action == "delete_entity" {
		names := []string{name}
		if _, ok := args["entities"]; ok {
			names, err = holographicStrings(args, "entities", 100, 100)
			if err != nil {
				return nil, err
			}
		}
		if len(names) == 0 || names[0] == "" {
			return nil, errors.New("delete_entity requires entity names")
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		for _, name := range names {
			key := strings.ToLower(name)
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO native_deleted_entities(name_key) VALUES(?)", key); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM entities WHERE name_key=? OR entity_id IN (SELECT entity_id FROM entity_aliases WHERE alias_key=?)", key, key); err != nil {
				return nil, err
			}
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"removed_names": len(names)}, nil
	}
	if entityID == 0 {
		return nil, errors.New("entity does not exist in current scope")
	}
	newName := nativeLocalString(args, "content")
	if (action == "alias" || action == "rename") && (newName == "" || utf8.RuneCountInString(newName) > 100 || holographicValidateFact(newName) != nil) {
		return nil, errors.New("entity name is invalid")
	}
	switch action {
	case "alias":
		if _, err = db.ExecContext(ctx, "INSERT INTO entity_aliases(alias_key,entity_id,alias) VALUES(?,?,?)", strings.ToLower(newName), entityID, newName); err != nil {
			return nil, err
		}
		return map[string]any{"entity": name, "alias": newName}, nil
	case "rename":
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO entity_aliases(alias_key,entity_id,alias) VALUES(?,?,?)", strings.ToLower(name), entityID, name); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE entities SET name=?,name_key=? WHERE entity_id=?", newName, strings.ToLower(newName), entityID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"entity": newName}, nil
	case "split":
		var alias string
		if err = db.QueryRowContext(ctx, "SELECT alias FROM entity_aliases WHERE alias_key=?", strings.ToLower(name)).Scan(&alias); err != nil {
			return nil, errors.New("split requires an existing alias")
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(ctx, "DELETE FROM entity_aliases WHERE alias_key=?", strings.ToLower(name)); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO entities(name,name_key) VALUES(?,?)", alias, strings.ToLower(alias))
		if err != nil {
			return nil, err
		}
		newID, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO fact_entities(fact_id,entity_id) SELECT fact_id,? FROM facts WHERE instr(lower(content),?)>0", newID, strings.ToLower(alias)); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"entity": alias, "entity_id": newID}, nil
	default:
		return nil, fmt.Errorf("unsupported entity action %q", action)
	}
}

func holographicWords(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	return holographicUnique(words)
}
func holographicUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}
func holographicJaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	intersection := 0
	for _, value := range b {
		if set[value] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(a)+len(b)-intersection)
}
func holographicSubset(a, b []string) bool {
	set := make(map[string]bool, len(b))
	for _, value := range b {
		set[value] = true
	}
	for _, value := range a {
		if !set[value] {
			return false
		}
	}
	return true
}

func holographicAtom(word string, dim int) []float64 {
	out := make([]float64, dim)
	for block := range (dim + 15) / 16 {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", word, block)))
		for offset := range 16 {
			index := block*16 + offset
			if index >= dim {
				break
			}
			out[index] = float64(binary.LittleEndian.Uint16(digest[offset*2:])) * (2 * math.Pi / 65536)
		}
	}
	return out
}
func holographicBind(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = math.Mod(a[i]+b[i], 2*math.Pi)
	}
	return out
}
func holographicUnbind(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = math.Mod(a[i]-b[i]+2*math.Pi, 2*math.Pi)
	}
	return out
}
func holographicBundle(vectors ...[]float64) []float64 {
	if len(vectors) == 0 {
		return nil
	}
	out := make([]float64, len(vectors[0]))
	for i := range out {
		real, imaginary := 0.0, 0.0
		for _, vector := range vectors {
			sin, cos := math.Sincos(vector[i])
			real += cos
			imaginary += sin
		}
		angle := math.Atan2(imaginary, real)
		if angle < 0 {
			angle += 2 * math.Pi
		}
		out[i] = angle
	}
	return out
}
func holographicEncodeText(text string, dim int) []float64 {
	words := strings.Fields(strings.ToLower(text))
	vectors := make([][]float64, 0, len(words))
	for _, word := range words {
		word = strings.Trim(word, ".,!?;:\"'()[]{}")
		if word != "" {
			vectors = append(vectors, holographicAtom(word, dim))
		}
	}
	if len(vectors) == 0 {
		return holographicAtom("__hrr_empty__", dim)
	}
	return holographicBundle(vectors...)
}
func holographicEncodeFact(content string, entities []string, dim int) []float64 {
	vectors := make([][]float64, 0, len(entities)+1)
	vectors = append(vectors, holographicBind(holographicEncodeText(content, dim), holographicAtom("__hrr_role_content__", dim)))
	role := holographicAtom("__hrr_role_entity__", dim)
	for _, entity := range entities {
		vectors = append(vectors, holographicBind(holographicAtom(strings.ToLower(entity), dim), role))
	}
	return holographicBundle(vectors...)
}
func holographicSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	sum := 0.0
	for i := range a {
		sum += math.Cos(a[i] - b[i])
	}
	return sum / float64(len(a))
}
func holographicPhasesToBytes(phases []float64) []byte {
	if len(phases) == 1 {
		out := make([]byte, 8)
		binary.LittleEndian.PutUint64(out, math.Float64bits(phases[0]))
		return out
	}
	out := make([]byte, 4+4*len(phases))
	copy(out, "HRR1")
	for i, phase := range phases {
		binary.LittleEndian.PutUint32(out[4+i*4:], math.Float32bits(float32(phase)))
	}
	return out
}
func holographicBytesToPhases(data []byte, dim int) ([]float64, error) {
	out := make([]float64, dim)
	if len(data) == dim*8 {
		for i := range out {
			out[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
		}
		return out, nil
	}
	if len(data) != 4+dim*4 || string(data[:4]) != "HRR1" {
		return nil, errors.New("invalid HRR vector blob")
	}
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[4+i*4:])))
	}
	return out, nil
}

func holographicTools() []map[string]any {
	properties := map[string]any{"action": map[string]any{"type": "string", "enum": []string{"add", "search", "probe", "related", "reason", "contradict", "update", "change", "end", "correct", "revert", "forget", "merge", "history", "remove", "list", "stats", "export", "review", "entities", "alias", "rename", "delete_entity", "split", "rebuild_entities", "enrich"}}, "content": nativeLocalField("string", "One standalone fact, or new entity name"), "query": nativeLocalField("string", "Search query"), "entity": nativeLocalField("string", "Entity name or alias"), "entities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "keywords": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "fact_id": nativeLocalField("integer", "Fact ID from the current scope"), "from": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "category": nativeLocalField("string", "user_pref, project, tool or general"), "tags": nativeLocalField("string", "Comma separated tags"), "origin": nativeLocalField("string", "said, confirmed or inferred"), "valid_from": nativeLocalField("string", "Known effective date YYYY-MM-DD"), "valid_to": nativeLocalField("string", "Known end date YYYY-MM-DD"), "as_of": nativeLocalField("string", "Historical date YYYY-MM-DD"), "min_trust": nativeLocalField("number", "Minimum trust 0..1"), "trust_delta": nativeLocalField("number", "Finite trust adjustment"), "limit": nativeLocalField("integer", "Maximum returned facts")}
	return []map[string]any{nativeLocalTool("fact_store", "SQLite facts, FTS5 search, HRR algebra, history and entities within current trusted scope", properties, "action"), nativeLocalTool("fact_feedback", "Asymmetric trust feedback: helpful +0.05, unhelpful -0.10", map[string]any{"fact_id": properties["fact_id"], "action": map[string]any{"type": "string", "enum": []string{"helpful", "unhelpful", "confirmed"}}}, "fact_id", "action")}
}
