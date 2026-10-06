package main

import (
	"testing"

	"github.com/joeyak/go-twitch-eventsub/v3"
)

func TestHandlersWithMissingData(t *testing.T) {
	for action := range eventHandlers {
		embed := buildEmbed(twitch.EventChannelModerate{Action: action})
		if embed == nil {
			t.Errorf("%s: no embed", action)
		}
	}
	for _, action := range []string{"shared_chat_ban", "shared_chat_timeout", "unknown_action"} {
		buildEmbed(twitch.EventChannelModerate{Action: action})
	}
}

func TestHandleTermsWithEmptyTerms(t *testing.T) {
	embed := buildEmbed(twitch.EventChannelModerate{Action: "add_blocked_term", AutomodTerms: &twitch.AutomodTerms{}})
	for _, f := range embed.Fields {
		if f.Value == "" {
			t.Errorf("field %s has empty value", f.Name)
		}
	}
}

func TestSharedChatBan(t *testing.T) {
	embed := buildEmbed(twitch.EventChannelModerate{
		Action:        "shared_chat_ban",
		SharedChatBan: &twitch.Ban{User: twitch.User{UserLogin: "someone", UserName: "Someone"}, Reason: "spam"},
	})
	if got := ptrValue(embed.Title); got != "[BAN] [SHARED CHAT] Twitch Modlog EventSub" {
		t.Errorf("unexpected title %q", got)
	}
}

func TestAddFieldTruncates(t *testing.T) {
	embed := buildEmbed(twitch.EventChannelModerate{Action: "ban"})
	addField(embed, "Long", string(make([]rune, 2000)), false)
	last := embed.Fields[len(embed.Fields)-1]
	if l := len([]rune(last.Value)); l > discordMaxFieldValue {
		t.Errorf("field value has %d characters", l)
	}
}
