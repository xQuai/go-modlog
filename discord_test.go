package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/typical-developers/discord-webhooks-go/webhooks"
)

type fakeWebhook struct {
	mu       sync.Mutex
	requests []webhooks.WebhookPayload
	times    []time.Time
	handler  func(n int, w http.ResponseWriter) bool // returns true if it handled the response
}

func (f *fakeWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p webhooks.WebhookPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, p)
	f.times = append(f.times, time.Now())
	f.mu.Unlock()

	if f.handler != nil && f.handler(n, w) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *fakeWebhook) embedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := 0
	for _, r := range f.requests {
		c += len(r.Embeds)
	}
	return c
}

func testEmbed(i int) *webhooks.DiscordEmbed {
	e := &webhooks.DiscordEmbed{}
	e.SetTitle(fmt.Sprintf("embed %d", i))
	return e
}

func newTestSender() *DiscordSender {
	s := NewDiscordSender()
	s.minInterval = 0
	return s
}

func TestDiscordRetriesOnRateLimit(t *testing.T) {
	fake := &fakeWebhook{handler: func(n int, w http.ResponseWriter) bool {
		if n == 0 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"You are being rate limited.","retry_after":0.2,"global":false}`))
			return true
		}
		return false
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	s := newTestSender()
	s.Enqueue(srv.URL, testEmbed(1))
	s.Close(5 * time.Second)

	if len(fake.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(fake.requests))
	}
	if wait := fake.times[1].Sub(fake.times[0]); wait < 200*time.Millisecond {
		t.Errorf("retry was sent after %s, expected at least 200ms", wait)
	}
	if ptrValue(fake.requests[1].Embeds[0].Title) != "embed 1" {
		t.Errorf("retry did not contain the same embed")
	}
}

func TestDiscordBatchesEmbeds(t *testing.T) {
	fake := &fakeWebhook{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	s := newTestSender()
	for i := 0; i < 25; i++ {
		s.Enqueue(srv.URL, testEmbed(i))
	}
	s.Close(5 * time.Second)

	if len(fake.requests) != 3 {
		t.Errorf("expected 3 requests, got %d", len(fake.requests))
	}
	for i, r := range fake.requests {
		if len(r.Embeds) > discordMaxEmbeds {
			t.Errorf("request %d has %d embeds", i, len(r.Embeds))
		}
	}
	if c := fake.embedCount(); c != 25 {
		t.Errorf("expected 25 embeds, got %d", c)
	}
}

func TestDiscordBatchRespectsCharacterLimit(t *testing.T) {
	fake := &fakeWebhook{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	s := newTestSender()
	for i := 0; i < 4; i++ {
		e := testEmbed(i)
		e.SetDescription(string(make([]rune, 2000)))
		s.Enqueue(srv.URL, e)
	}
	s.Close(5 * time.Second)

	if c := fake.embedCount(); c != 4 {
		t.Errorf("expected 4 embeds, got %d", c)
	}
	for i, r := range fake.requests {
		chars := 0
		for _, e := range r.Embeds {
			chars += embedLength(e)
		}
		if chars > discordMaxEmbedChars {
			t.Errorf("request %d has %d characters", i, chars)
		}
	}
}

func TestDiscordWaitsForBucketReset(t *testing.T) {
	fake := &fakeWebhook{handler: func(n int, w http.ResponseWriter) bool {
		if n == 0 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset-After", "0.5")
		}
		return false
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	s := newTestSender()
	s.Enqueue(srv.URL, testEmbed(1))
	time.Sleep(discordBatchWait + 200*time.Millisecond) // first message is sent
	s.Enqueue(srv.URL, testEmbed(2))
	s.Close(5 * time.Second)

	if len(fake.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(fake.requests))
	}
	if wait := fake.times[1].Sub(fake.times[0]); wait < 500*time.Millisecond {
		t.Errorf("second request was sent after %s, expected at least 500ms", wait)
	}
}

func TestDiscordRejectedBatchIsSentOneByOne(t *testing.T) {
	fake := &fakeWebhook{handler: func(n int, w http.ResponseWriter) bool {
		if n == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		return false
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	s := newTestSender()
	for i := 0; i < 3; i++ {
		s.Enqueue(srv.URL, testEmbed(i))
	}
	s.Close(5 * time.Second)

	// 1 rejected batch + 3 single messages
	if len(fake.requests) != 4 {
		t.Errorf("expected 4 requests, got %d", len(fake.requests))
	}
}
