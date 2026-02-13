package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed plugins/opencode/peon-ping.ts
var opencodePlugin []byte

func installOpenCode() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: could not find home directory: %v\n", err)
		os.Exit(1)
	}

	dir := filepath.Join(home, ".config", "opencode", "plugins")
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: could not create plugin directory: %v\n", err)
		os.Exit(1)
	}

	dest := filepath.Join(dir, "peon-ping.ts")
	if err := os.WriteFile(dest, opencodePlugin, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: could not write plugin: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("peon-ping: installed OpenCode plugin at %s\n", dest)
}

func uninstallOpenCode() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: could not find home directory: %v\n", err)
		os.Exit(1)
	}

	dest := filepath.Join(home, ".config", "opencode", "plugins", "peon-ping.ts")
	if err := os.Remove(dest); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("peon-ping: OpenCode plugin not found")
		} else {
			fmt.Fprintf(os.Stderr, "peon-ping: could not remove plugin: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println("peon-ping: OpenCode plugin removed")
}
