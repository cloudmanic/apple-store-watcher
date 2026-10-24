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

// fakeChecker answers lookups from stock the test sets, and records when
// each lookup happened.
type fakeChecker struct {
	stores  []string                   // stores included in each reply
	inStock map[string]map[string]bool // store -> part -> in stock
	err     error
	now     func() time.Time
	calls   []time.Time
}

// CheckLocation answers from the fake's maps instead of calling Apple.
func (f *fakeChecker) CheckLocation(_ context.Context, _ string, parts []string) ([]Availability, error) {
	f.calls = append(f.calls, f.now())

	if f.err != nil {
		return nil, f.err
	}

	var out []Availability

	for _, store := range f.stores {
		for _, p := range parts {
			a := Availability{StoreNumber: store, Part: p, Display: "unavailable", Quote: "Unavailable for Pickup"}
			if f.inStock[store][p] {
				a.Display, a.Quote = "available", "Available Today"
			}

			out = append(out, a)
		}
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
	start   time.Time
	clock   time.Time
}

// newHarness builds a watcher with two stores, two products, one phone
// number, and the default 60 second check / 3 minute text / 30 text limit.
func newHarness(t *testing.T) *testHarness {
	t.Helper()

	cfg := &Config{
		TextTo:         []string{"+15035551234"},
		TextLink:       "https://example.com/buy",
		SearchLocation: "97201",
		Stores:         []Store{{Number: "R077", Name: "Pioneer Place"}, {Number: "R090", Name: "Washington Square"}},
		Products:       []Product{{Part: "A/A", Name: "Phone A"}, {Part: "B/A", Name: "Phone B"}},
	}
	cfg.applyDefaults()

	h := &testHarness{
		texter: &fakeTexter{},
		start:  time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}
	h.clock = h.start

	h.checker = &fakeChecker{
		stores:  []string{"R077", "R090", "R999"},
		inStock: map[string]map[string]bool{},
		now:     func() time.Time { return h.clock },
	}

	h.w = NewWatcher(cfg, h.checker, h.texter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.w.now = func() time.Time { return h.clock }

	return h
}

// runFor runs one check per check interval for the given time, starting now.
func (h *testHarness) runFor(d time.Duration) {
	for end := h.clock.Add(d); h.clock.Before(end); h.clock = h.clock.Add(h.w.cfg.CheckInterval()) {
		h.w.CheckOnce(context.Background())
	}
}

// callMinutes lists when each Apple lookup happened, in minutes from start.
func (h *testHarness) callMinutes() []int {
	var mins []int

	for _, c := range h.checker.calls {
		mins = append(mins, int(c.Sub(h.start).Minutes()))
	}

	return mins
}

// TestOneLookupPerCheck checks every check makes exactly one Apple lookup,
// no matter how many stores are watched.
func TestOneLookupPerCheck(t *testing.T) {
	h := newHarness(t)
	h.runFor(5 * time.Minute)

	if len(h.checker.calls) != 5 {
		t.Errorf("made %d lookups in 5 checks, want 5", len(h.checker.calls))
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

// TestUnwatchedStoreIgnored checks stock at a store not in the config (but
// in Apple's reply) does not trigger a text.
func TestUnwatchedStoreIgnored(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R999", "A/A", true)
	h.runFor(5 * time.Minute)

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

// TestStoreMissingFromReplyKeepsTrigger checks a store Apple left out of
// its reply keeps its alert instead of clearing it.
func TestStoreMissingFromReplyKeepsTrigger(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.w.CheckOnce(context.Background())

	h.checker.stores = []string{"R090"}
	h.clock = h.clock.Add(3 * time.Minute)
	h.w.CheckOnce(context.Background())

	if len(h.w.triggers) != 1 {
		t.Errorf("triggers = %d, want 1", len(h.w.triggers))
	}

	if len(h.texter.sent) != 2 {
		t.Errorf("sent %d texts, want 2", len(h.texter.sent))
	}
}

// TestFailedCheckKeepsTextingAlerts checks a plain Apple error keeps the
// current alerts and their 3 minute texts going, and retries next check.
func TestFailedCheckKeepsTextingAlerts(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.w.CheckOnce(context.Background())

	h.checker.err = errors.New("connection reset")
	h.clock = h.clock.Add(time.Minute)
	h.runFor(3 * time.Minute)

	if len(h.w.triggers) != 1 {
		t.Errorf("triggers = %d, want 1", len(h.w.triggers))
	}

	if len(h.texter.sent) != 2 {
		t.Errorf("sent %d texts, want 2", len(h.texter.sent))
	}

	if got := fmt.Sprint(h.callMinutes()); got != "[0 1 2 3]" {
		t.Errorf("lookups at minutes %s, want [0 1 2 3]", got)
	}
}

// TestBlockBacksOff checks the wait after a block goes 5, 10, 20, then 30
// minutes, with no lookups in between.
func TestBlockBacksOff(t *testing.T) {
	h := newHarness(t)
	h.checker.err = &BlockedError{Status: 541}

	h.runFor(100 * time.Minute)

	if got := fmt.Sprint(h.callMinutes()); got != "[0 5 15 35 65 95]" {
		t.Errorf("lookups at minutes %s, want [0 5 15 35 65 95]", got)
	}
}

// TestBlockRetryToleratesTimerDrift checks a tick that fires a moment
// before the wait ends still makes the retry, instead of waiting a whole
// extra check.
func TestBlockRetryToleratesTimerDrift(t *testing.T) {
	h := newHarness(t)
	h.checker.err = &BlockedError{Status: 541}
	h.w.CheckOnce(context.Background())

	h.clock = h.start.Add(blockBackoffStart - 200*time.Millisecond)
	h.w.CheckOnce(context.Background())

	if len(h.checker.calls) != 2 {
		t.Errorf("made %d lookups, want 2", len(h.checker.calls))
	}
}

// TestBlockTextsOnceAfterThirtyMinutes checks the user gets one text when a
// block reaches 30 minutes, and no more while it lasts.
func TestBlockTextsOnceAfterThirtyMinutes(t *testing.T) {
	h := newHarness(t)
	h.checker.err = &BlockedError{Status: 541}

	h.runFor(30 * time.Minute)

	if len(h.texter.sent) != 0 {
		t.Fatalf("sent %d texts before 30 minutes, want 0", len(h.texter.sent))
	}

	h.runFor(2 * time.Hour)

	if len(h.texter.sent) != 1 {
		t.Fatalf("sent %d texts, want 1", len(h.texter.sent))
	}

	if !strings.Contains(h.texter.sent[0], "blocked stock checks for 30 minutes") {
		t.Errorf("text = %q", h.texter.sent[0])
	}
}

// TestBlockClearedTextsWorksAgain checks that after a long block, the first
// good lookup texts "work again" and checks go back to every minute.
func TestBlockClearedTextsWorksAgain(t *testing.T) {
	h := newHarness(t)
	h.checker.err = &BlockedError{Status: 541}

	h.runFor(40 * time.Minute)

	h.checker.err = nil
	h.runFor(30 * time.Minute)

	if len(h.texter.sent) != 2 || !strings.Contains(h.texter.sent[1], "work again") {
		t.Fatalf("sent = %q, want block text then work again text", h.texter.sent)
	}

	// Blocked lookups at 0, 5, 15, 35; the good one at 65; then every minute.
	if got := fmt.Sprint(h.callMinutes()); got != "[0 5 15 35 65 66 67 68 69]" {
		t.Errorf("lookups at minutes %s", got)
	}
}

// TestShortBlockNoText checks a block that clears before 30 minutes sends
// no texts at all.
func TestShortBlockNoText(t *testing.T) {
	h := newHarness(t)
	h.checker.err = &BlockedError{Status: 541}

	h.runFor(10 * time.Minute)

	h.checker.err = nil
	h.runFor(20 * time.Minute)

	if len(h.texter.sent) != 0 {
		t.Errorf("sent %q, want no texts", h.texter.sent)
	}

	if !h.w.blockedSince.IsZero() {
		t.Error("block state should be cleared")
	}
}

// TestBlockKeepsTextingAlerts checks stock seen before a block keeps its 3
// minute texts while the watcher waits out the block.
func TestBlockKeepsTextingAlerts(t *testing.T) {
	h := newHarness(t)
	h.checker.set("R077", "A/A", true)
	h.w.CheckOnce(context.Background())

	h.checker.err = &BlockedError{Status: 541}
	h.clock = h.clock.Add(time.Minute)
	h.runFor(9 * time.Minute)

	// Texts at 0:00, 3:00, 6:00 and 9:00.
	if len(h.texter.sent) != 4 {
		t.Errorf("sent %d texts, want 4", len(h.texter.sent))
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
