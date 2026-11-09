package vault

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func vaultContext(user string) context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "account", Conversation: "dm"}, SenderID: user, Direct: true})
}
func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(config.DefaultVaultConfig(), toolkit.Host{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
func loginItem() Item {
	return Item{Metadata: Metadata{Kind: "login", Label: "example login", Origins: []string{"https://example.com/login"}, Identifier: "alice"}, Fields: map[string]string{"password": "not-in-model-context"}}
}

func TestVaultEncryptedPersistenceAndOwnerIsolation(t *testing.T) {
	store := testStore(t)
	ctx := vaultContext("alice")
	meta, err := store.Put(ctx, loginItem())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, "vault.json.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "not-in-model-context") || strings.Contains(string(raw), "example login") {
		t.Fatal("vault ciphertext exposes plaintext")
	}
	info, err := os.Stat(filepath.Join(store.dir, "vault.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions: %v %v", info, err)
	}
	reopened, err := New(config.DefaultVaultConfig(), toolkit.Host{StateDir: filepath.Dir(store.dir)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.List(ctx, "https://example.com")
	if err != nil || len(got) != 1 || got[0].Handle != meta.Handle {
		t.Fatalf("reopened list: %v %v", got, err)
	}
	other, err := reopened.List(vaultContext("bob"), "")
	if err != nil || len(other) != 0 {
		t.Fatalf("other user saw vault items: %v %v", other, err)
	}
	wrote := false
	if _, err := reopened.Fill(vaultContext("bob"), meta.Handle, "https://example.com", func(map[string]string) error { wrote = true; return nil }); err == nil || wrote {
		t.Fatal("other user could fill secret")
	}
	if err := store.EncodeSecret(ctx, "connector:test", []byte(`{"access_token":"token"}`)); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("internal connector credentials leaked into metadata: %v %v", list, err)
	}
}

func TestVaultExactOriginCancellationAndRedaction(t *testing.T) {
	store := testStore(t)
	ctx := vaultContext("alice")
	meta, err := store.Put(ctx, loginItem())
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://evil.example.com", "https://example.com:444", "http://example.com"} {
		wrote := false
		_, err := store.Fill(ctx, meta.Handle, origin, func(map[string]string) error { wrote = true; return nil })
		if err == nil || wrote {
			t.Fatalf("filled wrong origin %s", origin)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Fill(canceled, meta.Handle, "https://example.com", func(map[string]string) error { t.Fatal("canceled fill ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	_, err = store.Fill(ctx, meta.Handle, "https://example.com", func(fields map[string]string) error {
		if fields["password"] != "not-in-model-context" {
			t.Fatal("real field not supplied")
		}
		return errors.New("browser echoed " + fields["password"])
	})
	if err == nil || strings.Contains(err.Error(), "not-in-model-context") {
		t.Fatalf("secret escaped through error: %v", err)
	}
	if strings.Contains(store.Redact("page value not-in-model-context"), "not-in-model-context") {
		t.Fatal("page redactor failed")
	}
}

func TestVaultRedactEncodedSecretForms(t *testing.T) {
	store := testStore(t)
	secret := `Pa55 w0rd/+"<&`
	store.remember(map[string]string{"password": secret})
	raw := []byte(secret)
	lower := hex.EncodeToString(raw)
	forms := map[string]string{
		"plain":      secret,
		"query":      url.QueryEscape(secret),
		"base64 std": base64.StdEncoding.EncodeToString(raw),
		"base64 url": base64.URLEncoding.EncodeToString(raw),
		"hex lower":  lower,
		"hex upper":  strings.ToUpper(lower),
	}
	for name, form := range forms {
		if got := store.Redact("page echo " + form); got != "page echo [REDACTED]" {
			t.Fatalf("%s form survived redaction: %q", name, got)
		}
	}
	escaped, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Redact("json " + string(escaped)); got != "json \"[REDACTED]\"" {
		t.Fatalf("JSON-escaped form survived redaction: %q", got)
	}
}

func TestVaultPaymentRequiresActualApproval(t *testing.T) {
	store := testStore(t)
	ctx := vaultContext("alice")
	item := Item{Metadata: Metadata{Kind: "payment", Label: "card", Origins: []string{"https://shop.example"}}, Fields: map[string]string{"card_number": "4111111111111111", "exp_month": "12", "exp_year": "2030", "cvc": "123"}}
	meta, err := store.Put(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	write := func(map[string]string) error { writes++; return nil }
	if _, err := store.Fill(ctx, meta.Handle, "https://shop.example", write); err == nil {
		t.Fatal("headless payment was approved")
	}
	store.SetApproval(func(context.Context, Metadata) (bool, error) { return false, nil })
	if _, err := store.Fill(ctx, meta.Handle, "https://shop.example", write); err == nil || writes != 0 {
		t.Fatal("declined payment wrote fields")
	}
	store.SetApproval(func(context.Context, Metadata) (bool, error) { return true, nil })
	if _, err := store.Fill(ctx, meta.Handle, "https://shop.example", write); err != nil || writes != 1 {
		t.Fatalf("approved payment did not fill: %d %v", writes, err)
	}
}

func TestVaultTamperAndLostKeyFailClosed(t *testing.T) {
	store := testStore(t)
	ctx := vaultContext("alice")
	if _, err := store.Put(ctx, loginItem()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, "vault.json.enc")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx, ""); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if err := os.Remove(filepath.Join(store.dir, "vault.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config.DefaultVaultConfig(), toolkit.Host{StateDir: filepath.Dir(store.dir)}); err == nil {
		t.Fatal("lost key silently replaced")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "vault.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new key created over old ciphertext")
	}
}

func TestTOTPRFC6238AndMetadataNeverContainsSecrets(t *testing.T) {
	code, err := TOTP("otpauth://totp/test?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&digits=8", time.Unix(59, 0))
	if err != nil || code != "94287082" {
		t.Fatalf("RFC vector: %s %v", code, err)
	}
	store := testStore(t)
	meta, err := store.Put(vaultContext("alice"), loginItem())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(meta)
	if err != nil || strings.Contains(string(encoded), "not-in-model-context") {
		t.Fatalf("metadata contains secret: %s %v", encoded, err)
	}
}
