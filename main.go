package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joeyak/go-twitch-eventsub/v3"
)

// Variables
var (
	configPath = os.Getenv("CONFIG_PATH")
	debug      = strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug")
)

const (
	reconnectMaxBackoff = time.Minute
	shutdownTimeout     = 10 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Read config
	config, errConf := ReadConfiguration(configPath)
	if errConf != nil {
		log.Fatalf("could not read config: %v", errConf)
	}
	if config.Twitch.Clientid == "" {
		log.Fatalf("twitch.clientid is missing in config")
	}

	// Broadcaster user id -> discord webhook
	webhookURLs := webhooksByUserID(config)
	if len(webhookURLs) == 0 {
		log.Fatalf("no channel with userid and discord webhook in config")
	}

	// Token
	reconnect := make(chan struct{}, 1)
	tokens := NewTokenManager(config.Twitch.Clientid, config.Twitch.Clientsecret, config.Twitch.Tokenfile)
	tokens.OnReauth = func() {
		// new authorization means the old subscriptions are gone, so reconnect and subscribe again
		select {
		case reconnect <- struct{}{}:
		default:
		}
	}
	if err := tokens.Init(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Fatalf("could not get twitch token: %v", err)
	}
	if config.Twitch.Userid != "" && config.Twitch.Userid != tokens.UserID() {
		log.Printf("WARNING: twitch.userid %s does not match the authorized user %s, using %s", config.Twitch.Userid, tokens.UserID(), tokens.UserID())
	}
	go tokens.Run(ctx)

	sender := NewDiscordSender()

	runEventSub(ctx, tokens, sender, webhookURLs, reconnect)

	log.Printf("shutting down, sending remaining messages")
	sender.Close(shutdownTimeout)
}

// Keep the eventsub websocket connected, reconnect with backoff
func runEventSub(ctx context.Context, tokens *TokenManager, sender *DiscordSender, webhookURLs map[string]string, reconnect <-chan struct{}) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := connectEventSub(ctx, tokens, sender, webhookURLs, reconnect)
		if ctx.Err() != nil {
			return
		}

		if time.Since(start) > reconnectMaxBackoff {
			backoff = time.Second
		}
		log.Printf("EVENTSUB: connection closed (%v), reconnecting in %s", err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, reconnectMaxBackoff)
	}
}

// One websocket session, returns when the connection is lost
func connectEventSub(ctx context.Context, tokens *TokenManager, sender *DiscordSender, webhookURLs map[string]string, reconnect <-chan struct{}) error {
	connCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var lastMessage, keepaliveTimeout atomic.Int64
	touch := func() { lastMessage.Store(time.Now().UnixNano()) }
	touch()

	// Init client
	client := twitch.NewClient()

	client.OnError(func(err error) {
		log.Printf("EVENTSUB ERROR: %v", err)
	})
	client.OnWelcome(func(message twitch.WelcomeMessage) {
		touch()
		keepaliveTimeout.Store(int64(message.Payload.Session.KeepaliveTimeoutSeconds))
		log.Printf("EVENTSUB: connected, session %s", message.Payload.Session.ID)

		subscribeAll(connCtx, tokens, message.Payload.Session.ID, webhookURLs)
	})

	// Notification Message from Websocketserver
	client.OnNotification(func(message twitch.NotificationMessage) {
		touch()
		if debug {
			log.Printf("NOTIFICATION: %s: %s", message.Payload.Subscription.Type, message.Payload.Event)
		}
	})

	// Keep Alive Message from Websocketserver
	client.OnKeepAlive(func(message twitch.KeepAliveMessage) {
		touch()
		if debug {
			log.Printf("KEEPALIVE: %v", message.Metadata.MessageTimestamp)
		}
	})

	// Reconnect Message from Websocketserver, handled by the library
	client.OnReconnect(func(message twitch.ReconnectMessage) {
		touch()
		log.Printf("EVENTSUB: server requested reconnect")
	})

	// Revoke Message from Websocketserver
	client.OnRevoke(func(message twitch.RevokeMessage) {
		touch()
		sub := message.Payload.Subscription
		log.Printf("EVENTSUB: subscription %s for %s revoked: %s", sub.Type, sub.Condition["broadcaster_user_id"], sub.Status)
		if sub.Status == "authorization_revoked" {
			// refresh fails if the authorization was revoked and starts the device code flow
			if err := tokens.Refresh(ctx); err != nil {
				log.Printf("AUTH ERROR: %v", err)
			}
		}
	})

	// Channel Moderation Event Message from Websocketserver
	client.OnEventChannelModerate(func(message twitch.EventChannelModerate) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("ERROR: could not handle %s event: %v", message.Action, r)
			}
		}()

		webhookURL, ok := webhookURLs[message.BroadcasterUserId]
		if !ok {
			log.Printf("ERROR: no discord webhook for channel %s (%s)", message.BroadcasterUserLogin, message.BroadcasterUserId)
			return
		}
		log.Printf("EVENT: %s in %s by %s", message.Action, message.BroadcasterUserLogin, message.ModeratorUserLogin)
		sender.Enqueue(webhookURL, buildEmbed(message))
	})

	// Watchdog: twitch sends a keepalive at least every keepalive_timeout_seconds
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-reconnect:
				cancel(errors.New("reconnect after new authorization"))
				return
			case <-ticker.C:
				timeout := time.Duration(keepaliveTimeout.Load())*time.Second + 10*time.Second
				if time.Since(time.Unix(0, lastMessage.Load())) > timeout {
					cancel(errors.New("keepalive timeout"))
					return
				}
			}
		}
	}()

	err := client.ConnectWithContext(connCtx)
	_ = client.Close()
	if err == nil {
		err = context.Cause(connCtx)
	}
	if err == nil {
		err = errors.New("connection closed")
	}
	return err
}

// Subscribe channel.moderate for all channels, a failing channel does not stop the others
func subscribeAll(ctx context.Context, tokens *TokenManager, sessionID string, webhookURLs map[string]string) {
	for broadcaster := range webhookURLs {
		err := subscribe(ctx, tokens, sessionID, broadcaster)
		if err != nil && strings.Contains(err.Error(), "401") {
			log.Printf("EVENTSUB: subscribe unauthorized, refreshing token")
			if errRefresh := tokens.Refresh(ctx); errRefresh != nil {
				log.Printf("AUTH ERROR: %v", errRefresh)
			}
			err = subscribe(ctx, tokens, sessionID, broadcaster)
		}
		if err != nil {
			log.Printf("EVENTSUB ERROR: could not subscribe for channel %s: %v", broadcaster, err)
			continue
		}
		log.Printf("EVENTSUB: subscribed to %s for channel %s", twitch.SubChannelModerate, broadcaster)
	}
}

func subscribe(ctx context.Context, tokens *TokenManager, sessionID string, broadcaster string) error {
	_, err := twitch.SubscribeEventWithContext(ctx, twitch.SubscribeRequest{
		SessionID:   sessionID,
		ClientID:    tokens.clientID,
		AccessToken: tokens.AccessToken(),
		Event:       twitch.SubChannelModerate,
		Condition: map[string]string{
			"broadcaster_user_id": broadcaster,
			"moderator_user_id":   tokens.UserID(),
		},
	})
	return err
}
