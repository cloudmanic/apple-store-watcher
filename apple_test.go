//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAppleReply builds a pickup-message reply listing each store in
// stores. Parts in available ("store|part") are "available"; the rest are
// "unavailable".
func fakeAppleReply(stores, parts []string, available map[string]bool) []byte {
	var storeList []any

	for _, store := range stores {
		pa := map[string]any{}

		for _, p := range parts {
			display, quote := "unavailable", "Unavailable for Pickup"
			if available[store+"|"+p] {
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

		storeList = append(storeList, map[string]any{
			"storeNumber":       store,
			"storeName":         "Store " + store,
			"city":              "Portland",
			"state":             "OR",
			"partsAvailability": pa,
		})
	}

	out, _ := json.Marshal(map[string]any{
		"head": map[string]any{"status": "200"},
		"body": map[string]any{"stores": storeList},
	})

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

// TestCheckLocationParsesReply checks one lookup returns every store in the
// reply, with statuses, names and quotes, and that the request carries the
// zip code, parts and a browser user agent.
func TestCheckLocationParsesReply(t *testing.T) {
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		if r.URL.Path != "/shop/retail/pickup-message" {
			t.Errorf("path = %s", r.URL.Path)
		}

		if got := r.URL.Query().Get("location"); got != "97201" {
			t.Errorf("location = %s, want 97201", got)
		}

		if r.URL.Query().Has("store") {
			t.Error("request should not name a store")
		}

		if !strings.Contains(r.Header.Get("User-Agent"), "Mozilla") {
			t.Errorf("user agent = %q, want a browser name", r.Header.Get("User-Agent"))
		}

		w.Write(fakeAppleReply([]string{"R077", "R090"}, partsFromQuery(r), map[string]bool{"R090|B/A": true}))
	}))
	defer srv.Close()

	got, err := newTestAppleClient(srv.URL).CheckLocation(context.Background(), "97201", []string{"A/A", "B/A"})
	if err != nil {
		t.Fatal(err)
	}

	if requests != 1 {
		t.Errorf("made %d requests, want 1", requests)
	}

	if len(got) != 4 {
		t.Fatalf("got %d results, want 4 (2 stores x 2 parts)", len(got))
	}

	for _, r := range got {
		wantAvailable := r.StoreNumber == "R090" && r.Part == "B/A"

		if r.Available() != wantAvailable {
			t.Errorf("%s %s available = %v, want %v", r.StoreNumber, r.Part, r.Available(), wantAvailable)
		}

		if r.StoreName != "Store "+r.StoreNumber || r.Title != "Title "+r.Part {
			t.Errorf("names = %q / %q", r.StoreName, r.Title)
		}
	}
}

// TestCheckLocationSplitsLargeLists checks more than 8 parts go out in
// batches and come back as one list.
func TestCheckLocationSplitsLargeLists(t *testing.T) {
	var batches []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := partsFromQuery(r)
		batches = append(batches, len(parts))
		w.Write(fakeAppleReply([]string{"R077"}, parts, nil))
	}))
	defer srv.Close()

	parts := make([]string, 11)
	for i := range parts {
		parts[i] = fmt.Sprintf("P%02d", i)
	}

	got, err := newTestAppleClient(srv.URL).CheckLocation(context.Background(), "97201", parts)
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

// TestCheckLocationBlocked checks Apple's limit codes become a BlockedError,
// so the watcher knows to wait longer.
func TestCheckLocationBlocked(t *testing.T) {
	for _, status := range []int{541, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, "<html><title>Page Not Found - Apple</title></html>")
			}))
			defer srv.Close()

			_, err := newTestAppleClient(srv.URL).CheckLocation(context.Background(), "97201", []string{"A/A"})

			var blocked *BlockedError
			if !errors.As(err, &blocked) || blocked.Status != status {
				t.Fatalf("want BlockedError with status %d, got %v", status, err)
			}
		})
	}
}

// TestCheckLocationErrors checks each other kind of bad reply becomes a
// plain error, not a block.
func TestCheckLocationErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http error", 500, "<html>oops</html>", "HTTP 500"},
		{"not json", 200, "<html>hi</html>", "not JSON"},
		{"apple error", 200, `{"body":{"errorMessage":"Product(s) Invalid or not buyable"}}`, "Product(s) Invalid"},
		{"no stores", 200, `{"body":{"stores":[]}}`, "no stores near"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer srv.Close()

			_, err := newTestAppleClient(srv.URL).CheckLocation(context.Background(), "97201", []string{"A/A"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error with %q, got %v", c.want, err)
			}

			var blocked *BlockedError
			if errors.As(err, &blocked) {
				t.Errorf("got a BlockedError, want a plain error")
			}
		})
	}
}

// TestCheckLocationSkipsMissingPart checks a part Apple left out is skipped,
// not reported as out of stock.
func TestCheckLocationSkipsMissingPart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakeAppleReply([]string{"R077"}, []string{"A/A"}, nil))
	}))
	defer srv.Close()

	got, err := newTestAppleClient(srv.URL).CheckLocation(context.Background(), "97201", []string{"A/A", "B/A"})
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
