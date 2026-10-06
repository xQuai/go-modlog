package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joeyak/go-twitch-eventsub/v3"
	"github.com/typical-developers/discord-webhooks-go/webhooks"
)

const (
	twitchUserCardURL = "[%s](https://twitch.tv/popout/%s/viewercard/%s)"
	twitchChannelURL  = "[%s](https://twitch.tv/%s)"

	discordMaxFieldValue = 1024
)

var eventHandlers = map[string]func(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed){
	"timeout":               handleTimeout,
	"ban":                   handleBan,
	"unban":                 handleUnban,
	"untimeout":             handleUntimeout,
	"delete":                handleDelete,
	"vip":                   handleVip,
	"unvip":                 handleUnVip,
	"mod":                   handleMod,
	"unmod":                 handleUnMod,
	"add_blocked_term":      handleAddBlockedTerm,
	"add_permitted_term":    handleAddPermittedTerm,
	"remove_permitted_term": handleRemovePermittedTerm,
	"remove_blocked_term":   handleRemoveBlockedTerm,
	"warn":                  handleWarn,
	"raid":                  handleRaid,
	"default":               handleDefault,
}

// Build the discord embed for a moderation event
func buildEmbed(message twitch.EventChannelModerate) *webhooks.DiscordEmbed {
	message, sharedChat := normalizeSharedChat(message)

	embed := createBaseEmbed(message)
	if handler, exists := eventHandlers[message.Action]; exists {
		handler(message, embed)
	} else {
		handleDefault(message, embed)
	}

	if sharedChat {
		embed.SetTitle(strings.Replace(ptrValue(embed.Title), "]", "] [SHARED CHAT]", 1))
		addField(embed, "Source Channel", fmt.Sprintf(twitchChannelURL, message.SourceBroadcasterUserName, message.SourceBroadcasterUserLogin), true)
	}
	return embed
}

// Map shared chat actions to the normal actions, so the same handlers can be used
func normalizeSharedChat(message twitch.EventChannelModerate) (twitch.EventChannelModerate, bool) {
	switch message.Action {
	case "shared_chat_ban":
		message.Action, message.Ban = "ban", message.SharedChatBan
	case "shared_chat_unban":
		message.Action, message.Unban = "unban", message.SharedChatUnban
	case "shared_chat_timeout":
		message.Action, message.Timeout = "timeout", message.SharedChatTimeout
	case "shared_chat_untimeout":
		message.Action, message.Untimeout = "untimeout", message.SharedChatuntimeout
	case "shared_chat_delete":
		message.Action, message.Delete = "delete", message.SharedChatDelete
	default:
		return message, false
	}
	return message, true
}

// create common fields
func createBaseEmbed(message twitch.EventChannelModerate) *webhooks.DiscordEmbed {
	embed := &webhooks.DiscordEmbed{}
	embed.SetTitle(fmt.Sprintf("[%s] Twitch Modlog EventSub", strings.ToUpper(message.Action)))
	embed.SetTimestamp(time.Now())

	addField(embed, "Channel", fmt.Sprintf(twitchChannelURL, message.BroadcasterUserName, message.BroadcasterUserLogin), true)
	addField(embed, "Moderator", message.ModeratorUserName, true)

	return embed
}

// Add field, discord rejects empty values and values longer than 1024 characters
func addField(embed *webhooks.DiscordEmbed, name string, value string, inline bool) {
	if strings.Trim(value, "` ") == "" {
		value = "-"
	}
	if r := []rune(value); len(r) > discordMaxFieldValue {
		value = string(r[:discordMaxFieldValue-1]) + "…"
	}
	field := embed.AddField()
	field.SetName(name)
	field.SetValue(value)
	field.SetInline(inline)
}

func addUserField(embed *webhooks.DiscordEmbed, message twitch.EventChannelModerate, user twitch.User) {
	addField(embed, "User", fmt.Sprintf(twitchUserCardURL, user.UserName, message.BroadcasterUserLogin, user.UserLogin), true)
}

func code(value string) string {
	if value == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(value, "`", "'") + "`"
}

func handleDefault(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[DEFAULT] Twitch Modlog EventSub")

	addField(embed, "Event", message.Action, true)
}

func handleTimeout(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	if message.Timeout == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle("[TIMEOUT] Twitch Modlog EventSub")

	addUserField(embed, message, message.Timeout.User)
	addField(embed, "Reason", code(message.Timeout.Reason), false)
	addField(embed, "Expires at", "<t:"+strconv.FormatInt(message.Timeout.ExpiresAt.Unix(), 10)+":f>", false)

	duration := message.Timeout.ExpiresAt.Sub(time.Now().Add(-1 * time.Second))
	addField(embed, "Duration", fmt.Sprintf("%d Seconds", int(duration.Seconds())), false)
}

func handleBan(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	if message.Ban == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle("[BAN] Twitch Modlog EventSub")

	addUserField(embed, message, message.Ban.User)
	addField(embed, "Reason", code(message.Ban.Reason), false)
}

func handleUnban(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "UNBAN", message.Unban)
}

func handleUntimeout(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "UNTIMEOUT", message.Untimeout)
}

func handleVip(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "VIP", message.Vip)
}

func handleUnVip(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "UNVIP", message.Unvip)
}

func handleMod(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "MOD", message.Mod)
}

func handleUnMod(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleUserAction(message, embed, "UNMOD", message.Unmod)
}

// Actions which only have a target user
func handleUserAction(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed, title string, user *twitch.User) {
	if user == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle(fmt.Sprintf("[%s] Twitch Modlog EventSub", title))

	addUserField(embed, message, *user)
}

func handleDelete(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	if message.Delete == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle("[DELETE] Twitch Modlog EventSub")

	addUserField(embed, message, message.Delete.User)
	addField(embed, "Message", code(message.Delete.MessageBody), false)
}

func handleAddBlockedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleTerms(message, embed, "ADD_BLOCKED_TERM", true)
}

func handleAddPermittedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleTerms(message, embed, "ADD_PERMITTED_TERM", true)
}

func handleRemovePermittedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleTerms(message, embed, "REMOVE_PERMITTED_TERM", false)
}

func handleRemoveBlockedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	handleTerms(message, embed, "REMOVE_BLOCKED_TERM", false)
}

// Blocked and permitted terms
func handleTerms(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed, title string, showAutomod bool) {
	if message.AutomodTerms == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle(fmt.Sprintf("[%s] Twitch Modlog EventSub", title))

	terms := make([]string, 0, len(message.AutomodTerms.Terms))
	for _, term := range message.AutomodTerms.Terms {
		terms = append(terms, code(term))
	}

	addField(embed, "Action", message.AutomodTerms.Action, true)
	addField(embed, "Term", strings.Join(terms, ", "), true)
	if showAutomod {
		addField(embed, "From Automod", code(strconv.FormatBool(message.AutomodTerms.FromAutomod)), true)
	}
}

func handleWarn(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	if message.Warn == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle("[WARN] Twitch Modlog EventSub")

	addUserField(embed, message, message.Warn.User)
	addField(embed, "Reason", code(message.Warn.Reason), false)
	if len(message.Warn.ChatRulesCited) > 0 {
		addField(embed, "Chat Rules", strings.Join(message.Warn.ChatRulesCited, "\n"), false)
	}
}

func handleRaid(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	if message.Raid == nil {
		handleDefault(message, embed)
		return
	}
	embed.SetTitle("[RAID] Twitch Modlog EventSub")

	addField(embed, "Raided Channel", message.Raid.UserName, false)
	addField(embed, "Viewer", strconv.Itoa(message.Raid.ViewerCount), false)
}
