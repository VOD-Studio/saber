package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupInitDoesNotOverwriteExistingConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Command(context.Background(), []string{"init"}, path); err == nil {
		t.Fatal("existing configuration overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatalf("original changed: %q %v", data, err)
	}
}
func TestSetupSetInvalidConfigurationPreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Command(context.Background(), []string{"init"}, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Command(context.Background(), []string{"set", "desktop.endpoint", "http://remote.invalid"}, path); err == nil {
		t.Fatal("insecure endpoint accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("invalid update modified original")
	}
}
func TestPrivateWriteBacksUpAndRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := writePrivate(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(path, []byte("second"), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path + ".previous")
	if err != nil || string(data) != "first" {
		t.Fatal("backup missing")
	}
	link := path + ".link"
	if err = os.Symlink(path, link); err != nil {
		t.Skip(err)
	}
	if err = writePrivate(link, []byte("bad"), false); err == nil {
		t.Fatal("symlink write accepted")
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatal("symlink target changed")
	}
}
func TestInstallRejectsUntrustedSourceBeforeChangingBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saber")
	if err := os.WriteFile(path, []byte("previous"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), "http://example.com/saber", strings.Repeat("0", 64), path, true); err == nil {
		t.Fatal("HTTP source accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "previous" {
		t.Fatal("binary modified")
	}
}
