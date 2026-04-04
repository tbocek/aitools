package main

import (
	"fmt"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Settings struct {
	LLMConnections []LLMConnection `toml:"llmconnections"`
}

type LLMConnection struct {
	Name  string `toml:"name"`
	URL   string `toml:"url"`
	Token string `toml:"token,omitempty"`
	Model string `toml:"model"`
}

func loadSettings(cwd string) (Settings, error) {
	path := filepath.Join(cwd, sessionDir, "settings.toml")
	var s Settings
	if _, err := toml.DecodeFile(path, &s); err != nil {
		return s, fmt.Errorf("loading settings: %w", err)
	}
	return s, nil
}

func (s *Settings) LLM(name string) *LLMConnection {
	for i := range s.LLMConnections {
		if s.LLMConnections[i].Name == name {
			return &s.LLMConnections[i]
		}
	}
	return nil
}
