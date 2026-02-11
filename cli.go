package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func runCLI(peonDir string, args []string) {
	if len(args) == 0 {
		return
	}

	pausedFile := filepath.Join(peonDir, ".paused")

	switch args[0] {
	case "--pause":
		f, err := os.Create(pausedFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		f.Close()
		fmt.Println("peon-ping: sounds paused")
		os.Exit(0)

	case "--resume":
		os.Remove(pausedFile)
		fmt.Println("peon-ping: sounds resumed")
		os.Exit(0)

	case "--toggle":
		if _, err := os.Stat(pausedFile); err == nil {
			os.Remove(pausedFile)
			fmt.Println("peon-ping: sounds resumed")
		} else {
			f, _ := os.Create(pausedFile)
			if f != nil {
				f.Close()
			}
			fmt.Println("peon-ping: sounds paused")
		}
		os.Exit(0)

	case "--status":
		if _, err := os.Stat(pausedFile); err == nil {
			fmt.Println("peon-ping: paused")
		} else {
			fmt.Println("peon-ping: active")
		}
		os.Exit(0)

	case "--packs":
		cfg := loadConfig(peonDir)
		packs, err := listPacks(peonDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		sort.Slice(packs, func(i, j int) bool {
			return packs[i].Name < packs[j].Name
		})
		for _, p := range packs {
			display := p.DisplayName
			if display == "" {
				display = p.Name
			}
			marker := ""
			if p.Name == cfg.ActivePack {
				marker = " *"
			}
			fmt.Printf("  %-24s %s%s\n", p.Name, display, marker)
		}
		os.Exit(0)

	case "--pack":
		cfg := loadConfig(peonDir)
		packs, err := listPacks(peonDir)
		if err != nil || len(packs) == 0 {
			fmt.Fprintln(os.Stderr, "Error: no packs found")
			os.Exit(1)
		}

		names := make([]string, len(packs))
		displayNames := make(map[string]string)
		for i, p := range packs {
			names[i] = p.Name
			displayNames[p.Name] = p.DisplayName
		}
		sort.Strings(names)

		var target string
		if len(args) > 1 {
			// Specific pack requested.
			target = args[1]
			found := false
			for _, n := range names {
				if n == target {
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "Error: pack %q not found.\n", target)
				fmt.Fprintf(os.Stderr, "Available packs: ")
				for i, n := range names {
					if i > 0 {
						fmt.Fprintf(os.Stderr, ", ")
					}
					fmt.Fprintf(os.Stderr, "%s", n)
				}
				fmt.Fprintln(os.Stderr)
				os.Exit(1)
			}
		} else {
			// Cycle to next pack.
			idx := -1
			for i, n := range names {
				if n == cfg.ActivePack {
					idx = i
					break
				}
			}
			target = names[(idx+1)%len(names)]
		}

		cfg.ActivePack = target
		if err := saveConfig(peonDir, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
			os.Exit(1)
		}
		display := displayNames[target]
		if display == "" {
			display = target
		}
		fmt.Printf("peon-ping: switched to %s (%s)\n", target, display)
		os.Exit(0)

	case "--help", "-h":
		fmt.Print(`Usage: peon <command>

Commands:
  --pause        Mute sounds
  --resume       Unmute sounds
  --toggle       Toggle mute on/off
  --status       Check if paused or active
  --packs        List available sound packs
  --pack <name>  Switch to a specific pack
  --pack         Cycle to the next pack
  --version      Show version
  --help         Show this help
`)
		os.Exit(0)

	case "--version":
		fmt.Printf("peon-ping %s\n", version)
		os.Exit(0)

	default:
		if len(args[0]) > 0 && args[0][0] == '-' {
			fmt.Fprintf(os.Stderr, "Unknown option: %s\n", args[0])
			fmt.Fprintln(os.Stderr, "Run 'peon --help' for usage.")
			os.Exit(1)
		}
	}
}
