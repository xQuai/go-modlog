package main

import (
	"fmt"
	"github.com/joeyak/go-twitch-eventsub/v3"
	"github.com/nil-go/konf"
	"github.com/nil-go/konf/provider/env"
	"github.com/nil-go/konf/provider/file"
	"github.com/typical-developers/discord-webhooks-go/webhooks"
	"gopkg.in/yaml.v3"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables
var (
	broadcasters = []string{}
	configPath   = os.Getenv("CONFIG_PATH")
)

const (
	twitchUserCardURL = "[%s](https://twitch.tv/popout/%s/viewercard/%s)"
	twitchChannelURL  = "[%s](https://twitch.tv/%s)"
)

// Struct for config
type Config struct {
	Twitch struct {
		Userid      string `yaml:"userid"`
		Clientid    string `yaml:"clientid"`
		Accesstoken string `yaml:"accesstoken"`
	} `yaml:"twitch"`
	Modlog struct {
		Channel []struct {
			Name    string `yaml:"name"`
			Userid  string `yaml:"userid"`
			Discord struct {
				Webhook string `yaml:"webhook"`
			} `yaml:"discord"`
		} `yaml:"channel"`
	} `yaml:"modlog"`
}

func main() {

	// Read config
	config, errConf := ReadConfiguration(configPath)
	if errConf != nil {
		panic(errConf)
	}

	// Add channels from config to broadcasters
	broadcasters := addChannels(config, broadcasters)

	// Init client
	client := twitch.NewClient()

	client.OnError(func(err error) {
		fmt.Printf("ERROR: %v\n", err)
	})
	client.OnWelcome(func(message twitch.WelcomeMessage) {
		fmt.Printf("WELCOME: %v\n", message)

		// Subscribe to events
		events := []twitch.EventSubscription{
			twitch.SubChannelModerate,
		}

		for _, event := range events {
			for _, broadcaster := range broadcasters {
				fmt.Printf("subscribing to %s for streamer %s\n", event, broadcaster)
				_, err := twitch.SubscribeEvent(twitch.SubscribeRequest{
					SessionID:   message.Payload.Session.ID,
					ClientID:    config.Twitch.Clientid,
					AccessToken: config.Twitch.Accesstoken,
					Event:       event,
					Condition: map[string]string{
						"broadcaster_user_id": broadcaster,
						"moderator_user_id":   config.Twitch.Userid,
					},
				})
				if err != nil {
					fmt.Printf("ERROR: %v\n", err)
					return
				}
			}
		}
	})

	// Notification Message from Websocketserver
	client.OnNotification(func(message twitch.NotificationMessage) {
		fmt.Printf("NOTIFICATION: %s: %#v\n", message.Payload.Subscription.Type, message.Payload.Event)
	})

	// Keep Alive Message from Websocketserver
	client.OnKeepAlive(func(message twitch.KeepAliveMessage) {
		fmt.Printf("KEEPALIVE: %v\n", message)
	})

	// Revoke Message from Websocketserver
	client.OnRevoke(func(message twitch.RevokeMessage) {
		fmt.Printf("REVOKE: %v\n", message)
	})

	// Raw Message from Websocketserver
	client.OnRawEvent(func(event string, metadata twitch.MessageMetadata, subscription twitch.PayloadSubscription) {
		fmt.Printf("EVENT[%s]: %s: %s\n", subscription.Type, metadata, event)
	})

	// Channel Ban Message from Websocketserver
	client.OnEventChannelBan(func(message twitch.EventChannelBan) {
		fmt.Printf("EVENT[BAN]: %v\n", message)
	})

	// Channel Moderation Event Message from Websocketserver
	client.OnEventChannelModerate(func(message twitch.EventChannelModerate) {

		webhook := webhooks.NewWebhookClientFromURL(getDiscordWebHookUrl(config, strings.ToLower(message.BroadcasterUserName)))
		payload := webhooks.WebhookPayload{}

		embed := createBaseEmbed(message.Action, message)
		if handler, exists := eventHandlers[message.Action]; exists {
			handler(message, embed)
		} else {
			handleDefault(message, embed)
		}
		payload.Embeds = append(payload.Embeds, embed)

		// Send Message to Discord
		_, err := webhook.SendMessage(&payload)
		if err != nil {
			fmt.Printf("Error sending webhook: %v\n", err.Error())
		}

	})

	err := client.Connect()
	if err != nil {
		fmt.Printf("Could not connect client: %v\n", err)
	}
}

// Read Configuration from file
func ReadConfiguration(configPath string) (Config, error) {
	var config konf.Config

	err := config.Load(file.New(configPath, file.WithUnmarshal(yaml.Unmarshal)))
	if err != nil {
		return Config{}, err
	}

	err = config.Load(env.New())
	if err != nil {
		return Config{}, err
	}

	var res Config

	err = config.Unmarshal("", &res)
	if err != nil {
		return Config{}, err
	}
	return res, nil
}

// Add Channels to array
func addChannels(config Config, channels []string) []string {
	for _, channel := range config.Modlog.Channel {
		if channel.Userid != "" {
			channels = append(channels, channel.Userid)

		}
	}
	return channels
}

// Get Discord Webhook URL by Channel Name
func getDiscordWebHookUrl(config Config, channel string) string {
	for _, c := range config.Modlog.Channel {
		if c.Name == channel {
			return c.Discord.Webhook
		}
	}
	return ""
}

// create common fields
func createBaseEmbed(action string, message twitch.EventChannelModerate) *webhooks.DiscordEmbed {
	embed := &webhooks.DiscordEmbed{}
	embed.SetTitle(fmt.Sprintf("[%s] Twitch Modlog EventSub", strings.ToUpper(action)))
	embed.SetTimestamp(time.Now())

	// Gemeinsame Felder
	embedFieldChannel := embed.AddField()
	embedFieldChannel.SetName("Channel")
	embedFieldChannel.SetValue(fmt.Sprintf(twitchChannelURL, message.BroadcasterUserName, message.BroadcasterUserName))
	embedFieldChannel.SetInline(true)

	embedFieldModerator := embed.AddField()
	embedFieldModerator.SetName("Moderator")
	embedFieldModerator.SetValue(message.ModeratorUserName)
	embedFieldModerator.SetInline(true)

	return embed
}

var eventHandlers = map[string]func(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed){
	"timeout":               handleTimeout,
	"ban":                   handleBan,
	"unban":                 handleUnban,
	"untimeout":             handleUntimeout,
	"delete":                handleDelete,
	"vip":                   handleVip,
	"uncip":                 handleUnVip,
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

func handleDefault(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle(fmt.Sprintf("[DEFAULT] Twitch Modlog EventSub"))

	embedField := embed.AddField()
	embedField.SetName("Event")
	embedField.SetValue(message.Action)
	embedField.SetInline(true)
}

func handleTimeout(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[TIMEOUT] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Timeout.UserName, message.BroadcasterUserName, message.Timeout.UserName))
	embedFieldUser.SetInline(true)
	embedFieldReason := embed.AddField()
	embedFieldReason.SetName("Reason")
	embedFieldReason.SetValue("`" + message.Timeout.Reason + "`")
	embedFieldReason.SetInline(false)
	embedFieldTime := embed.AddField()
	embedFieldTime.SetName("Expires at")
	embedFieldTime.SetValue("<t:" + strconv.FormatInt(message.Timeout.ExpiresAt.Unix(), 10) + ":f>")
	embedFieldTime.SetInline(false)

	duration := message.Timeout.ExpiresAt.Sub(time.Now().Add(-1 * time.Second))
	embedFieldDuration := embed.AddField()
	embedFieldDuration.SetName("Duration")
	embedFieldDuration.SetValue(fmt.Sprintf("%d Seconds", int(duration.Seconds())))
	embedFieldDuration.SetInline(false)
}

