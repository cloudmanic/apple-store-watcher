//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

// textSlack lets a text go out a little early, so normal timer drift does
// not push it back a whole check.
const textSlack = time.Second

// storeGap spaces out the per-store lookups so Apple does not see a burst.
const storeGap = time.Second

// maxTextLength keeps a text under Twilio's 1,600 character limit, with room
// for the footer.
const maxTextLength = 1400

// StockChecker is the part of AppleClient the watcher needs. Tests use a fake.
type StockChecker interface {
	CheckStore(ctx context.Context, storeNumber string, parts []string) ([]Availability, error)
}

// Texter is the part of TwilioClient the watcher needs. Tests use a fake.
type Texter interface {
	SendText(ctx context.Context, to, body string) (string, error)
}

// trigger is one product in stock at one store. Each one has its own text
// budget, so new stock at another store still gets a text after an older
// alert has used up its budget.
type trigger struct {
	StoreNumber string
	StoreName   string
	Part        string
	ProductName string
	Quote       string
	FirstSeen   time.Time
	TextsSent   int
}

// Watcher checks Apple on a timer and texts when watched stock shows up.
type Watcher struct {
	cfg      *Config
	checker  StockChecker
	texter   Texter
	log      *slog.Logger
	now      func() time.Time
	pause    func(ctx context.Context, d time.Duration)
	triggers map[string]*trigger
	lastText time.Time
	checks   int
}

// NewWatcher wires a watcher to the real clock. Tests replace now and pause
// so they run instantly.
func NewWatcher(cfg *Config, checker StockChecker, texter Texter, log *slog.Logger) *Watcher {
	return &Watcher{
		cfg:      cfg,
		checker:  checker,
		texter:   texter,
		log:      log,
		now:      time.Now,
		pause:    sleepContext,
		triggers: map[string]*trigger{},
	}
}

// Run checks right away and then on every tick until ctx is cancelled. There
// is no other way to stop it; the user kills the program once they have the
// stock.
func (w *Watcher) Run(ctx context.Context) {
	w.log.Info("watcher running",
		"check_every", w.cfg.CheckInterval(),
		"stores", len(w.cfg.Stores),
		"products", len(w.cfg.Products),
	)

	w.CheckOnce(ctx)

	ticker := time.NewTicker(w.cfg.CheckInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log.Info("stop signal received", "checks_run", w.checks, "active_alerts", len(w.triggers))
			return
		case <-ticker.C:
			w.CheckOnce(ctx)
		}
	}
}

// CheckOnce asks Apple about every store, updates which items are in stock,
// and sends a text if one is due.
func (w *Watcher) CheckOnce(ctx context.Context) {
	w.checks++
	started := w.now()
	parts := w.cfg.PartNumbers()

	w.log.Debug("check starting", "check", w.checks, "stores", len(w.cfg.Stores), "products", len(parts))

	inStock := map[string]Availability{}
	answered := map[string]bool{}
	storesOK, storesFailed := 0, 0

	for i, store := range w.cfg.Stores {
		if ctx.Err() != nil {
			return
		}

		if i > 0 {
			w.pause(ctx, storeGap)
		}

		results, err := w.checker.CheckStore(ctx, store.Number, parts)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			storesFailed++
			w.log.Error("store check failed; keeping current alerts for this store",
				"check", w.checks,
				"store", store.Number,
				"store_name", w.cfg.StoreName(store.Number, ""),
				"error", err,
			)

			continue
		}

		storesOK++

		for _, r := range results {
			key := triggerKey(r.StoreNumber, r.Part)
			answered[key] = true

			level := slog.LevelInfo
			if r.Available() {
				inStock[key] = r
				level = slog.LevelWarn
			}

			w.log.Log(ctx, level, "stock status",
				"check", w.checks,
				"store", r.StoreNumber,
				"store_name", w.cfg.StoreName(r.StoreNumber, r.StoreName),
				"part", r.Part,
				"product", w.cfg.ProductName(r.Part, r.Title),
				"status", r.Display,
				"quote", r.Quote,
			)
		}
	}

	w.updateTriggers(inStock, answered, started)
	w.maybeText(ctx, started)

	w.log.Info("check finished",
		"check", w.checks,
		"took", w.now().Sub(started).Round(time.Millisecond),
		"stores_ok", storesOK,
		"stores_failed", storesFailed,
		"in_stock_now", len(inStock),
		"active_alerts", len(w.triggers),
	)
}

