//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeChecker returns whatever stock the test sets for each store.
type fakeChecker struct {
	inStock map[string]map[string]bool // store -> part -> in stock
	errs    map[string]error
}

// CheckStore answers from the fake's maps instead of calling Apple.
func (f *fakeChecker) CheckStore(_ context.Context, store string, parts []string) ([]Availability, error) {
	if err := f.errs[store]; err != nil {
		return nil, err
	}

	out := make([]Availability, 0, len(parts))

	for _, p := range parts {
		a := Availability{StoreNumber: store, Part: p, Display: "unavailable", Quote: "Unavailable for Pickup"}
		if f.inStock[store][p] {
			a.Display, a.Quote = "available", "Available Today"
		}

		out = append(out, a)
	}

	return out, nil
}

// set marks one part at one store in or out of stock.
func (f *fakeChecker) set(store, part string, inStock bool) {
	if f.inStock[store] == nil {
		f.inStock[store] = map[string]bool{}
	}

	f.inStock[store][part] = inStock
}

// fakeTexter records texts instead of sending them.
type fakeTexter struct {
	sent []string // "to: body"
	err  error
}

// SendText records the text, or fails if the test set err.
func (f *fakeTexter) SendText(_ context.Context, to, body string) (string, error) {
	if f.err != nil {
		return "", f.err
	}

	f.sent = append(f.sent, to+": "+body)

	return fmt.Sprintf("SM%d", len(f.sent)), nil
}

// testHarness holds a watcher and its fakes with a clock the test moves.
type testHarness struct {
	w       *Watcher
	checker *fakeChecker
	texter  *fakeTexter
	clock   time.Time
}

// newHarness builds a watcher with two stores, two products, one phone
// number, and the default 60 second check / 3 minute text / 30 text limit.
func newHarness(t *testing.T) *testHarness {
	t.Helper()

	cfg := &Config{
		TextTo:   []string{"+15035551234"},
		TextLink: "https://example.com/buy",
		Stores:   []Store{{Number: "R077", Name: "Pioneer Place"}, {Number: "R090", Name: "Washington Square"}},
		Products: []Product{{Part: "A/A", Name: "Phone A"}, {Part: "B/A", Name: "Phone B"}},
	}
	cfg.applyDefaults()

	h := &testHarness{
		checker: &fakeChecker{inStock: map[string]map[string]bool{}, errs: map[string]error{}},
		texter:  &fakeTexter{},
		clock:   time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}

	h.w = NewWatcher(cfg, h.checker, h.texter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.w.now = func() time.Time { return h.clock }
	h.w.pause = func(context.Context, time.Duration) {}

	return h
}

// runFor runs one check per check interval for the given time, starting now.
func (h *testHarness) runFor(d time.Duration) {
	for end := h.clock.Add(d); h.clock.Before(end); h.clock = h.clock.Add(h.w.cfg.CheckInterval()) {
		h.w.CheckOnce(context.Background())
	}
}

// TestNoStockNoText checks nothing is sent while everything is out of stock.
func TestNoStockNoText(t *testing.T) {
	h := newHarness(t)
	h.runFor(10 * time.Minute)

	if len(h.texter.sent) != 0 {
		t.Errorf("sent %d texts, want 0", len(h.texter.sent))
	}
}

// TestStockTextsRightAwayThenEveryThreeMinutes checks the first text goes
// out on the first check, and then one every 3 minutes, not every check.
func TestStockTextsRightAwayThenEveryThreeMinutes(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)

	h.w.CheckOnce(context.Background())

	if len(h.texter.sent) != 1 {
		t.Fatalf("sent %d texts after first check, want 1", len(h.texter.sent))
	}

	// Checks at 1:00 through 9:00 should text at 3:00, 6:00 and 9:00.
	h.clock = h.clock.Add(time.Minute)
	h.runFor(9 * time.Minute)

	if len(h.texter.sent) != 4 {
		t.Errorf("sent %d texts in 10 minutes, want 4", len(h.texter.sent))
	}
}

// TestStopsAtTextLimit checks a trigger stops texting after max texts.
func TestStopsAtTextLimit(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.MaxTextsPerTrigger = 3
	h.checker.set("R077", "A/A", true)

	h.runFor(time.Hour)

	if len(h.texter.sent) != 3 {
		t.Errorf("sent %d texts, want 3", len(h.texter.sent))
	}

	if !strings.Contains(h.texter.sent[2], "Alert 3 of 3.") {
		t.Errorf("last text = %q, want Alert 3 of 3", h.texter.sent[2])
	}
}

// TestDefaultLimitIsThirty checks the default config stops at 30 texts.
func TestDefaultLimitIsThirty(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)

	h.runFor(5 * time.Hour)

	if len(h.texter.sent) != 30 {
		t.Errorf("sent %d texts, want 30", len(h.texter.sent))
	}
}

