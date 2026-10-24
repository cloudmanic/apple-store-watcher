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
	"os"
	"strings"
	"time"
)

// twilioBaseURL is Twilio's REST API host. Tests swap in a local server.
const twilioBaseURL = "https://api.twilio.com"

// TwilioClient sends text messages through Twilio.
type TwilioClient struct {
	BaseURL    string
	AccountSID string
	AuthToken  string
	From       string
	HTTP       *http.Client
	Log        *slog.Logger
}

// NewTwilioClientFromEnv builds a client from the TWILIO_* values in the
// environment (usually from .env), so the keys never sit in config.json.
func NewTwilioClientFromEnv(log *slog.Logger) (*TwilioClient, error) {
	c := &TwilioClient{
		BaseURL:    twilioBaseURL,
		AccountSID: strings.TrimSpace(os.Getenv("TWILIO_ACCOUNT_SID")),
		AuthToken:  strings.TrimSpace(os.Getenv("TWILIO_AUTH_TOKEN")),
		From:       strings.TrimSpace(os.Getenv("TWILIO_FROM_NUMBER")),
		HTTP:       &http.Client{Timeout: 20 * time.Second},
		Log:        log,
	}

	var missing []string

	if c.AccountSID == "" {
		missing = append(missing, "TWILIO_ACCOUNT_SID")
	}

	if c.AuthToken == "" {
		missing = append(missing, "TWILIO_AUTH_TOKEN")
	}

	if c.From == "" {
		missing = append(missing, "TWILIO_FROM_NUMBER")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing %s (set them in .env)", strings.Join(missing, ", "))
	}

	return c, nil
}

// VerifyAccount reads the Twilio account once. The watcher runs it at
// startup so bad keys stop the program now, not when stock finally shows up.
func (c *TwilioClient) VerifyAccount(ctx context.Context) (name, status string, err error) {
	var out struct {
		FriendlyName string `json:"friendly_name"`
		Status       string `json:"status"`
	}

	path := "/2010-04-01/Accounts/" + url.PathEscape(c.AccountSID) + ".json"

	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", "", fmt.Errorf("check twilio account: %w", err)
	}

	return out.FriendlyName, out.Status, nil
}

// SendText sends one SMS and returns Twilio's message ID. Twilio accepting
// the message means it is queued, not yet delivered.
func (c *TwilioClient) SendText(ctx context.Context, to, body string) (string, error) {
	form := url.Values{}
	form.Set("From", c.From)
	form.Set("To", to)
	form.Set("Body", body)

	var out struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
	}

	path := "/2010-04-01/Accounts/" + url.PathEscape(c.AccountSID) + "/Messages.json"

	if err := c.do(ctx, http.MethodPost, path, form, &out); err != nil {
		return "", fmt.Errorf("send text to %s: %w", to, err)
	}

	c.Log.Debug("twilio accepted text", "to", to, "twilio_sid", out.SID, "twilio_status", out.Status)

	return out.SID, nil
}

// do sends one signed-in request to Twilio and decodes the JSON reply into
// out. A non-2xx reply becomes an error that carries Twilio's own message.
func (c *TwilioClient) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.AccountSID, c.AuthToken)
	req.Header.Set("Accept", "application/json")

	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	started := time.Now()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	c.Log.Debug("twilio response",
		"method", method,
		"path", path,
		"http_status", resp.StatusCode,
		"took", time.Since(started).Round(time.Millisecond),
	)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Code     int    `json:"code"`
			Message  string `json:"message"`
			MoreInfo string `json:"more_info"`
		}

		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("twilio HTTP %d, code %d: %s (%s)", resp.StatusCode, apiErr.Code, apiErr.Message, apiErr.MoreInfo)
		}

		return fmt.Errorf("twilio HTTP %d: %s", resp.StatusCode, snippet(raw))
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("twilio reply was not JSON (%v): %s", err, snippet(raw))
	}

	return nil
}
