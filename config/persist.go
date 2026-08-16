package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadConfig reads config.json from the data dir. It never fails: a missing
// file is created with defaults, and any read/parse error logs a warning and
// falls back to DefaultConfig. See loadJSONFile for the shared load behavior.
func LoadConfig() *Config {
	return loadJSONFile(ConfigFileName, "config", seededDefaultConfig, saveConfig)
}

// SeededDefaultConfig is the config LoadConfig falls back to when config.json is
// missing or unreadable: DefaultConfig with the machine's detected agent profiles
// filled in. Exported for the headless commands, which must reproduce that fallback
// without LoadConfig's writes — a plain DefaultConfig has no Profiles at all, so
// GetProfiles synthesizes a single "claude" and `atrium new --profile codex` is
// refused on exactly the never-run-the-TUI machine the flag exists to serve.
func SeededDefaultConfig() *Config { return seededDefaultConfig() }

// saveConfig saves the configuration to disk
func saveConfig(config *Config) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return writeFileAtomic(configPath, data, 0644)
}

// SaveConfig exports the saveConfig function for use by other packages
func SaveConfig(config *Config) error {
	return saveConfig(config)
}
