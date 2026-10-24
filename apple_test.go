//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAppleReply builds a pickup-message reply for one store. Parts listed
// in available are "available"; the rest are "unavailable".
func fakeAppleReply(storeNumber string, parts []string, available map[string]bool) []byte {
	pa := map[string]any{}

	for _, p := range parts {
		display, quote := "unavailable", "Unavailable for Pickup"
		if available[p] {
			display, quote = "available", "Available Today"
		}

		pa[p] = map[string]any{
			"partNumber":        p,
			"pickupDisplay":     display,
			"pickupSearchQuote": quote,
			"messageTypes": map[string]any{
				"regular": map[string]any{"storePickupProductTitle": "Title " + p},
			},
		}
	}

	reply := map[string]any{
		"head": map[string]any{"status": "200"},
		"body": map[string]any{
			"stores": []any{map[string]any{
				"storeNumber":       storeNumber,
				"storeName":         "Store " + storeNumber,
				"city":              "Portland",
				"state":             "OR",
				"partsAvailability": pa,
			}},
		},
	}

	out, _ := json.Marshal(reply)

	return out
}

// newTestAppleClient points an AppleClient at a test server.
func newTestAppleClient(url string) *AppleClient {
	return &AppleClient{
		BaseURL: url,
		HTTP:    http.DefaultClient,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// partsFromQuery reads parts.0, parts.1, ... from a request in order.
func partsFromQuery(r *http.Request) []string {
	var parts []string

	for i := 0; ; i++ {
		p := r.URL.Query().Get(fmt.Sprintf("parts.%d", i))
		if p == "" {
			return parts
		}

		parts = append(parts, p)
	}
}

// TestCheckStoreParsesReply checks statuses, names and quotes come through,
// and the request carries the store, parts and a browser user agent.
func TestCheckStoreParsesReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shop/retail/pickup-message" {
			t.Errorf("path = %s", r.URL.Path)
		}

		if got := r.URL.Query().Get("store"); got != "R077" {
			t.Errorf("store = %s, want R077", got)
		}

		if !strings.Contains(r.Header.Get("User-Agent"), "Mozilla") {
			t.Errorf("user agent = %q, want a browser name", r.Header.Get("User-Agent"))
		}

		w.Write(fakeAppleReply("R077", partsFromQuery(r), map[string]bool{"B/A": true}))
	}))
	defer srv.Close()

	got, err := newTestAppleClient(srv.URL).CheckStore(context.Background(), "R077", []string{"A/A", "B/A"})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}

	if got[0].Part != "A/A" || got[0].Available() || got[0].Quote != "Unavailable for Pickup" {
		t.Errorf("first result = %+v", got[0])
	}

	if got[1].Part != "B/A" || !got[1].Available() || got[1].Quote != "Available Today" {
		t.Errorf("second result = %+v", got[1])
	}

	if got[1].StoreName != "Store R077" || got[1].Title != "Title B/A" {
		t.Errorf("names = %q / %q", got[1].StoreName, got[1].Title)
	}
}

// TestCheckStoreSplitsLargeLists checks more than 8 parts go out in batches
// and come back as one list.
func TestCheckStoreSplitsLargeLists(t *testing.T) {
	var batches []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := partsFromQuery(r)
		batches = append(batches, len(parts))
		w.Write(fakeAppleReply("R077", parts, nil))
	}))
	defer srv.Close()

	parts := make([]string, 11)
	for i := range parts {
		parts[i] = fmt.Sprintf("P%02d", i)
	}

	got, err := newTestAppleClient(srv.URL).CheckStore(context.Background(), "R077", parts)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 11 {
		t.Errorf("got %d results, want 11", len(got))
	}

	if fmt.Sprint(batches) != "[8 3]" {
		t.Errorf("batches = %v, want [8 3]", batches)
	}
}

// TestCheckStoreErrors checks each kind of bad reply becomes a clear error.
func TestCheckStoreErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http error", 503, "<html>busy</html>", "HTTP 503"},
		{"not json", 200, "<html>hi</html>", "not JSON"},
		{"apple error", 200, `{"body":{"errorMessage":"Product(s) Invalid or not buyable"}}`, "Product(s) Invalid"},
		{"store missing", 200, `{"body":{"stores":[{"storeNumber":"R999"}]}}`, "did not include store R077"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer srv.Close()

			_, err := newTestAppleClient(srv.URL).CheckStore(context.Background(), "R077", []string{"A/A"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error with %q, got %v", c.want, err)
			}
		})
	}
}

// TestCheckStoreSkipsMissingPart checks a part Apple left out is skipped,
// not reported as out of stock.
func TestCheckStoreSkipsMissingPart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakeAppleReply("R077", []string{"A/A"}, nil))
	}))
	defer srv.Close()

	got, err := newTestAppleClient(srv.URL).CheckStore(context.Background(), "R077", []string{"A/A", "B/A"})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Part != "A/A" {
		t.Errorf("got %+v, want only A/A", got)
	}
}

// TestSnippetShortensLongBodies checks long replies are cut and flattened.
func TestSnippetShortensLongBodies(t *testing.T) {
	got := snippet([]byte(strings.Repeat("ab\n", 200)))

	if len(got) != 303 || strings.Contains(got, "\n") || !strings.HasSuffix(got, "...") {
		t.Errorf("snippet length %d, got %q", len(got), got[:20])
	}
}
