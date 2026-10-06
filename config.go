package main

import (
	"path/filepath"

	"github.com/nil-go/konf"
	"github.com/nil-go/konf/provider/env"
	"github.com/nil-go/konf/provider/file"
	"gopkg.in/yaml.v3"
)

// Struct for config
type Config struct {
	Twitch struct {
		Userid       string `yaml:"userid"`
		Clientid     string `yaml:"clientid"`
		Clientsecret string `yaml:"clientsecret"`
		Tokenfile    string `yaml:"tokenfile"`
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

	// Token file defaults to token.json next to the config file
	if res.Twitch.Tokenfile == "" {
		res.Twitch.Tokenfile = filepath.Join(filepath.Dir(configPath), "token.json")
	}
	return res, nil
}

// Map broadcaster user id to discord webhook url
func webhooksByUserID(config Config) map[string]string {
	res := map[string]string{}
	for _, c := range config.Modlog.Channel {
		if c.Userid != "" && c.Discord.Webhook != "" {
			res[c.Userid] = c.Discord.Webhook
		}
	}
	return res
}
