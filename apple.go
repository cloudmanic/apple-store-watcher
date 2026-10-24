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

// CheckStore returns the stock of each part at one store. Large part lists
// are split into batches Apple will accept. Any failed batch fails the whole
// store, so the caller never acts on half an answer.
func (c *AppleClient) CheckStore(ctx context.Context, storeNumber string, parts []string) ([]Availability, error) {
	all := make([]Availability, 0, len(parts))

	for start := 0; start < len(parts); start += appleMaxPartsPerRequest {
		end := min(start+appleMaxPartsPerRequest, len(parts))

		got, err := c.checkBatch(ctx, storeNumber, parts[start:end])
		if err != nil {
			return nil, err
		}

		all = append(all, got...)
	}

	return all, nil
}

// checkBatch makes one pickup-message request for up to
// appleMaxPartsPerRequest parts at one store.
func (c *AppleClient) checkBatch(ctx context.Context, storeNumber string, parts []string) ([]Availability, error) {
	q := url.Values{}
	q.Set("store", storeNumber)

	for i, p := range parts {
		q.Set(fmt.Sprintf("parts.%d", i), p)
	}

	reqURL := c.BaseURL + "/shop/retail/pickup-message?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build apple request for store %s: %w", storeNumber, err)
	}

	req.Header.Set("User-Agent", appleUserAgent)
	req.Header.Set("Accept", "application/json")

	c.Log.Debug("apple request", "store", storeNumber, "parts", strings.Join(parts, ","), "url", reqURL)

	started := time.Now()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apple request for store %s: %w", storeNumber, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("read apple reply for store %s: %w", storeNumber, err)
	}

	c.Log.Debug("apple response",
		"store", storeNumber,
		"http_status", resp.StatusCode,
		"bytes", len(body),
		"took", time.Since(started).Round(time.Millisecond),
	)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple returned HTTP %d for store %s: %s", resp.StatusCode, storeNumber, snippet(body))
	}

	var parsed pickupResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("apple reply for store %s was not JSON (%v): %s", storeNumber, err, snippet(body))
	}

	if msg := parsed.Body.ErrorMessage; msg != "" {
		return nil, fmt.Errorf("apple refused the lookup for store %s: %q (check these part numbers: %s)", storeNumber, msg, strings.Join(parts, ","))
	}

	var store *pickupStore
	for i := range parsed.Body.Stores {
		if parsed.Body.Stores[i].StoreNumber == storeNumber {
			store = &parsed.Body.Stores[i]
			break
		}
	}

	if store == nil {
		return nil, fmt.Errorf("apple reply did not include store %s (it had %d other stores)", storeNumber, len(parsed.Body.Stores))
	}

	out := make([]Availability, 0, len(parts))

	for _, p := range parts {
		pa, ok := store.PartsAvailability[p]
		if !ok {
			c.Log.Warn("apple reply is missing a part; skipping it this check", "store", storeNumber, "part", p)
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
