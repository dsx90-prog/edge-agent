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
	QuickCommands map[string]interface{} `yaml:"quick_commands"`

	APIProxy struct {
		Headers map[string]string `yaml:"headers"`
		Auth    struct {
			Token string `yaml:"token"`
			Type  string `yaml:"type" env-default:"Bearer"`
		} `yaml:"auth"`
		BaseURL string        `yaml:"base_url" env-required:"true"`
		Timeout time.Duration `yaml:"timeout" env-default:"30s"`
	} `yaml:"api_proxy" env-required:"true"`

	WebSocket struct {
		Protocol  string `yaml:"protocol" env-default:"websocket"` // "websocket" or "tcp"
		ClientID  string `yaml:"client_id" env-default:"socket-proxy-client"`
		URL       string `yaml:"url" env-default:""`
		Reconnect struct {
			BackoffMultiplier float64       `yaml:"backoff_multiplier"`
			MaxDelay          time.Duration `yaml:"max_delay"`
			InitialDelay      time.Duration `yaml:"initial_delay" env-default:"5s"`
			MaxAttempts       int           `yaml:"max_attempts" env-default:"5"`
			Enabled           bool          `yaml:"enabled" env-default:"true"`
		} `yaml:"reconnect"`
		Enabled bool `yaml:"enabled" env-default:"false"`
	} `yaml:"websocket"  env-required:"true"`

	EnabledCommands struct {
		HTTPRequest  bool `yaml:"http_request" env-default:"true"`
		APICall      bool `yaml:"api_call" env-default:"true"`
		LocalCommand bool `yaml:"local_command" env-default:"true"`
	} `yaml:"enabled_commands"`

	Logging struct {
		File   string `yaml:"file"`
		Format string `yaml:"format" env-default:"text"`
		Level  string `yaml:"level" env-default:"info"`
	} `yaml:"logging"`

	FileManager struct {
		BasePath string `yaml:"base_path"`
		Enabled  bool   `yaml:"enabled" env-default:"true"`
	} `yaml:"file_manager"`
}

type Logging struct {
	File   string `yaml:"file"`
	Format string `yaml:"format" env-default:"text"`
	Level  string `yaml:"level" env-default:"info"`
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
