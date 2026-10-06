package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Twitch OAuth endpoints (variables so tests can point them to a fake server)
var (
	twitchDeviceURL   = "https://id.twitch.tv/oauth2/device"
	twitchTokenURL    = "https://id.twitch.tv/oauth2/token"
	twitchValidateURL = "https://id.twitch.tv/oauth2/validate"
)

// Scopes needed for eventsub type channel.moderate v2
var twitchScopes = []string{
	"moderator:read:blocked_terms",
	"moderator:read:chat_settings",
	"moderator:read:unban_requests",
	"moderator:read:banned_users",
	"moderator:read:chat_messages",
	"moderator:read:moderators",
	"moderator:read:vips",
	"moderator:read:warnings",
}

const (
	refreshBeforeExpiry = 10 * time.Minute
	validateInterval    = time.Hour
)

// errInvalidGrant means the refresh token is not usable anymore and a new authorization is needed
var errInvalidGrant = errors.New("refresh token is invalid")

// errUnauthorized means the access token is not valid anymore
var errUnauthorized = errors.New("access token is invalid")

// Token as stored in the token file
type storedToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       string    `json:"user_id"`
	Login        string    `json:"login"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type validateResponse struct {
	Login     string `json:"login"`
	UserID    string `json:"user_id"`
	ExpiresIn int    `json:"expires_in"`
}

type deviceResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type twitchError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// TokenManager holds the user access token and keeps it valid
type TokenManager struct {
	clientID     string
	clientSecret string
	file         string
	httpClient   *http.Client

	// OnReauth is called after a new token had to be obtained via device code flow
	OnReauth func()

	authMu sync.Mutex // serializes refresh and device flow, refresh tokens may be single use
	mu     sync.RWMutex
	tok    storedToken
}

func NewTokenManager(clientID, clientSecret, file string) *TokenManager {
	return &TokenManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		file:         file,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Current access token
func (m *TokenManager) AccessToken() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tok.AccessToken
}

// User id of the authorized user (moderator)
func (m *TokenManager) UserID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tok.UserID
}

func (m *TokenManager) expiresAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tok.ExpiresAt
}

// Init loads the token from file and makes sure it is valid, otherwise starts the device code flow
func (m *TokenManager) Init(ctx context.Context) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()

	if err := m.load(); err != nil {
		log.Printf("AUTH: no usable token file (%v), starting device code flow", err)
		return m.deviceFlow(ctx)
	}

	err := m.validate(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errUnauthorized) {
		return err
	}

	err = m.refresh(ctx)
	if errors.Is(err, errInvalidGrant) {
		log.Printf("AUTH: stored refresh token is invalid, starting device code flow")
		return m.deviceFlow(ctx)
	}
	return err
}

// Refresh refreshes the access token, falls back to the device code flow if the refresh token is invalid
func (m *TokenManager) Refresh(ctx context.Context) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	return m.refreshOrReauth(ctx)
}

// Reauth forces a new authorization via device code flow
func (m *TokenManager) Reauth(ctx context.Context) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if err := m.deviceFlow(ctx); err != nil {
		return err
	}
	m.notifyReauth()
	return nil
}

func (m *TokenManager) refreshOrReauth(ctx context.Context) error {
	err := m.refresh(ctx)
	if !errors.Is(err, errInvalidGrant) {
		return err
	}
	log.Printf("AUTH: refresh token is invalid, new authorization required")
	if err := m.deviceFlow(ctx); err != nil {
		return err
	}
	m.notifyReauth()
	return nil
}

func (m *TokenManager) notifyReauth() {
	if m.OnReauth != nil {
		m.OnReauth()
	}
}

// Run keeps the token valid: refreshes before expiry and validates hourly
func (m *TokenManager) Run(ctx context.Context) {
	for {
		wait := validateInterval
		if untilRefresh := time.Until(m.expiresAt()) - refreshBeforeExpiry; untilRefresh < wait {
			wait = max(untilRefresh, 0)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if err := m.maintain(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("AUTH ERROR: %v, retrying in 1 minute", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}
}

func (m *TokenManager) maintain(ctx context.Context) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()

	if time.Until(m.expiresAt()) <= refreshBeforeExpiry {
		return m.refreshOrReauth(ctx)
	}

	err := m.validate(ctx)
	if errors.Is(err, errUnauthorized) {
		return m.refreshOrReauth(ctx)
	}
	return err
}

// Validate token, updates user id and expiry
func (m *TokenManager) validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, twitchValidateURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+m.AccessToken())

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not validate token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not validate token: %s: %s", resp.Status, body)
	}

	var v validateResponse
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("could not parse validate response: %w", err)
	}

	m.mu.Lock()
	m.tok.UserID = v.UserID
	m.tok.Login = v.Login
	if v.ExpiresIn > 0 {
		m.tok.ExpiresAt = time.Now().Add(time.Duration(v.ExpiresIn) * time.Second)
	}
	m.mu.Unlock()
	return m.save()
}

// Refresh access token with refresh token
func (m *TokenManager) refresh(ctx context.Context) error {
	m.mu.RLock()
	refreshToken := m.tok.RefreshToken
	m.mu.RUnlock()
	if refreshToken == "" {
		return errInvalidGrant
	}

	form := url.Values{
		"client_id":     {m.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if m.clientSecret != "" {
		form.Set("client_secret", m.clientSecret)
	}

	status, body, err := m.postForm(ctx, twitchTokenURL, form)
	if err != nil {
		return fmt.Errorf("could not refresh token: %w", err)
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		log.Printf("AUTH: refresh rejected: %s", body)
		return errInvalidGrant
	}
	if status != http.StatusOK {
		return fmt.Errorf("could not refresh token: status %d: %s", status, body)
	}

	if err := m.setToken(body); err != nil {
		return err
	}
	log.Printf("AUTH: token refreshed, valid until %s", m.expiresAt().Format(time.RFC3339))
	return m.validate(ctx)
}

// Device code flow, logs the url and code which has to be confirmed in the browser
func (m *TokenManager) deviceFlow(ctx context.Context) error {
	scopes := strings.Join(twitchScopes, " ")

	for {
		status, body, err := m.postForm(ctx, twitchDeviceURL, url.Values{
			"client_id": {m.clientID},
			"scopes":    {scopes},
		})
		if err != nil {
			return fmt.Errorf("could not start device code flow: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("could not start device code flow: status %d: %s", status, body)
		}

		var dev deviceResponse
		if err := json.Unmarshal(body, &dev); err != nil {
			return fmt.Errorf("could not parse device code response: %w", err)
		}

		log.Printf("AUTH: ==================================================")
		log.Printf("AUTH: open %s and enter code %s", dev.VerificationURI, dev.UserCode)
		log.Printf("AUTH: (the code expires in %d minutes)", dev.ExpiresIn/60)
		log.Printf("AUTH: ==================================================")

		expired, err := m.pollDeviceToken(ctx, dev, scopes)
		if err != nil {
			return err
		}
		if !expired {
			log.Printf("AUTH: authorization successful")
			return m.validate(ctx)
		}
		log.Printf("AUTH: device code expired, requesting a new one")
	}
}

// Poll token endpoint until user confirmed the code. Returns true if the code expired.
func (m *TokenManager) pollDeviceToken(ctx context.Context, dev deviceResponse, scopes string) (bool, error) {
	interval := time.Duration(max(dev.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(interval):
		}

		status, body, err := m.postForm(ctx, twitchTokenURL, url.Values{
			"client_id":   {m.clientID},
			"scopes":      {scopes},
			"device_code": {dev.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		})
		if err != nil {
			log.Printf("AUTH: polling token failed: %v", err)
			continue
		}
		if status == http.StatusOK {
			return false, m.setToken(body)
		}

		var tErr twitchError
		_ = json.Unmarshal(body, &tErr)
		switch tErr.Message {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token", "invalid device code":
			return true, nil
		default:
			return false, fmt.Errorf("device code flow failed: status %d: %s", status, body)
		}
	}
	return true, nil
}

func (m *TokenManager) setToken(body []byte) error {
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return fmt.Errorf("could not parse token response: %w", err)
	}
	if t.AccessToken == "" {
		return fmt.Errorf("token response without access token: %s", body)
	}

	m.mu.Lock()
	m.tok.AccessToken = t.AccessToken
	// always store the new refresh token, public clients get single use refresh tokens
	if t.RefreshToken != "" {
		m.tok.RefreshToken = t.RefreshToken
	}
	m.tok.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	m.mu.Unlock()
	return m.save()
}

func (m *TokenManager) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

func (m *TokenManager) load() error {
	data, err := os.ReadFile(m.file)
	if err != nil {
		return err
	}
	var t storedToken
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		return errors.New("token file is empty")
	}
	m.mu.Lock()
	m.tok = t
	m.mu.Unlock()
	return nil
}

// Write token file atomically
func (m *TokenManager) save() error {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.tok, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(m.file), ".token-*.json")
	if err != nil {
		return fmt.Errorf("could not save token: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("could not save token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not save token: %w", err)
	}
	if err := os.Rename(tmp.Name(), m.file); err != nil {
		return fmt.Errorf("could not save token: %w", err)
	}
	return nil
}