// TestSoldOutAndBackStartsNewTrigger checks a part that sells out and
// returns gets a fresh text budget.
func TestSoldOutAndBackStartsNewTrigger(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.MaxTextsPerTrigger = 2
	h.checker.set("R077", "A/A", true)

	h.runFor(time.Hour)

	if len(h.texter.sent) != 2 {
		t.Fatalf("sent %d texts, want 2", len(h.texter.sent))
	}

	h.checker.set("R077", "A/A", false)
	h.runFor(time.Minute)

	if len(h.w.triggers) != 0 {
		t.Fatalf("triggers = %d after sell out, want 0", len(h.w.triggers))
	}

	h.checker.set("R077", "A/A", true)
	h.runFor(time.Hour)

	if len(h.texter.sent) != 4 {
		t.Errorf("sent %d texts, want 4", len(h.texter.sent))
	}
}

// TestFlickeringStockStillWaitsThreeMinutes checks stock that keeps going
// on and off does not text more than once per 3 minutes.
func TestFlickeringStockStillWaitsThreeMinutes(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < 10; i++ {
		h.checker.set("R077", "A/A", i%2 == 0)
		h.w.CheckOnce(context.Background())
		h.clock = h.clock.Add(time.Minute)
	}

	// Stock is in on even minutes from 0:00 to 8:00. Texts go out at 0:00,
	// 4:00 and 8:00; 2:00 and 6:00 are too soon after the last text.
	if len(h.texter.sent) != 3 {
		t.Errorf("sent %d texts, want 3", len(h.texter.sent))
	}
}

// TestNewStockElsewhereAfterLimit checks new stock at another store still
// texts after the first alert used up its budget.
func TestNewStockElsewhereAfterLimit(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.MaxTextsPerTrigger = 1
	h.checker.set("R077", "A/A", true)

	h.runFor(10 * time.Minute)

	h.checker.set("R090", "B/A", true)
	h.runFor(10 * time.Minute)

	if len(h.texter.sent) != 2 {
		t.Fatalf("sent %d texts, want 2", len(h.texter.sent))
	}

	last := h.texter.sent[1]
	if !strings.Contains(last, "Phone B at Washington Square") || strings.Contains(last, "Phone A") {
		t.Errorf("second text = %q, want only Phone B", last)
	}
}

// TestFailedStoreKeepsTrigger checks an Apple error does not clear an alert.
func TestFailedStoreKeepsTrigger(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.w.CheckOnce(context.Background())

	h.checker.errs["R077"] = errors.New("apple is down")
	h.clock = h.clock.Add(3 * time.Minute)
	h.w.CheckOnce(context.Background())

	if len(h.w.triggers) != 1 {
		t.Errorf("triggers = %d, want 1", len(h.w.triggers))
	}

	if len(h.texter.sent) != 2 {
		t.Errorf("sent %d texts, want 2", len(h.texter.sent))
	}
}

// TestFailedTextRetriesNextCheck checks a failed send is not counted and is
// tried again on the next check, not 3 minutes later.
func TestFailedTextRetriesNextCheck(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.texter.err = errors.New("twilio down")

	h.w.CheckOnce(context.Background())

	h.texter.err = nil
	h.clock = h.clock.Add(h.w.cfg.CheckInterval())
	h.w.CheckOnce(context.Background())

	if len(h.texter.sent) != 1 {
		t.Fatalf("sent %d texts, want 1", len(h.texter.sent))
	}

	if got := h.w.triggers[triggerKey("R077", "A/A")].TextsSent; got != 1 {
		t.Errorf("texts counted = %d, want 1", got)
	}
}

// TestTextGoesToEveryNumber checks each phone number in text_to gets the text.
func TestTextGoesToEveryNumber(t *testing.T) {
	h := newHarness(t)
	h.w.cfg.TextTo = []string{"+15035551111", "+15035552222"}
	h.checker.set("R077", "A/A", true)

	h.w.CheckOnce(context.Background())

	if len(h.texter.sent) != 2 ||
		!strings.HasPrefix(h.texter.sent[0], "+15035551111: ") ||
		!strings.HasPrefix(h.texter.sent[1], "+15035552222: ") {
		t.Errorf("sent = %q", h.texter.sent)
	}
}

// TestMessageText checks the text lists each item, the alert number and link.
func TestMessageText(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.checker.set("R090", "B/A", true)

	h.w.CheckOnce(context.Background())

	want := "+15035551234: Apple stock alert!\n" +
		"- Phone A at Pioneer Place (Available Today)\n" +
		"- Phone B at Washington Square (Available Today)\n" +
		"Alert 1 of 30. https://example.com/buy"

	if len(h.texter.sent) != 1 || h.texter.sent[0] != want {
		t.Errorf("got:\n%q\nwant:\n%q", h.texter.sent, want)
	}
}

// TestMessageTextStaysShort checks a long list is cut off with "+N more".
func TestMessageTextStaysShort(t *testing.T) {
	h := newHarness(t)

	var due []*trigger
	for i := 0; i < 60; i++ {
		due = append(due, &trigger{ProductName: fmt.Sprintf("A product with a long name %02d", i), StoreName: "Some Store", Quote: "Available Today"})
	}

	msg := h.w.buildMessage(due)

	if len(msg) > 1600 {
		t.Errorf("message is %d chars, want 1600 or less", len(msg))
	}

	if !strings.Contains(msg, " more\n") {
		t.Errorf("message should end with +N more: %q", msg[len(msg)-80:])
	}
}
