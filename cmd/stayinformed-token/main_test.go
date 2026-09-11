package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteTokenFileReplacesValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens", "access-token")
	if err := writeTokenFile(path, "first"); err != nil {
		t.Fatal(err)
	}
	if err := writeTokenFile(path, "second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second\n" {
		t.Fatalf("unexpected token file contents")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected permissions %o", info.Mode().Perm())
	}
}

func TestClearTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access-token")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearTokenFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file still exists: %v", err)
	}
	if err := clearTokenFile(path); err != nil {
		t.Fatalf("clearing a missing token file must succeed: %v", err)
	}
}
