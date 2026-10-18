// admin-keygen writes an Ed25519 seed and public key to new files; keys are never printed.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	private := flag.String("private-file", "", "new private seed file (0600)")
	public := flag.String("public-file", "", "new public key file (0644)")
	flag.Parse()
	if *private == "" || *public == "" {
		fmt.Fprintln(os.Stderr, "both --private-file and --public-file are required")
		os.Exit(1)
	}
	a, _ := filepath.Abs(*private)
	b, _ := filepath.Abs(*public)
	if a == b {
		fmt.Fprintln(os.Stderr, "use separate files")
		os.Exit(1)
	}
	if err := generate(*private, *public); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Key files created; private seed was not printed")
}
func generate(private, public string) error {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	privFile, e := os.OpenFile(private, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	pubFile, e := os.OpenFile(public, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if e != nil {
		privFile.Close()
		os.Remove(private)
		return e
	}
	success := false
	defer func() {
		privFile.Close()
		pubFile.Close()
		if !success {
			os.Remove(private)
			os.Remove(public)
		}
	}()
	if _, e = privFile.WriteString(base64.StdEncoding.EncodeToString(key.Seed()) + "\n"); e != nil {
		return e
	}
	if _, e = pubFile.WriteString(base64.StdEncoding.EncodeToString(pub) + "\n"); e != nil {
		return e
	}
	if e = privFile.Sync(); e != nil {
		return e
	}
	if e = pubFile.Sync(); e != nil {
		return e
	}
	success = true
	return nil
}
