# peon-ping-go

Warcraft III Peon voice lines for AI coding hooks. Plays sounds and shows desktop notifications when your AI assistant needs attention.

Go rewrite of [peon-ping](https://github.com/tonyyont/peon-ping) — eliminates python3 subprocess overhead (~5ms vs ~1s+).

## Features

- Sound packs: Orc Peon, Human Peasant, StarCraft Battlecruiser, Kerrigan, RA2 Soviet Engineer (+ French/Polish variants)
- Desktop notifications with colored popups (red/blue/yellow)
- Annoyed easter egg — spam prompts and the peon gets irritated
- Agent suppression — silent in non-interactive (delegate) sessions
- Pause/resume, pack switching, daily update checks
- Harness-agnostic — works with Claude Code out of the box, extensible to other tools

## Install

```bash
git clone https://github.com/cederikdotcom/peon-ping-go.git
cd peon-ping-go
make install   # builds and copies binary to ~/.claude/hooks/peon-ping/
```

### Hook setup

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [{ "matcher": "", "hooks": [{ "type": "command", "command": "~/.claude/hooks/peon-ping/peon", "timeout": 10 }] }],
    "UserPromptSubmit": [{ "matcher": "", "hooks": [{ "type": "command", "command": "~/.claude/hooks/peon-ping/peon", "timeout": 10 }] }],
    "Stop": [{ "matcher": "", "hooks": [{ "type": "command", "command": "~/.claude/hooks/peon-ping/peon", "timeout": 10 }] }],
    "Notification": [{ "matcher": "", "hooks": [{ "type": "command", "command": "~/.claude/hooks/peon-ping/peon", "timeout": 10 }] }]
  }
}
```

Optional shell alias:

```bash
alias peon="~/.claude/hooks/peon-ping/peon"
```

## Usage

```
peon --pause        Mute sounds
peon --resume       Unmute sounds
peon --toggle       Toggle mute on/off
peon --status       Check if paused or active
peon --packs        List available sound packs
peon --pack <name>  Switch to a specific pack
peon --pack         Cycle to the next pack
peon --version      Show version
```

## How it works

The binary reads JSON from stdin (piped by the hook system), detects the harness, maps the event to a sound category, picks a random sound (avoiding repeats), and fires off audio + notification in the background. The Go process exits in ~5ms; the Windows/macOS audio process continues playing independently.

### Event routing

| Hook event | Sound category | Tab title | Notification |
|---|---|---|---|
| Session start | greeting | `project: ready` | — |
| Prompt submit | annoyed (if spamming) | `project: working` | — |
| Task complete | complete | `● project: done` | blue |
| Permission needed | permission | `● project: needs approval` | red |
| Idle | — | `● project: done` | yellow |

## Platforms

- **WSL** — audio via `powershell.exe` MediaPlayer, notifications via WinForms popups
- **macOS** — audio via `afplay`, notifications via `osascript`

## Sound packs

Sound packs live in `~/.claude/hooks/peon-ping/packs/`. Each pack has a `manifest.json` and a `sounds/` directory with WAV files. See existing packs for the format.

## License

MIT
