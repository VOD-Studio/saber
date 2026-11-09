// Package vault 保存按可信用户和精确站点绑定的秘密，仅将其交给受监督浏览器，不交给模型。
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// Metadata 只包含可展示的项目资料；密码、卡号和验证码不会序列化到工具结果。
type Metadata struct {
	Handle     string   `json:"handle"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Origins    []string `json:"origins"`
	Identifier string   `json:"identifier,omitempty"`
	Source     string   `json:"source"`
	TOTP       bool     `json:"totp"`
}

// Item 是仅供可信 CLI/API 输入的秘密记录，不允许从模型工具参数保存。
type Item struct {
	Metadata
	Fields map[string]string `json:"fields"`
	Owner  string            `json:"-"`
}
type diskItem struct {
	Metadata
	Fields map[string]string `json:"fields"`
	Owner  string            `json:"owner"`
}
type diskState struct {
	Version int                 `json:"version"`
	Items   map[string]diskItem `json:"items"`
	Sources map[string]bool     `json:"sources"`
}

// Browser 是不返回字段值、并在页面写入前重新检查站点的浏览器端口。
type Browser interface {
	Origin(context.Context, string) (string, error)
	Fill(context.Context, string, string, map[string]string) error
}

// Store 持有加密记录、短期密码管理器会话及注入秘密的防泄漏词典。
type Store struct {
	cfg       config.VaultConfig
	host      toolkit.Host
	dir       string
	mu        *sync.Mutex
	browser   Browser
	approval  func(context.Context, Metadata) (bool, error)
	secretMu  sync.RWMutex
	secrets   map[string]struct{}
	managerMu sync.Mutex
	sessions  map[string]managerSession
}
type managerSession struct {
	Token   string
	Touched time.Time
}

var pathLocks sync.Map

// New 打开独立私有目录，已有 ciphertext 的密钥丢失时拒绝创建替代密钥。
func New(cfg config.VaultConfig, host toolkit.Host) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dir := cfg.Directory
	if dir == "" {
		dir = filepath.Join(host.StateDir, "vault")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(host.StateDir, dir)
	}
	if host.StateDir == "" && cfg.Directory == "" {
		return nil, errors.New("vault requires private state directory")
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lock, _ := pathLocks.LoadOrStore(dir, &sync.Mutex{})
	s := &Store{cfg: cfg, host: host, dir: dir, mu: lock.(*sync.Mutex), secrets: map[string]struct{}{}, sessions: map[string]managerSession{}}
	if cfg.Enabled {
		if err := s.transaction(context.Background(), false, func(*diskState) error { return nil }); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("vault directory must not be symlink")
	}
	return os.Chmod(path, 0700)
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".vault-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func privateRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("vault files must be private regular files")
	}
	return os.ReadFile(path)
}
func (s *Store) transaction(ctx context.Context, write bool, fn func(*diskState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(filepath.Join(s.dir, "vault.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.key(ctx)
	if err != nil {
		return err
	}
	defer clear(key)
	state := diskState{Version: 1, Items: map[string]diskItem{}, Sources: map[string]bool{}}
	data, err := privateRead(filepath.Join(s.dir, "vault.json.enc"))
	if err == nil {
		plain, e := decrypt(key, data)
		if e != nil {
			return errors.New("vault authentication failed; key or encrypted file does not match")
		}
		defer clear(plain)
		if e = json.Unmarshal(plain, &state); e != nil || state.Version != 1 || state.Items == nil {
			return errors.New("vault data format invalid")
		}
		if state.Sources == nil {
			state.Sources = map[string]bool{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = fn(&state); err != nil {
		return err
	}
	if !write {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plain, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(plain)
	data, err = encrypt(key, plain)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, "vault.json.enc"), data)
}
func encrypt(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(append([]byte("SV1"), nonce...), nonce, plain, []byte("saber-vault-v1")), nil
}
func decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < 3+g.NonceSize() || string(data[:3]) != "SV1" {
		return nil, errors.New("invalid vault envelope")
	}
	return g.Open(nil, data[3:3+g.NonceSize()], data[3+g.NonceSize():], []byte("saber-vault-v1"))
}
func (s *Store) key(ctx context.Context) ([]byte, error) {
	path := filepath.Join(s.dir, "vault.key")
	key, err := privateRead(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("vault key length invalid")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sum := sha256.Sum256([]byte(s.dir))
	expectedRef := "saber-vault-" + hex.EncodeToString(sum[:16])
	reference, err := privateRead(filepath.Join(s.dir, "vault.keyref"))
	if err == nil {
		key, err = keychainRead(ctx, string(reference))
		if err != nil {
			return nil, err
		}
		if string(reference) != expectedRef {
			if err := keychainWrite(ctx, expectedRef, key); err != nil {
				clear(key)
				return nil, err
			}
			if err := atomicWrite(filepath.Join(s.dir, "vault.keyref"), []byte(expectedRef)); err != nil {
				clear(key)
				return nil, err
			}
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(s.dir, "vault.json.enc")); err == nil {
		return nil, errors.New("vault key is missing; restore the original key instead of creating a new one")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if s.cfg.KeyStorage == "keychain" {
		if err := keychainWrite(ctx, expectedRef, key); err != nil {
			clear(key)
			return nil, err
		}
		if err := atomicWrite(filepath.Join(s.dir, "vault.keyref"), []byte(expectedRef)); err != nil {
			clear(key)
			return nil, err
		}
	} else if err := atomicWrite(path, key); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}
func owner(ctx context.Context) (string, error) {
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("vault requires trusted identity")
	}
	if !i.Direct {
		return "", errors.New("vault is unavailable in group/public conversations")
	}
	return i.UserKey(), nil
}

// NormalizeOrigin 绑定精确协议、主机和端口，不推测父域、子域或重定向关系。
func NormalizeOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return "", errors.New("invalid vault origin")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1")) {
		return "", errors.New("vault requires HTTPS or loopback HTTP")
	}
	port := u.Port()
	if port == "443" && u.Scheme == "https" || port == "80" && u.Scheme == "http" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}
func newHandle() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Put 仅用于可信交互入口；所有字段先验证，项目按当前用户保存。
func (s *Store) Put(ctx context.Context, item Item) (Metadata, error) {
	who, err := owner(ctx)
	if err != nil {
		return Metadata{}, err
	}
	if !s.cfg.Enabled {
		return Metadata{}, errors.New("vault disabled")
	}
	required := map[string][]string{"login": {"password"}, "payment": {"card_number", "exp_month", "exp_year", "cvc"}, "address": {"address_line1", "city", "postal_code", "country"}}
	allowed := map[string][]string{"login": {"password", "totp_secret"}, "payment": {"card_number", "card_name", "exp_month", "exp_year", "cvc"}, "address": {"full_name", "first_name", "last_name", "address_line1", "address_line2", "city", "state", "postal_code", "country", "email", "phone", "company"}}
	if _, ok := required[item.Kind]; !ok {
		return Metadata{}, errors.New("vault kind must be login, payment or address")
	}
	if len(item.Origins) < 1 || len(item.Origins) > 32 {
		return Metadata{}, errors.New("vault needs 1..32 exact origins")
	}
	for i, value := range item.Origins {
		item.Origins[i], err = NormalizeOrigin(value)
		if err != nil {
			return Metadata{}, err
		}
	}
	if item.Fields == nil {
		return Metadata{}, errors.New("vault fields required")
	}
	for key, value := range item.Fields {
		if !contains(allowed[item.Kind], key) || len(value) > 64<<10 {
			return Metadata{}, errors.New("vault field name or size invalid for item kind")
		}
	}
	for _, key := range required[item.Kind] {
		if item.Fields[key] == "" {
			return Metadata{}, fmt.Errorf("vault field %s required", key)
		}
	}
	if item.Kind == "login" && item.Identifier == "" {
		return Metadata{}, errors.New("login identifier required")
	}
	item.TOTP = false
	if seed := item.Fields["totp_secret"]; seed != "" {
		if _, err := TOTP(seed, time.Now()); err != nil {
			return Metadata{}, err
		}
		item.TOTP = true
	}
	if item.Handle == "" {
		item.Handle = newHandle()
	} else {
		if len(item.Handle) != 32 {
			return Metadata{}, errors.New("local vault handle must be 32 hexadecimal characters")
		}
		if _, err := hex.DecodeString(item.Handle); err != nil {
			return Metadata{}, errors.New("local vault handle invalid")
		}
	}
	item.Source = "local"
	err = s.transaction(ctx, true, func(state *diskState) error {
		if old, ok := state.Items[item.Handle]; ok && old.Owner != who {
			return errors.New("vault item not found")
		}
		state.Items[item.Handle] = diskItem{item.Metadata, item.Fields, who}
		return nil
	})
	return item.Metadata, err
}

// List 只返回当前用户在指定站点可用的元数据，外部管理器也不会返回密码。
func (s *Store) List(ctx context.Context, origin string) ([]Metadata, error) {
	who, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if !s.cfg.Enabled {
		return nil, errors.New("vault disabled")
	}
	if origin != "" {
		origin, err = NormalizeOrigin(origin)
		if err != nil {
			return nil, err
		}
	}
	out := []Metadata{}
	disabled := map[string]bool{}
	err = s.transaction(ctx, false, func(state *diskState) error {
		for _, item := range state.Items {
			if item.Kind != "internal" && item.Owner == who && (origin == "" || contains(item.Origins, origin)) {
				out = append(out, item.Metadata)
			}
		}
		for k, v := range state.Sources {
			disabled[k] = !v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"onepassword", "bitwarden"} {
		if s.managerAllowed(name, who) && !disabled[who+":"+name] {
			items, err := s.managerItems(ctx, name, who, false, "")
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if origin == "" || contains(item.Origins, origin) {
					out = append(out, item.Metadata)
				}
			}
		}
	}
	return out, nil
}
func contains(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}

// Remove 不暴露项目存在于另一个用户的事实，删除仅作用于本地记录。
func (s *Store) Remove(ctx context.Context, handle string) error {
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	return s.transaction(ctx, true, func(state *diskState) error {
		item, ok := state.Items[handle]
		if !ok || item.Owner != who {
			return errors.New("vault item not found")
		}
		delete(state.Items, handle)
		return nil
	})
}

// AttachBrowser 接入监督浏览器，vault 不自行打开其他页面作为失败回退。
func (s *Store) AttachBrowser(browser Browser) { s.browser = browser }

// SetApproval 接入可信人类付款确认；没有交互批准通道时付款填充失败关闭。
func (s *Store) SetApproval(fn func(context.Context, Metadata) (bool, error)) { s.approval = fn }
func (s *Store) remember(fields map[string]string) {
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	for _, v := range fields {
		if len(v) >= 3 {
			s.secrets[v] = struct{}{}
		}
	}
}

// Redact 屏蔽已注入的密码、卡值、验证码及其常见转义/编码形式，
// 避免页面用 btoa/hex/URL 编码回读后绕过明文替换。
func (s *Store) Redact(text string) string {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	for value := range s.secrets {
		for _, form := range secretForms(value) {
			text = strings.ReplaceAll(text, form, "[REDACTED]")
		}
	}
	return text
}

// secretForms 返回一个秘密在页面代码里常见的可回读形式：原文、JSON 转义、
// URL 编码，以及 base64（std/url）与 hex（小写/大写）编码。
func secretForms(value string) []string {
	raw := []byte(value)
	forms := []string{value, url.QueryEscape(value)}
	if b, err := json.Marshal(value); err == nil && len(b) > 2 {
		forms = append(forms, string(b[1:len(b)-1]))
	}
	lower := hex.EncodeToString(raw)
	forms = append(forms,
		base64.StdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		lower,
		strings.ToUpper(lower),
	)
	out := make([]string, 0, len(forms))
	seen := make(map[string]struct{}, len(forms))
	for _, form := range forms {
		if len(form) < 3 {
			continue
		}
		if _, ok := seen[form]; ok {
			continue
		}
		seen[form] = struct{}{}
		out = append(out, form)
	}
	return out
}
func (s *Store) resolve(ctx context.Context, handle, who string) (diskItem, error) {
	var item diskItem
	err := s.transaction(ctx, false, func(state *diskState) error {
		v, ok := state.Items[handle]
		if ok && v.Owner == who && v.Kind != "internal" {
			item = v
			return nil
		}
		parts := strings.SplitN(handle, ":", 2)
		if len(parts) == 2 && (parts[0] == "onepassword" || parts[0] == "bitwarden") {
			if enabled, configured := state.Sources[who+":"+parts[0]]; configured && !enabled {
				return errors.New("password manager source disabled")
			}
			return nil
		}
		return errors.New("vault item not found")
	})
	if err != nil {
		return item, err
	}
	if item.Handle != "" {
		return item, nil
	}
	parts := strings.SplitN(handle, ":", 2)
	items, err := s.managerItems(ctx, parts[0], who, true, parts[1])
	if err != nil {
		return item, err
	}
	for _, v := range items {
		if v.Handle == handle {
			return v, nil
		}
	}
	return item, errors.New("vault manager item not found")
}

// Fill 在读取秘密后仍验证可信用户、站点和付款批准，并只向回调传递字段。
func (s *Store) Fill(ctx context.Context, handle, origin string, write func(map[string]string) error) (Metadata, error) {
	return s.fill(ctx, handle, origin, false, write)
}
func (s *Store) fill(ctx context.Context, handle, origin string, otp bool, write func(map[string]string) error) (Metadata, error) {
	who, err := owner(ctx)
	if err != nil {
		return Metadata{}, err
	}
	if !s.cfg.Enabled {
		return Metadata{}, errors.New("vault disabled")
	}
	origin, err = NormalizeOrigin(origin)
	if err != nil {
		return Metadata{}, err
	}
	if write == nil {
		return Metadata{}, errors.New("vault fill channel unavailable")
	}
	item, err := s.resolve(ctx, handle, who)
	if err != nil {
		return Metadata{}, err
	}
	if !contains(item.Origins, origin) {
		return Metadata{}, errors.New("vault origin mismatch")
	}
	if item.Kind == "payment" {
		if s.approval == nil {
			return Metadata{}, errors.New("payment confirmation unavailable in this session")
		}
		allowed, err := s.approval(ctx, item.Metadata)
		if err != nil {
			return Metadata{}, err
		}
		if !allowed {
			return Metadata{}, errors.New("payment fill declined")
		}
	}
	fields := map[string]string{}
	if otp {
		seed := item.Fields["totp_secret"]
		if seed == "" {
			return Metadata{}, errors.New("saved authenticator key not available; use trusted masked code entry")
		}
		code, err := TOTP(seed, time.Now())
		if err != nil {
			return Metadata{}, err
		}
		fields["one_time_code"] = code
	} else {
		for k, v := range item.Fields {
			if k != "totp_secret" {
				fields[k] = v
			}
		}
		if item.Kind == "login" {
			fields["username"] = item.Identifier
		}
	}
	s.remember(fields)
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	if err := write(fields); err != nil {
		return Metadata{}, errors.New(s.Redact(err.Error()))
	}
	return item.Metadata, nil
}

// TOTP 实现 RFC 6238，可接受 base32 seed 或 otpauth URI；错误不包含密钥。
func TOTP(value string, at time.Time) (string, error) {
	digits, period, algo := 6, 30, "SHA1"
	seed := strings.TrimSpace(value)
	if strings.HasPrefix(seed, "otpauth://") {
		u, err := url.Parse(seed)
		if err != nil || u.Host != "totp" {
			return "", errors.New("invalid authenticator URI")
		}
		q := u.Query()
		seed = q.Get("secret")
		if q.Get("digits") != "" {
			digits, _ = strconv.Atoi(q.Get("digits"))
		}
		if q.Get("period") != "" {
			period, _ = strconv.Atoi(q.Get("period"))
		}
		if q.Get("algorithm") != "" {
			algo = strings.ToUpper(q.Get("algorithm"))
		}
	}
	if (digits != 6 && digits != 8) || period < 1 || period > 300 {
		return "", errors.New("invalid authenticator parameters")
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(strings.ToUpper(strings.ReplaceAll(seed, " ", "")), "="))
	if err != nil || len(key) < 10 {
		return "", errors.New("invalid authenticator seed")
	}
	defer clear(key)
	var makeHash func() hash.Hash
	switch algo {
	case "SHA1":
		makeHash = sha1.New
	case "SHA256":
		makeHash = sha256.New
	case "SHA512":
		makeHash = sha512.New
	default:
		return "", errors.New("unsupported authenticator algorithm")
	}
	counter := make([]byte, 8)
	binary.BigEndian.PutUint64(counter, uint64(at.Unix()/int64(period)))
	h := hmac.New(makeHash, key)
	_, _ = h.Write(counter)
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1000000)
	if digits == 8 {
		mod = 100000000
	}
	return fmt.Sprintf("%0*d", digits, code%mod), nil
}

// Definitions 不包含保存或读取秘密的模型工具。
func (s *Store) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	makeTool := func(name, description string, props map[string]any) toolkit.Definition {
		return toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": props, "additionalProperties": false}}}}
	}
	list := makeTool("vault_list", "List this trusted private user's metadata for an exact origin. Secret fields are never returned.", map[string]any{"origin": map[string]any{"type": "string"}})
	fill := makeTool("browser_vault", "Fill a saved item or authenticator code in the currently supervised page. Missing codes require a trusted masked UI; never send secrets in tool arguments. Filling does not submit forms.", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"fill", "enter_code"}}, "handle": map[string]any{"type": "string"}, "session": map[string]any{"type": "string"}})
	fill.Permission = "browser_vault"
	fill.Requires = []string{"browser_exec", "exec"}
	fill.External = true
	return []toolkit.Definition{list, fill}
}

// Execute 从实际页面读取 origin，而不是信任模型声明的地址。
func (s *Store) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if !agent.ToolAllowed(ctx, name) {
		return nil, errors.New("vault tool outside child task scope")
	}
	if name == "vault_list" {
		origin, _ := args["origin"].(string)
		return s.List(ctx, origin)
	}
	if name != "browser_vault" {
		return nil, errors.New("unknown vault tool")
	}
	if s.browser == nil {
		return nil, errors.New("supervised browser unavailable")
	}
	if s.host.Check == nil {
		return nil, errors.New("browser authorization unavailable")
	}
	if err := s.host.Check(ctx, "browser_vault", true); err != nil {
		return nil, err
	}
	if err := s.host.Check(ctx, "browser_exec", true); err != nil {
		return nil, err
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return nil, err
	}
	session, _ := args["session"].(string)
	handle, _ := args["handle"].(string)
	action, _ := args["action"].(string)
	if action != "fill" && action != "enter_code" {
		return nil, errors.New("browser_vault action must be fill or enter_code")
	}
	origin, err := s.browser.Origin(ctx, session)
	if err != nil {
		return nil, err
	}
	count := 0
	meta, err := s.fill(ctx, handle, origin, action == "enter_code", func(fields map[string]string) error {
		count = len(fields)
		return s.browser.Fill(ctx, session, origin, fields)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"filled_fields": count, "origin": origin, "handle": meta.Handle}, nil
}

// Close 清除内存中的管理器 token 和秘密词典；加密文件不受影响。
func (s *Store) Close() error {
	s.managerMu.Lock()
	clear(s.sessions)
	s.managerMu.Unlock()
	s.secretMu.Lock()
	clear(s.secrets)
	s.secretMu.Unlock()
	return nil
}

// Command 仅在可信私聊或 CLI 身份执行；add 从 stdin 接受 JSON，秘密不进入参数或模型。
func (s *Store) Command(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if _, err := owner(ctx); err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	var result any
	switch args[0] {
	case "list":
		origin := ""
		if len(args) > 1 {
			origin = args[1]
		}
		value, err := s.List(ctx, origin)
		if err != nil {
			return err
		}
		result = value
	case "add":
		var item Item
		decoder := json.NewDecoder(io.LimitReader(in, (1<<20)+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return errors.New("invalid vault item JSON on stdin")
		}
		value, err := s.Put(ctx, item)
		if err != nil {
			return err
		}
		result = value
	case "rm":
		if len(args) != 2 {
			return errors.New("vault rm needs handle")
		}
		if err := s.Remove(ctx, args[1]); err != nil {
			return err
		}
		result = map[string]bool{"removed": true}
	case "key":
		if len(args) == 4 && args[1] == "migrate" && args[2] == "--to" {
			if err := s.MigrateKey(ctx, args[3]); err != nil {
				return err
			}
		}
		storage := "file"
		if _, err := os.Stat(filepath.Join(s.dir, "vault.keyref")); err == nil {
			storage = "keychain"
		}
		result = map[string]string{"storage": storage}
	case "sources":
		who, _ := owner(ctx)
		if len(args) == 3 && (args[1] == "--enable" || args[1] == "--disable") {
			if args[2] != "onepassword" && args[2] != "bitwarden" {
				return errors.New("unknown password manager")
			}
			if !s.managerAllowed(args[2], who) {
				return errors.New("password manager is not authorized by configuration for this user")
			}
			if err := s.transaction(ctx, true, func(state *diskState) error { state.Sources[who+":"+args[2]] = args[1] == "--enable"; return nil }); err != nil {
				return err
			}
		}
		value, err := s.Sources(ctx)
		if err != nil {
			return err
		}
		result = value
	case "unlock":
		if len(args) != 2 {
			return errors.New("vault unlock needs source")
		}
		secret, err := io.ReadAll(io.LimitReader(in, (64<<10)+1))
		if err != nil {
			return err
		}
		defer clear(secret)
		if len(secret) > 64<<10 {
			return errors.New("master password input too large")
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(secret), "\n"), "\r")
		if err := s.Unlock(ctx, args[1], password); err != nil {
			return err
		}
		result = map[string]bool{"unlocked": true}
	case "lock":
		who, _ := owner(ctx)
		s.managerMu.Lock()
		delete(s.sessions, who+":onepassword")
		delete(s.sessions, who+":bitwarden")
		s.managerMu.Unlock()
		result = map[string]bool{"locked": true}
	default:
		return errors.New("vault commands: list/add/rm/key/sources/unlock/lock")
	}
	return json.NewEncoder(out).Encode(result)
}

// MigrateKey 验证目标密钥能解密原文件后才切换引用，重复执行可恢复中断迁移。
func (s *Store) MigrateKey(ctx context.Context, to string) error {
	if to != "file" && to != "keychain" {
		return errors.New("key target must be file or keychain")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(filepath.Join(s.dir, "vault.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	key, err := s.key(ctx)
	if err != nil {
		return err
	}
	defer clear(key)
	if to == "file" {
		if err := atomicWrite(filepath.Join(s.dir, "vault.key"), key); err != nil {
			return err
		}
		err := os.Remove(filepath.Join(s.dir, "vault.keyref"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	sum := sha256.Sum256([]byte(s.dir))
	ref := "saber-vault-" + hex.EncodeToString(sum[:16])
	if err := keychainWrite(ctx, ref, key); err != nil {
		return err
	}
	check, err := keychainRead(ctx, ref)
	if err != nil {
		return err
	}
	defer clear(check)
	if !hmac.Equal(key, check) {
		return errors.New("keychain write verification failed")
	}
	if data, err := privateRead(filepath.Join(s.dir, "vault.json.enc")); err == nil {
		plain, err := decrypt(check, data)
		if err != nil {
			return err
		}
		clear(plain)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := atomicWrite(filepath.Join(s.dir, "vault.keyref"), []byte(ref)); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(s.dir, "vault.key"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// EncodeSecret 将宿主连接凭据作为内部二进制记录保存，不进入模型工具。
func (s *Store) EncodeSecret(ctx context.Context, key string, value []byte) error {
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	return s.transaction(ctx, true, func(state *diskState) error {
		handle := "internal:" + who + ":" + key
		state.Items[handle] = diskItem{Metadata: Metadata{Handle: handle, Kind: "internal", Source: "local"}, Owner: who, Fields: map[string]string{"data": base64.StdEncoding.EncodeToString(value)}}
		return nil
	})
}

// DecodeSecret 仅供宿主连接器读取当前用户的内部凭据，调用者不得将返回值交给模型。
func (s *Store) DecodeSecret(ctx context.Context, key string) ([]byte, error) {
	who, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	var result []byte
	err = s.transaction(ctx, false, func(state *diskState) error {
		v, ok := state.Items["internal:"+who+":"+key]
		if !ok {
			return os.ErrNotExist
		}
		b, err := base64.StdEncoding.DecodeString(v.Fields["data"])
		result = b
		return err
	})
	return result, err
}

// DeleteSecret 撤销连接时移除当前用户的宿主凭据。
func (s *Store) DeleteSecret(ctx context.Context, key string) error {
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	return s.transaction(ctx, true, func(state *diskState) error { delete(state.Items, "internal:"+who+":"+key); return nil })
}