func handleBan(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[BAN] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Ban.UserName, message.BroadcasterUserName, message.Ban.UserName))
	embedFieldUser.SetInline(true)
	embedFieldReason := embed.AddField()
	embedFieldReason.SetName("Reason")
	embedFieldReason.SetValue("`" + message.Ban.Reason + "`")
	embedFieldReason.SetInline(false)

}

func handleUnban(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[UNBAN] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Unban.UserName, message.BroadcasterUserName, message.Unban.UserName))
	embedFieldUser.SetInline(true)

}

func handleUntimeout(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[UNTIMEOUT] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Untimeout.UserName, message.BroadcasterUserName, message.Untimeout.UserName))
	embedFieldUser.SetInline(true)

}

func handleDelete(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[DELETE] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Delete.UserName, message.BroadcasterUserName, message.Delete.UserName))
	embedFieldUser.SetInline(true)
	embedFieldMessage := embed.AddField()
	embedFieldMessage.SetName("Message")
	embedFieldMessage.SetValue("`" + message.Delete.MessageBody + "`")
	embedFieldMessage.SetInline(false)
}

func handleVip(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[VIP] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Vip.UserName, message.BroadcasterUserName, message.Vip.UserName))
	embedFieldUser.SetInline(true)

}

func handleUnVip(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[UNVIP] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Unvip.UserName, message.BroadcasterUserName, message.Unvip.UserName))
	embedFieldUser.SetInline(true)

}

