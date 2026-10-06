# Quai Modlog - twitch.tv/quai1

this programm is sending discord notifications for modactions via a discord webhook.


## implemented modactions

- timeout
- untimeout
- ban
- unban
- mod
- unmod
- vip
- unvip
- add blocked term
- remove blocked term
- add permitted term
- remove permitted term
- warn
- raid

## Requirements

- clientId from [Twitch Developer Console](https://dev.twitch.tv/console)
- mod or broadcaster in some channels

## Receive Token

The token is obtained via the [Device Code Flow](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/#device-code-grant-flow)
and refreshed automatically.

- create an Application in the [Twitch Developer Console](https://dev.twitch.tv/console)
  - redirect url can be anything, e.g. `http://localhost`
  - client type `Public` needs only the clientId, client type `Confidential` also needs the client secret (`twitch.clientsecret`)
- put your clientId in `config.yml`
- start the bot, it logs a url and a code (in docker: `docker compose logs -f`)

  ```
  AUTH: open https://www.twitch.tv/activate and enter code ABCD-EFGH
  ```
- open the url with the moderator account and confirm the code
- the token is stored in `token.json` next to the config (or `twitch.tokenfile`), on the next start no new code is needed
- the token is validated every hour and refreshed before it expires. If the authorization is revoked, a new code is logged.
- following scopes are requested for eventsub type `channel.moderate v2`
  - moderator:read:blocked_terms
  - moderator:read:chat_settings
  - moderator:read:unban_requests
  - moderator:read:banned_users
  - moderator:read:chat_messages
  - moderator:read:moderators
  - moderator:read:vips
  - moderator:read:warnings

## Config

- copy `config.yml.example` to `config.yml`
- put your clientid in `config.yml`
- add one entry per channel with the broadcaster `userid` and the discord `webhook` of that channel

## Discord rate limit

Every webhook has its own queue. Up to 10 modactions are combined into one discord message,
the rate limit headers of discord are respected and on `429` the message is sent again after `retry_after`.

## Logging

Set `LOG_LEVEL=debug` to log keepalive and raw notification messages.

## running in docker

- execute `docker compose up -d` to start it in a container
- the container user must be able to write to `./config` for `token.json`

## Used Librarys

- `github.com/nil-go/konf`
- `github.com/typical-developers/discord-webhooks-go`
- `github.com/joeyak/go-twitch-eventsub/v3`

## future (possible) features

- include/exclude some modactions
- ignore modactions from specific users