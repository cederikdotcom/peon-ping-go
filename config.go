package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// Config represents config.json (user preferences).
type Config struct {
	ActivePack           string          `json:"active_pack"`
	Volume               float64         `json:"volume"`
	Enabled              bool            `json:"enabled"`
	Categories           map[string]bool `json:"categories"`
	AnnoyedThreshold     int             `json:"annoyed_threshold"`
	AnnoyedWindowSeconds float64         `json:"annoyed_window_seconds"`
}

// State represents .state.json (runtime state).
type State struct {
	LastPlayed       map[string]string  `json:"last_played"`
	PromptTimestamps []float64          `json:"prompt_timestamps"`
	AgentSessions    []string           `json:"agent_sessions,omitempty"`
	WindowHandles    map[string]uint64  `json:"window_handles,omitempty"`
}

// lockedState holds the state plus the open file handle for flock.
type lockedState struct {
	State *State
	file  *os.File
}

func defaultConfig() Config {
	return Config{
		ActivePack: "peon",
		Volume:     0.5,
		Enabled:    true,
		Categories: map[string]bool{
			"greeting":       true,
			"acknowledge":    true,
			"complete":       true,
			"error":          true,
			"permission":     true,
			"resource_limit": true,
			"annoyed":        true,
		},
		AnnoyedThreshold:     3,
		AnnoyedWindowSeconds: 10,
	}
}

func loadConfig(peonDir string) Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(filepath.Join(peonDir, "config.json"))
	if err != nil {
		return cfg
	}
	// Unmarshal on top of defaults so missing fields keep defaults.
	_ = json.Unmarshal(data, &cfg)
	if cfg.ActivePack == "" {
		cfg.ActivePack = "peon"
	}
	if cfg.Volume == 0 {
		cfg.Volume = 0.5
	}
	if cfg.AnnoyedThreshold == 0 {
		cfg.AnnoyedThreshold = 3
	}
	if cfg.AnnoyedWindowSeconds == 0 {
		cfg.AnnoyedWindowSeconds = 10
	}
	return cfg
}

func saveConfig(peonDir string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(peonDir, "config.json"), data, 0644)
}

// loadStateLocked opens .state.json with an exclusive flock.
// Caller MUST call saveStateUnlock when done.
func loadStateLocked(peonDir string) (*lockedState, error) {
	statePath := filepath.Join(peonDir, ".state.json")

	f, err := os.OpenFile(statePath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}

	// Acquire exclusive lock (blocking).
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}

	var st State
	data, err := os.ReadFile(statePath)
	if err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &st)
	}
	if st.LastPlayed == nil {
		st.LastPlayed = make(map[string]string)
	}

	return &lockedState{State: &st, file: f}, nil
}

// saveStateUnlock writes state and releases the flock.
func (ls *lockedState) saveStateUnlock(peonDir string) error {
	statePath := filepath.Join(peonDir, ".state.json")

	data, err := json.Marshal(ls.State)
	if err != nil {
		syscall.Flock(int(ls.file.Fd()), syscall.LOCK_UN)
		ls.file.Close()
		return err
	}

	err = os.WriteFile(statePath, data, 0644)

	// Release lock and close.
	syscall.Flock(int(ls.file.Fd()), syscall.LOCK_UN)
	ls.file.Close()
	return err
}
