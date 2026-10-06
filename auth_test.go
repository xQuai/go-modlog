package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Fake twitch oauth server
type fakeTwitch struct {
	pollsUntilAuthorized int32
	polls                atomic.Int32
	refreshes            atomic.Int32
	validToken           atomic.Value // string
	refreshValid         bool
}

func (f *fakeTwitch) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceResponse{DeviceCode: "dev", UserCode: "ABCD", VerificationURI: "https://example/activate", ExpiresIn: 60, Interval: 0})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if f.polls.Add(1) <= f.pollsUntilAuthorized {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"status":400,"message":"authorization_pending"}`))
				return
			}
			f.validToken.Store("device-access")
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "device-access", RefreshToken: "device-refresh", ExpiresIn: 14000})
		case "refresh_token":
			if !f.refreshValid {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"status":400,"message":"Invalid refresh token"}`))
				return
			}
			n := f.refreshes.Add(1)
			access := fmt.Sprintf("access-%d", n)
			f.validToken.Store(access)
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresIn: 14000})
		}
	})
	mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
		if valid, _ := f.validToken.Load().(string); r.Header.Get("Authorization") != "OAuth "+valid {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(validateResponse{Login: "mod", UserID: "42", ExpiresIn: 14000})
	})
	return mux
}

func setupFakeTwitch(t *testing.T, f *fakeTwitch) string {
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	oldDevice, oldToken, oldValidate := twitchDeviceURL, twitchTokenURL, twitchValidateURL
	twitchDeviceURL, twitchTokenURL, twitchValidateURL = srv.URL+"/device", srv.URL+"/token", srv.URL+"/validate"
	t.Cleanup(func() { twitchDeviceURL, twitchTokenURL, twitchValidateURL = oldDevice, oldToken, oldValidate })

	return filepath.Join(t.TempDir(), "token.json")
}

func readTokenFile(t *testing.T, file string) storedToken {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var tok storedToken
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestDeviceFlowWithoutTokenFile(t *testing.T) {
	f := &fakeTwitch{pollsUntilAuthorized: 2}
	file := setupFakeTwitch(t, f)

	m := NewTokenManager("client", "", file)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}

	if m.AccessToken() != "device-access" || m.UserID() != "42" {
		t.Errorf("unexpected token %q / user %q", m.AccessToken(), m.UserID())
	}
	if f.polls.Load() != 3 {
		t.Errorf("expected 3 polls, got %d", f.polls.Load())
	}
	if tok := readTokenFile(t, file); tok.RefreshToken != "device-refresh" {
		t.Errorf("refresh token not stored: %+v", tok)
	}
}

func TestInitRefreshesExpiredToken(t *testing.T) {
	f := &fakeTwitch{refreshValid: true}
	file := setupFakeTwitch(t, f)
	f.validToken.Store("other")
	data, _ := json.Marshal(storedToken{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)})
	os.WriteFile(file, data, 0600)

	m := NewTokenManager("client", "", file)
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	if m.AccessToken() != "access-1" {
		t.Errorf("expected refreshed token, got %q", m.AccessToken())
	}
	if tok := readTokenFile(t, file); tok.RefreshToken != "refresh-1" || tok.UserID != "42" {
		t.Errorf("new refresh token not stored: %+v", tok)
	}
}

func TestInvalidRefreshTokenStartsDeviceFlow(t *testing.T) {
	f := &fakeTwitch{}
	file := setupFakeTwitch(t, f)
	f.validToken.Store("other")
	data, _ := json.Marshal(storedToken{AccessToken: "old", RefreshToken: "revoked"})
	os.WriteFile(file, data, 0600)

	reauth := false
	m := NewTokenManager("client", "", file)
	m.OnReauth = func() { reauth = true }
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.AccessToken() != "device-access" {
		t.Errorf("expected token from device flow, got %q", m.AccessToken())
	}
	if !reauth {
		t.Errorf("OnReauth was not called")
	}
}

func TestRunRefreshesBeforeExpiry(t *testing.T) {
	f := &fakeTwitch{refreshValid: true}
	file := setupFakeTwitch(t, f)

	m := NewTokenManager("client", "", file)
	m.tok = storedToken{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(refreshBeforeExpiry - time.Second)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for m.AccessToken() != "access-1" {
		if time.Now().After(deadline) {
			t.Fatalf("token was not refreshed, got %q", m.AccessToken())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