func handleMod(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[MOD] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Mod.UserName, message.BroadcasterUserName, message.Mod.UserName))
	embedFieldUser.SetInline(true)

}

func handleUnMod(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[UNMOD] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Unmod.UserName, message.BroadcasterUserName, message.Unmod.UserName))
	embedFieldUser.SetInline(true)

}
func handleAddBlockedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[ADD_BLOCKED_TERM] Twitch Modlog EventSub")

	embedField := embed.AddField()
	embedField.SetName("Action")
	embedField.SetValue(message.AutomodTerms.Action)
	embedField.SetInline(true)
	embedFieldMessage := embed.AddField()
	embedFieldMessage.SetName("Term")
	embedFieldMessage.SetValue("`" + message.AutomodTerms.Terms[0] + "`")
	embedFieldMessage.SetInline(true)
	embedFieldAutomod := embed.AddField()
	embedFieldAutomod.SetName("From Automod")
	embedFieldAutomod.SetValue("`" + strconv.FormatBool(message.AutomodTerms.FromAutomod) + "`")
	embedFieldAutomod.SetInline(true)
}
func handleAddPermittedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[ADD_PERMITTED_TERM] Twitch Modlog EventSub")

	embedField := embed.AddField()
	embedField.SetName("Action")
	embedField.SetValue(message.AutomodTerms.Action)
	embedField.SetInline(true)
	embedFieldMessage := embed.AddField()
	embedFieldMessage.SetName("Term")
	embedFieldMessage.SetValue("`" + message.AutomodTerms.Terms[0] + "`")
	embedFieldMessage.SetInline(true)
	embedFieldAutomod := embed.AddField()
	embedFieldAutomod.SetName("From Automod")
	embedFieldAutomod.SetValue("`" + strconv.FormatBool(message.AutomodTerms.FromAutomod) + "`")
	embedFieldAutomod.SetInline(true)

}

func handleRemovePermittedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[REMOVE_PERMITTED_TERM] Twitch Modlog EventSub")

	embedField := embed.AddField()
	embedField.SetName("Action")
	embedField.SetValue(message.AutomodTerms.Action)
	embedField.SetInline(true)
	embedFieldMessage := embed.AddField()
	embedFieldMessage.SetName("Term")
	embedFieldMessage.SetValue("`" + message.AutomodTerms.Terms[0] + "`")
	embedFieldMessage.SetInline(true)
}

func handleRemoveBlockedTerm(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[REMOVE_BLOCKED_TERM] Twitch Modlog EventSub")

	embedField := embed.AddField()
	embedField.SetName("Action")
	embedField.SetValue(message.AutomodTerms.Action)
	embedField.SetInline(true)
	embedFieldMessage := embed.AddField()
	embedFieldMessage.SetName("Term")
	embedFieldMessage.SetValue("`" + message.AutomodTerms.Terms[0] + "`")
	embedFieldMessage.SetInline(true)
}

func handleWarn(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[WARN] Twitch Modlog EventSub")

	embedFieldUser := embed.AddField()
	embedFieldUser.SetName("User")
	embedFieldUser.SetValue(fmt.Sprintf(twitchUserCardURL, message.Warn.UserName, message.BroadcasterUserName, message.Warn.UserName))
	embedFieldUser.SetInline(true)
	embedFieldReason := embed.AddField()
	embedFieldReason.SetName("Reason")
	embedFieldReason.SetValue("`" + message.Warn.Reason + "`")
	embedFieldReason.SetInline(false)

}

func handleRaid(message twitch.EventChannelModerate, embed *webhooks.DiscordEmbed) {
	embed.SetTitle("[RAID] Twitch Modlog EventSub")

	embedFieldRaidedChannel := embed.AddField()
	embedFieldRaidedChannel.SetName("Raided Channel")
	embedFieldRaidedChannel.SetValue(message.Raid.UserName)
	embedFieldRaidedChannel.SetInline(false)
	embedFieldRaidViewer := embed.AddField()
	embedFieldRaidViewer.SetName("Viewer")
	embedFieldRaidViewer.SetValue(strconv.Itoa(message.Raid.ViewerCount))
	embedFieldRaidViewer.SetInline(false)

}
