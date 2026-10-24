//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadEnvFile checks the common .env forms load, and shell values win.
func TestLoadEnvFile(t *testing.T) {
	for _, k := range []string{"ASW_PLAIN", "ASW_DOUBLE", "ASW_SINGLE", "ASW_EXPORT", "ASW_COMMENT", "ASW_HASH"} {
		t.Setenv(k, "")
	}

	t.Setenv("ASW_SHELL", "from-shell")

	path := filepath.Join(t.TempDir(), ".env")
	body := `
# a comment
ASW_PLAIN=plain
ASW_DOUBLE="double quoted"
ASW_SINGLE='single quoted'
export ASW_EXPORT=exported
ASW_COMMENT=value # trailing note
ASW_HASH="keep # this"
ASW_SHELL=from-file
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if n != 6 {
		t.Errorf("loaded %d values, want 6", n)
	}

	want := map[string]string{
		"ASW_PLAIN":   "plain",
		"ASW_DOUBLE":  "double quoted",
		"ASW_SINGLE":  "single quoted",
		"ASW_EXPORT":  "exported",
		"ASW_COMMENT": "value",
		"ASW_HASH":    "keep # this",
		"ASW_SHELL":   "from-shell",
	}

	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// TestLoadEnvFileMissing checks a missing file is not an error.
func TestLoadEnvFileMissing(t *testing.T) {
	n, err := LoadEnvFile(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil || n != 0 {
		t.Fatalf("got n=%d err=%v, want 0 and nil", n, err)
	}
}

// TestLoadEnvFileBadLine checks a line with no = names its line number.
func TestLoadEnvFileBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GOOD=1\nnot a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOOD", "")

	_, err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("want error, got nil")
	}

	if got := err.Error(); !strings.Contains(got, "line 2") {
		t.Errorf("error %q should name line 2", got)
	}
}
