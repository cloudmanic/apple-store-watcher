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
	"time"
)

// writeConfig saves body to a temp config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestLoadConfigAppliesDefaults checks a minimal config gets the 30 second,
// 3 minute, 30 text defaults.
func TestLoadConfigAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `{
		"text_to": ["+15035551234"],
		"stores": [{"number": "R077"}],
		"products": [{"part": "MK1E4LL/A"}]
	}`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.CheckInterval() != 30*time.Second {
		t.Errorf("check interval = %v, want 30s", cfg.CheckInterval())
	}

	if cfg.TextInterval() != 3*time.Minute {
		t.Errorf("text interval = %v, want 3m", cfg.TextInterval())
	}

	if cfg.MaxTextsPerTrigger != 30 {
		t.Errorf("max texts = %d, want 30", cfg.MaxTextsPerTrigger)
	}

	if cfg.LogFile != "watcher.log" {
		t.Errorf("log file = %q, want watcher.log", cfg.LogFile)
	}
}

// TestLoadConfigExampleFile checks the shipped example config is valid.
func TestLoadConfigExampleFile(t *testing.T) {
	cfg, err := LoadConfig("config.example.json")
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Stores) != 3 || len(cfg.Products) != 8 {
		t.Errorf("got %d stores and %d products, want 3 and 8", len(cfg.Stores), len(cfg.Products))
	}
}

// TestLoadConfigRejectsUnknownKey checks a typo in a key name is an error.
func TestLoadConfigRejectsUnknownKey(t *testing.T) {
	path := writeConfig(t, `{
		"text_too": ["+15035551234"],
		"stores": [{"number": "R077"}],
		"products": [{"part": "MK1E4LL/A"}]
	}`)

	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "text_too") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

// TestLoadConfigMissingFile checks the error points the user at the example.
func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !strings.Contains(err.Error(), "config.example.json") {
		t.Fatalf("want hint about config.example.json, got %v", err)
	}
}

// TestValidateListsEveryProblem checks all bad values are reported together.
func TestValidateListsEveryProblem(t *testing.T) {
	cfg := &Config{
		CheckIntervalSeconds: -1,
		TextTo:               []string{"503-555-1234"},
		Stores:               []Store{{Number: "77"}, {Number: "R090"}, {Number: "R090"}},
		Products:             []Product{{Part: "MK1E4LL/A"}, {Part: "MK1E4LL/A"}, {Part: " "}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want errors, got nil")
	}

	for _, want := range []string{
		"check_interval_seconds",
		`text_to "503-555-1234"`,
		`store number "77"`,
		"store R090 is listed twice",
		"part MK1E4LL/A is listed twice",
		"every product needs a part number",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// TestValidateEmptyLists checks stores, products and phone numbers are required.
func TestValidateEmptyLists(t *testing.T) {
	err := (&Config{}).Validate()
	if err == nil {
		t.Fatal("want errors, got nil")
	}

	for _, want := range []string{"text_to", "stores", "products"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// TestNamesFallBack checks names come from config, then Apple, then the ID.
func TestNamesFallBack(t *testing.T) {
	cfg := &Config{
		Stores:   []Store{{Number: "R077", Name: "Pioneer Place"}, {Number: "R090"}},
		Products: []Product{{Part: "A", Name: "Phone A"}, {Part: "B"}},
	}

	cases := []struct{ got, want string }{
		{cfg.StoreName("R077", "Apple Name"), "Pioneer Place"},
		{cfg.StoreName("R090", "Washington Square"), "Washington Square"},
		{cfg.StoreName("R999", ""), "R999"},
		{cfg.ProductName("A", "Apple Title"), "Phone A"},
		{cfg.ProductName("B", "Apple Title"), "Apple Title"},
		{cfg.ProductName("C", ""), "C"},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// TestPartNumbersKeepsOrder checks parts come back in config order.
func TestPartNumbersKeepsOrder(t *testing.T) {
	cfg := &Config{Products: []Product{{Part: "B"}, {Part: "A"}, {Part: "C"}}}

	if got := strings.Join(cfg.PartNumbers(), ","); got != "B,A,C" {
		t.Errorf("got %s, want B,A,C", got)
	}
}
