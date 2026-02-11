package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
)

// Manifest represents a sound pack's manifest.json.
type Manifest struct {
	Name        string                      `json:"name"`
	DisplayName string                      `json:"display_name"`
	Categories  map[string]ManifestCategory `json:"categories"`
}

// ManifestCategory holds the sounds for one category.
type ManifestCategory struct {
	Sounds []ManifestSound `json:"sounds"`
}

// ManifestSound is a single sound entry.
type ManifestSound struct {
	File string `json:"file"`
	Line string `json:"line"`
}

// loadManifest loads a sound pack's manifest.json.
func loadManifest(peonDir, packName string) (Manifest, error) {
	path := filepath.Join(peonDir, "packs", packName, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// pickSound selects a random sound from the category, avoiding the last-played.
// Updates state.LastPlayed. Returns the full path to the sound file, or "" if none.
func pickSound(peonDir, packName, category string, state *State) string {
	manifest, err := loadManifest(peonDir, packName)
	if err != nil {
		return ""
	}

	cat, ok := manifest.Categories[category]
	if !ok || len(cat.Sounds) == 0 {
		return ""
	}

	lastFile := state.LastPlayed[category]

	candidates := cat.Sounds
	if len(cat.Sounds) > 1 {
		filtered := make([]ManifestSound, 0, len(cat.Sounds))
		for _, s := range cat.Sounds {
			if s.File != lastFile {
				filtered = append(filtered, s)
			}
		}
		candidates = filtered
	}

	pick := candidates[rand.Intn(len(candidates))]
	state.LastPlayed[category] = pick.File

	return filepath.Join(peonDir, "packs", packName, "sounds", pick.File)
}

// checkAnnoyed checks if the user is spamming prompts.
// Adds current timestamp, prunes old ones, returns true if threshold exceeded.
func checkAnnoyed(state *State, threshold int, windowSeconds float64, now float64) bool {
	cutoff := now - windowSeconds
	filtered := make([]float64, 0, len(state.PromptTimestamps))
	for _, t := range state.PromptTimestamps {
		if t >= cutoff {
			filtered = append(filtered, t)
		}
	}
	filtered = append(filtered, now)
	state.PromptTimestamps = filtered
	return len(filtered) >= threshold
}

// checkAgent checks if this session is an agent session.
// If permissionMode is an agent mode, records the session.
// Returns true if the session should be suppressed.
func checkAgent(state *State, sessionID, permissionMode string) bool {
	if permissionMode == "delegate" && sessionID != "" {
		// Add to agent sessions if not already there.
		found := false
		for _, s := range state.AgentSessions {
			if s == sessionID {
				found = true
				break
			}
		}
		if !found {
			state.AgentSessions = append(state.AgentSessions, sessionID)
		}
		return true
	}

	// Check if this session was previously identified as agent.
	for _, s := range state.AgentSessions {
		if s == sessionID {
			return true
		}
	}

	return false
}

// listPacks returns all available pack directories with manifests.
func listPacks(peonDir string) ([]Manifest, error) {
	pattern := filepath.Join(peonDir, "packs", "*", "manifest.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var packs []Manifest
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}
		// Use directory name if manifest name is empty.
		if manifest.Name == "" {
			manifest.Name = filepath.Base(filepath.Dir(m))
		}
		packs = append(packs, manifest)
	}
	return packs, nil
}
