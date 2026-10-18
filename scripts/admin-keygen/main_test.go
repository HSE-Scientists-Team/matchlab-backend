package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeyFilesAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	private, public := filepath.Join(dir, "private"), filepath.Join(dir, "public")
	if e := generate(private, public); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(private)
	if e != nil {
		t.Fatal(e)
	}
	// Windows reports synthetic mode bits; access is controlled by ACLs.
	// Keep testing key generation and no-overwrite behavior on every platform.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions: got %04o, want 0600", info.Mode().Perm())
	}
	priv, e := os.ReadFile(private)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := os.ReadFile(public)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(priv)))
	if e != nil || len(seed) != 32 {
		t.Fatal("bad private seed")
	}
	encodedPublic := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if strings.TrimSpace(string(pub)) != encodedPublic {
		t.Fatal("key pair mismatch")
	}
	if e = generate(private, filepath.Join(dir, "other")); e == nil {
		t.Fatal("overwrote private key")
	}
	after, _ := os.ReadFile(private)
	if string(after) != string(priv) {
		t.Fatal("existing key modified")
	}
}
