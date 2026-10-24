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
	"net/url"
	"strings"
	"time"
)

// appleBaseURL is the host for Apple's store pickup lookup. Tests swap in a
// local server.
const appleBaseURL = "https://www.apple.com"

// appleMaxPartsPerRequest caps the part numbers in one lookup. Eight is the
// largest batch we have seen Apple answer.
const appleMaxPartsPerRequest = 8

// appleUserAgent is a normal browser name, so Apple treats us like its own
// store page.
const appleUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

// appleAvailable is the pickupDisplay value Apple uses when a store has the
// part on hand for pickup.
const appleAvailable = "available"

// appleBlockStatuses are the HTTP codes Apple sends when it limits us. 541
// is Apple's own code and comes with a "Page Not Found" page.
var appleBlockStatuses = map[int]bool{
	http.StatusTooManyRequests:    true,
	http.StatusServiceUnavailable: true,
	541:                           true,
}

// BlockedError means Apple turned the lookup away because we asked too
// often. The watcher waits longer before the next try when it sees one.
type BlockedError struct {
	Status int
}

// Error says Apple is blocking us and which HTTP code it sent.
func (e *BlockedError) Error() string {
	return fmt.Sprintf("apple is blocking stock checks (HTTP %d)", e.Status)
}

// Availability is Apple's answer for one part at one store.
type Availability struct {
	StoreNumber string
	StoreName   string
	Part        string
	Title       string // Apple's product name, like "iPhone Duo 256GB Star White"
	Display     string // Apple's status word: available, unavailable or ineligible
	Quote       string // Apple's short text, like "Available Today"
}

// Available reports whether the store has this part ready for pickup.
func (a Availability) Available() bool {
	return a.Display == appleAvailable
}

// AppleClient asks apple.com which stores have a part ready for pickup.
type AppleClient struct {
	BaseURL string
	HTTP    *http.Client
	Log     *slog.Logger
}

// NewAppleClient returns a client for the live apple.com site. The timeout
// keeps one slow reply from holding up the next check.
func NewAppleClient(log *slog.Logger) *AppleClient {
	return &AppleClient{
		BaseURL: appleBaseURL,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Log:     log,
	}
}

// pickupResponse is the part of Apple's pickup-message reply we use.
type pickupResponse struct {
	Body struct {
		ErrorMessage string        `json:"errorMessage"`
		Stores       []pickupStore `json:"stores"`
	} `json:"body"`
}

// pickupStore is one store in Apple's reply.
type pickupStore struct {
	StoreNumber       string                `json:"storeNumber"`
	StoreName         string                `json:"storeName"`
	City              string                `json:"city"`
	State             string                `json:"state"`
	PartsAvailability map[string]pickupPart `json:"partsAvailability"`
}

// pickupPart is one part's stock at one store in Apple's reply.
type pickupPart struct {
	PartNumber        string `json:"partNumber"`
	PickupDisplay     string `json:"pickupDisplay"`
	PickupSearchQuote string `json:"pickupSearchQuote"`
	MessageTypes      struct {
		Regular struct {
			StorePickupProductTitle string `json:"storePickupProductTitle"`
		} `json:"regular"`
	} `json:"messageTypes"`
}

// CheckLocation returns the stock of each part at every store Apple lists
// near location (a zip code). One lookup covers all nearby stores, which
// keeps us well under Apple's limit. Large part lists are split into
// batches Apple will accept; any failed batch fails the whole check, so the
// caller never acts on half an answer.
func (c *AppleClient) CheckLocation(ctx context.Context, location string, parts []string) ([]Availability, error) {
	var all []Availability

	for start := 0; start < len(parts); start += appleMaxPartsPerRequest {
		end := min(start+appleMaxPartsPerRequest, len(parts))

		got, err := c.checkBatch(ctx, location, parts[start:end])
		if err != nil {
			return nil, err
		}

		all = append(all, got...)
	}

	return all, nil
}

// checkBatch makes one pickup-message request for up to
// appleMaxPartsPerRequest parts near location.
func (c *AppleClient) checkBatch(ctx context.Context, location string, parts []string) ([]Availability, error) {
	q := url.Values{}
	q.Set("location", location)

	for i, p := range parts {
		q.Set(fmt.Sprintf("parts.%d", i), p)
	}

	reqURL := c.BaseURL + "/shop/retail/pickup-message?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build apple request: %w", err)
	}

	req.Header.Set("User-Agent", appleUserAgent)
	req.Header.Set("Accept", "application/json")

	c.Log.Debug("apple request", "location", location, "parts", strings.Join(parts, ","), "url", reqURL)

	started := time.Now()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apple request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("read apple reply: %w", err)
	}

	c.Log.Debug("apple response",
		"location", location,
		"http_status", resp.StatusCode,
		"bytes", len(body),
		"took", time.Since(started).Round(time.Millisecond),
	)

	if appleBlockStatuses[resp.StatusCode] {
		return nil, &BlockedError{Status: resp.StatusCode}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed pickupResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("apple reply was not JSON (%v): %s", err, snippet(body))
	}

	if msg := parsed.Body.ErrorMessage; msg != "" {
		return nil, fmt.Errorf("apple refused the lookup: %q (check search_location %q and these part numbers: %s)", msg, location, strings.Join(parts, ","))
	}

	if len(parsed.Body.Stores) == 0 {
		return nil, fmt.Errorf("apple found no stores near %q", location)
	}

	storeNumbers := make([]string, 0, len(parsed.Body.Stores))
	out := make([]Availability, 0, len(parsed.Body.Stores)*len(parts))

	for _, store := range parsed.Body.Stores {
		storeNumbers = append(storeNumbers, store.StoreNumber)

		for _, p := range parts {
			pa, ok := store.PartsAvailability[p]
			if !ok {
				continue
			}

			out = append(out, Availability{
				StoreNumber: store.StoreNumber,
				StoreName:   store.StoreName,
				Part:        p,
				Title:       pa.MessageTypes.Regular.StorePickupProductTitle,
				Display:     pa.PickupDisplay,
				Quote:       pa.PickupSearchQuote,
			})
		}
	}

	c.Log.Debug("apple reply stores", "location", location, "stores", strings.Join(storeNumbers, ","))

	return out, nil
}

// snippet shortens a reply body to one line for error messages, so an HTML
// error page does not flood the log.
func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")

	if len(s) > 300 {
		return s[:300] + "..."
	}

	return s
}
