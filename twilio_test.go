//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestTwilioClient points a TwilioClient at a test server.
func newTestTwilioClient(url string) *TwilioClient {
	return &TwilioClient{
		BaseURL:    url,
		AccountSID: "ACtest",
		AuthToken:  "secret",
		From:       "+19517833868",
		HTTP:       http.DefaultClient,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestSendText checks the request is signed in and carries From, To and Body.
func TestSendText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/2010-04-01/Accounts/ACtest/Messages.json" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}

		user, pass, ok := r.BasicAuth()
		if !ok || user != "ACtest" || pass != "secret" {
			t.Errorf("basic auth = %q %q %v", user, pass, ok)
		}

		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}

		if r.PostForm.Get("From") != "+19517833868" || r.PostForm.Get("To") != "+15035551234" || r.PostForm.Get("Body") != "hello" {
			t.Errorf("form = %v", r.PostForm)
		}

		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"sid":"SM123","status":"queued"}`)
	}))
	defer srv.Close()

	sid, err := newTestTwilioClient(srv.URL).SendText(context.Background(), "+15035551234", "hello")
	if err != nil {
		t.Fatal(err)
	}

	if sid != "SM123" {
		t.Errorf("sid = %q, want SM123", sid)
	}
}

// TestSendTextError checks Twilio's own error message reaches the caller.
func TestSendTextError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":21211,"message":"Invalid 'To' Phone Number","more_info":"https://www.twilio.com/docs/errors/21211","status":400}`)
	}))
	defer srv.Close()

	_, err := newTestTwilioClient(srv.URL).SendText(context.Background(), "+1", "hello")
	if err == nil || !strings.Contains(err.Error(), "21211") || !strings.Contains(err.Error(), "Invalid 'To' Phone Number") {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyAccount checks the account name and status are read back.
func TestVerifyAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/2010-04-01/Accounts/ACtest.json" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}

		io.WriteString(w, `{"friendly_name":"Test Account","status":"active"}`)
	}))
	defer srv.Close()

	name, status, err := newTestTwilioClient(srv.URL).VerifyAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if name != "Test Account" || status != "active" {
		t.Errorf("got %q %q", name, status)
	}
}

// TestVerifyAccountBadKeys checks a 401 becomes an error.
func TestVerifyAccountBadKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":20003,"message":"Authenticate","more_info":"https://www.twilio.com/docs/errors/20003","status":401}`)
	}))
	defer srv.Close()

	if _, _, err := newTestTwilioClient(srv.URL).VerifyAccount(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
}

// TestNewTwilioClientFromEnv checks every missing value is named at once.
func TestNewTwilioClientFromEnv(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "")
	t.Setenv("TWILIO_AUTH_TOKEN", "")
	t.Setenv("TWILIO_FROM_NUMBER", "+19517833868")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := NewTwilioClientFromEnv(log)
	if err == nil || !strings.Contains(err.Error(), "TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN") {
		t.Fatalf("got %v", err)
	}

	t.Setenv("TWILIO_ACCOUNT_SID", "ACtest")
	t.Setenv("TWILIO_AUTH_TOKEN", "secret")

	c, err := NewTwilioClientFromEnv(log)
	if err != nil {
		t.Fatal(err)
	}

	if c.AccountSID != "ACtest" || c.From != "+19517833868" || c.BaseURL != twilioBaseURL {
		t.Errorf("client = %+v", c)
	}
}
