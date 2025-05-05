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

- access Token and clientId from [Twitch Developer Console](https://dev.twitch.tv/console)
- mod or broadcaster in some channels

## Receive Token

- create an Application [Twitch Developer Console](https://dev.twitch.tv/console) with redirect url http://localhost and receive your clientid
- put your clientId in here`https://id.twitch.tv/oauth2/authorize?response_type=token&client_id=<ClientId>&redirect_uri=http://localhost&scope=moderator%3Aread%3Ablocked_terms%20moderator%3Aread%3Achat_settings%20moderator%3Aread%3Aunban_requests%20moderator%3Aread%3Abanned_users%20moderator%3Aread%3Achat_messages%20moderator%3Aread%3Amoderators%20moderator%3Aread%3Avips%20moderator%3Aread%3Awarnings`
- open the url and get the access token from the url parameter `access_token`
  - the url should look something like that `http://localhost/#access_token=<your_access_token>&scope=moderator%3Aread%3Ablocked_terms+moderator%3Aread%3Achat_settings+moderator%3Aread%3Aunban_requests+moderator%3Aread%3Abanned_users+moderator%3Aread%3Achat_messages+moderator%3Aread%3Amoderators+moderator%3Aread%3Avips+moderator%3Aread%3Awarnings&token_type=bearer`
- following permissions are received by the token for eventsub type `channel.moderate v2`
  - moderator:read:blocked_terms
  - moderator:read:chat_settings
  - moderator:read:unban_requests
  - moderator:read:banned_users
  - moderator:read:chat_messages
  - moderator:read:moderators
  - moderator:read:vips

## Config

- copy `config.yml.example` to `config.yml`
- put your clientid and access token in `config.yml`
- fill out the other fields in `config.yml`

## running in docker

- execute `docker compose up -d` to start it in a container

## Used Librarys

- `github.com/nil-go/konf`
- `github.com/typical-developers/discord-webhooks-go`
- `github.com/joeyak/go-twitch-eventsub/v3`

## future (possible) features

- include/exclude some modactions
- ignore modactions from specific users