// updateTriggers starts a trigger for each newly in-stock item and clears
// triggers Apple now says are out of stock. A store whose check failed has
// no answer, so its triggers stay as they were.
func (w *Watcher) updateTriggers(inStock map[string]Availability, answered map[string]bool, now time.Time) {
	for _, key := range slices.Sorted(maps.Keys(inStock)) {
		r := inStock[key]

		if t, ok := w.triggers[key]; ok {
			t.Quote = r.Quote
			continue
		}

		t := &trigger{
			StoreNumber: r.StoreNumber,
			StoreName:   w.cfg.StoreName(r.StoreNumber, r.StoreName),
			Part:        r.Part,
			ProductName: w.cfg.ProductName(r.Part, r.Title),
			Quote:       r.Quote,
			FirstSeen:   now,
		}

		w.triggers[key] = t

		w.log.Warn("NEW STOCK FOUND",
			"store", t.StoreNumber,
			"store_name", t.StoreName,
			"part", t.Part,
			"product", t.ProductName,
			"quote", t.Quote,
		)
	}

	for _, key := range slices.Sorted(maps.Keys(w.triggers)) {
		if _, still := inStock[key]; still || !answered[key] {
			continue
		}

		t := w.triggers[key]
		delete(w.triggers, key)

		w.log.Warn("stock gone; alert cleared",
			"store", t.StoreNumber,
			"store_name", t.StoreName,
			"part", t.Part,
			"product", t.ProductName,
			"in_stock_for", now.Sub(t.FirstSeen).Round(time.Second),
			"texts_sent", t.TextsSent,
		)
	}
}

// maybeText sends one text listing every in-stock item that still has text
// budget. Texts go out no more than once per text interval, even when items
// come and go, so a flickering stock reading cannot flood the phone.
func (w *Watcher) maybeText(ctx context.Context, now time.Time) {
	if len(w.triggers) == 0 {
		return
	}

	limit := w.cfg.MaxTextsPerTrigger

	var due []*trigger
	for _, key := range slices.Sorted(maps.Keys(w.triggers)) {
		t := w.triggers[key]

		if t.TextsSent >= limit {
			w.log.Info("alert hit its text limit; no more texts unless it sells out and comes back",
				"store", t.StoreNumber,
				"product", t.ProductName,
				"texts_sent", t.TextsSent,
				"limit", limit,
			)

			continue
		}

		due = append(due, t)
	}

	if len(due) == 0 {
		return
	}

	if !w.lastText.IsZero() {
		next := w.lastText.Add(w.cfg.TextInterval())

		if now.Before(next.Add(-textSlack)) {
			w.log.Info("text not due yet",
				"last_text", w.lastText.Format(time.TimeOnly),
				"next_text_in", next.Sub(now).Round(time.Second),
				"items", len(due),
			)

			return
		}
	}

	body := w.buildMessage(due)

	w.log.Info("sending text", "recipients", len(w.cfg.TextTo), "items", len(due), "body", body)

	sent := 0
	for _, to := range w.cfg.TextTo {
		sid, err := w.texter.SendText(ctx, to, body)
		if err != nil {
			w.log.Error("text failed", "to", to, "error", err)
			continue
		}

		sent++
		w.log.Info("text sent", "to", to, "twilio_sid", sid)
	}

	if sent == 0 {
		w.log.Error("no texts went out; trying again next check")
		return
	}

	w.lastText = now

	for _, t := range due {
		t.TextsSent++

		w.log.Info("alert text count",
			"store", t.StoreNumber,
			"product", t.ProductName,
			"texts_sent", t.TextsSent,
			"limit", limit,
		)
	}

	w.log.Info("next text due", "at", now.Add(w.cfg.TextInterval()).Format(time.TimeOnly))
}

// buildMessage writes the text body: one line per in-stock item, then the
// alert number and the optional buy link. Items past maxTextLength are
// summed up as "+N more" so the text never gets too long to send.
func (w *Watcher) buildMessage(due []*trigger) string {
	var b strings.Builder

	b.WriteString("Apple stock alert!\n")

	alertNo := 0
	hidden := 0

	for _, t := range due {
		alertNo = max(alertNo, t.TextsSent+1)

		line := fmt.Sprintf("- %s at %s", t.ProductName, t.StoreName)
		if t.Quote != "" {
			line += " (" + t.Quote + ")"
		}

		if b.Len()+len(line) > maxTextLength {
			hidden++
			continue
		}

		b.WriteString(line + "\n")
	}

	if hidden > 0 {
		fmt.Fprintf(&b, "+%d more\n", hidden)
	}

	fmt.Fprintf(&b, "Alert %d of %d.", alertNo, w.cfg.MaxTextsPerTrigger)

	if w.cfg.TextLink != "" {
		b.WriteString(" " + w.cfg.TextLink)
	}

	return b.String()
}

// triggerKey names one store and part pair in the triggers map.
func triggerKey(storeNumber, part string) string {
	return storeNumber + "|" + part
}

// sleepContext waits for d, or less if ctx is cancelled, so Ctrl-C is not
// held up by the pause between stores.
func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
