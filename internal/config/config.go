package config

import (
	"flag"
	"log"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIProxy struct {
		BaseURL string            `yaml:"base_url" env-required:"true"`
		Timeout time.Duration     `yaml:"timeout" env-default:"30s"`
		Headers map[string]string `yaml:"headers"`
		Auth    struct {
			Token string `yaml:"token"`
			Type  string `yaml:"type" env-default:"Bearer"`
		} `yaml:"auth"`
	} `yaml:"api_proxy" env-required:"true"`

	WebSocket struct {
		Enabled   bool   `yaml:"enabled" env-default:"false"`
		URL       string `yaml:"url" env-default:""`
		ClientID  string `yaml:"client_id" env-default:"socket-proxy-client"`
		Protocol  string `yaml:"protocol" env-default:"websocket"` // "websocket" or "tcp"
		Reconnect struct {
			Enabled           bool          `yaml:"enabled" env-default:"true"`
			MaxAttempts       int           `yaml:"max_attempts" env-default:"5"`
			InitialDelay      time.Duration `yaml:"initial_delay" env-default:"5s"`
			MaxDelay          time.Duration `yaml:"max_delay" env-default:"60s"`
			BackoffMultiplier float64       `yaml:"backoff_multiplier" env-default:"2"`
		} `yaml:"reconnect"`
	} `yaml:"websocket"  env-required:"true"`

	QuickCommands map[string]interface{} `yaml:"quick_commands"`

	EnabledCommands struct {
		APICall     bool `yaml:"api_call" env-default:"true"`
		HTTPRequest bool `yaml:"http_request" env-default:"true"`
		SSHCommand  bool `yaml:"ssh_command" env-default:"true"`
	} `yaml:"enabled_commands"`

	Logging struct {
		Level  string `yaml:"level" env-default:"info"`
		Format string `yaml:"format" env-default:"text"`
		File   string `yaml:"file"`
	} `yaml:"logging"`
}

type Logging struct {
	Level  string `yaml:"level" env-default:"info"`
	Format string `yaml:"format" env-default:"text"`
	File   string `yaml:"file"`
}

var instance *Config
var once sync.Once
var configFile string

func init() {
	flag.StringVar(&configFile, "config", "config.yml", "Path to configuration file")
}

func GetConfig() *Config {
	once.Do(func() {
		instance = &Config{}
		loadConfig()
	})
	return instance
}

func loadConfig() {
	// Check if config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Config file %s not found, using defaults", configFile)
	} else {
		data, err := os.ReadFile(configFile)
		if err != nil {
			log.Printf("Error reading config file: %v", err)
			return
		}

		if err := yaml.Unmarshal(data, instance); err != nil {
			log.Printf("Error parsing YAML config: %v", err)
			return
		}
	}
}
