package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/typical-developers/discord-webhooks-go/webhooks"
)

const (
	discordMaxEmbeds      = 10   // embeds per message
	discordMaxEmbedChars  = 6000 // characters of all embeds per message
	discordQueueSize      = 1000
	discordMaxRetries     = 5
	discordMessagesPerMin = 30 // webhook messages per minute per channel
	discordBatchWait      = 500 * time.Millisecond
)

// DiscordSender sends embeds to discord webhooks, one queue and worker per webhook
type DiscordSender struct {
	httpClient *http.Client
	mu         sync.Mutex
	queues     map[string]*webhookQueue
	wg         sync.WaitGroup

	// minimum interval between two messages to one webhook
	minInterval time.Duration
}

type webhookQueue struct {
	url    string
	embeds chan *webhooks.DiscordEmbed
}

func NewDiscordSender() *DiscordSender {
	return &DiscordSender{
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		queues:      map[string]*webhookQueue{},
		minInterval: time.Minute / discordMessagesPerMin,
	}
}

// Enqueue embed for webhook, never blocks
func (s *DiscordSender) Enqueue(webhookURL string, embed *webhooks.DiscordEmbed) {
	s.mu.Lock()
	q, ok := s.queues[webhookURL]
	if !ok {
		q = &webhookQueue{url: webhookURL, embeds: make(chan *webhooks.DiscordEmbed, discordQueueSize)}
		s.queues[webhookURL] = q
		s.wg.Add(1)
		go s.worker(q)
	}
	s.mu.Unlock()

	select {
	case q.embeds <- embed:
	default:
		log.Printf("DISCORD ERROR: queue full, dropping embed %q", ptrValue(embed.Title))
	}
}

// Close stops accepting embeds and waits until the queues are sent or the timeout is reached
func (s *DiscordSender) Close(timeout time.Duration) {
	s.mu.Lock()
	for _, q := range s.queues {
		close(q.embeds)
	}
	s.queues = map[string]*webhookQueue{}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("DISCORD ERROR: shutdown timeout, some embeds were not sent")
	}
}

func (s *DiscordSender) worker(q *webhookQueue) {
	defer s.wg.Done()

	var pending *webhooks.DiscordEmbed // embed which did not fit into the last batch
	var nextSend time.Time

	for {
		batch := []*webhooks.DiscordEmbed{}
		chars := 0
		if pending != nil {
			batch = append(batch, pending)
			chars = embedLength(pending)
			pending = nil
		} else {
			embed, ok := <-q.embeds
			if !ok {
				return
			}
			batch = append(batch, embed)
			chars = embedLength(embed)
		}

		// collect more embeds which arrived meanwhile or within a short time
		closed := false
		timer := time.NewTimer(max(discordBatchWait, time.Until(nextSend)))
	collect:
		for len(batch) < discordMaxEmbeds {
			select {
			case embed, ok := <-q.embeds:
				if !ok {
					closed = true
					break collect
				}
				if l := embedLength(embed); chars+l > discordMaxEmbedChars {
					pending = embed
					break collect
				} else {
					chars += l
				}
				batch = append(batch, embed)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()

		if wait := time.Until(nextSend); wait > 0 {
			time.Sleep(wait)
		}

		nextSend = s.sendBatch(q.url, batch)

		if closed {
			if pending != nil {
				s.sendBatch(q.url, []*webhooks.DiscordEmbed{pending})
			}
			return
		}
	}
}

// Send batch and return the earliest time for the next message.
// If discord rejects a batch, the embeds are sent one by one so only the invalid one is lost.
func (s *DiscordSender) sendBatch(webhookURL string, batch []*webhooks.DiscordEmbed) time.Time {
	resetAfter, err := s.send(webhookURL, batch)
	if err == nil {
		return time.Now().Add(max(s.minInterval, resetAfter))
	}
	if _, rejected := err.(rejectedError); !rejected || len(batch) == 1 {
		log.Printf("DISCORD ERROR: could not send %d embeds: %v", len(batch), err)
		return time.Now().Add(max(s.minInterval, resetAfter))
	}

	log.Printf("DISCORD: batch rejected (%v), sending embeds one by one", err)
	next := time.Now().Add(max(s.minInterval, resetAfter))
	for _, embed := range batch {
		time.Sleep(time.Until(next))
		next = s.sendBatch(webhookURL, []*webhooks.DiscordEmbed{embed})
	}
	return next
}

// Discord rejected the request (4xx other than 429)
type rejectedError struct {
	status int
	body   []byte
}

func (e rejectedError) Error() string {
	return fmt.Sprintf("status %d: %s", e.status, e.body)
}

// Send batch, retries on rate limit and server errors.
// Returns how long to wait before the next request when the bucket is exhausted.
func (s *DiscordSender) send(webhookURL string, batch []*webhooks.DiscordEmbed) (time.Duration, error) {
	body, err := json.Marshal(webhooks.WebhookPayload{Embeds: batch})
	if err != nil {
		return 0, err
	}

	backoff := time.Second
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			if attempt >= discordMaxRetries {
				return 0, err
			}
			log.Printf("DISCORD: request failed (%v), retrying in %s", err, backoff)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			// rate limits are not counted as failed attempts, nothing is dropped
			wait := retryAfter(resp, respBody)
			log.Printf("DISCORD: rate limited (global=%s), waiting %s", resp.Header.Get("X-RateLimit-Global"), wait)
			time.Sleep(wait)
			attempt--
			continue
		case resp.StatusCode >= 500:
			if attempt >= discordMaxRetries {
				return 0, fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
			}
			log.Printf("DISCORD: server error %d, retrying in %s", resp.StatusCode, backoff)
			time.Sleep(backoff)
			backoff *= 2
			continue
		case resp.StatusCode >= 300:
			return bucketWait(resp), rejectedError{status: resp.StatusCode, body: respBody}
		}
		return bucketWait(resp), nil
	}
}

// Time to wait if the rate limit bucket is exhausted
func bucketWait(resp *http.Response) time.Duration {
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return 0
	}
	return parseSeconds(resp.Header.Get("X-RateLimit-Reset-After"))
}

// Time to wait after a 429 response
func retryAfter(resp *http.Response, body []byte) time.Duration {
	var rl struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &rl) == nil && rl.RetryAfter > 0 {
		return time.Duration(rl.RetryAfter * float64(time.Second))
	}
	if d := parseSeconds(resp.Header.Get("Retry-After")); d > 0 {
		return d
	}
	if d := parseSeconds(resp.Header.Get("X-RateLimit-Reset-After")); d > 0 {
		return d
	}
	return time.Second
}

func parseSeconds(v string) time.Duration {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

// Number of characters counting towards discords embed limit
func embedLength(e *webhooks.DiscordEmbed) int {
	l := len([]rune(ptrValue(e.Title))) + len([]rune(ptrValue(e.Description)))
	if e.Footer != nil {
		l += len([]rune(ptrValue(e.Footer.Text)))
	}
	if e.Author != nil {
		l += len([]rune(ptrValue(e.Author.Name)))
	}
	for _, f := range e.Fields {
		l += len([]rune(f.Name)) + len([]rune(f.Value))
	}
	return l
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
