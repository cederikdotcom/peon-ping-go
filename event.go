package main

// Event is the internal, harness-agnostic event model.
type Event struct {
	Type      string // "session_start", "prompt_submit", "task_complete", "permission_needed", "idle"
	CWD       string
	SessionID string
	AgentMode bool // suppress sounds for non-interactive sessions
}

// Route describes what to do for a given event.
type Route struct {
	Category    string // sound category to play (empty = no sound)
	Status      string // tab title status text
	Marker      string // prefix for tab title (e.g. "● ")
	Notify      bool   // whether to send a desktop notification
	NotifyIcon string // "permission", "complete", "idle"
	NotifyMsg   string // notification body (project name is prepended by caller)
}

// routeEvent maps an internal Event to a Route.
func routeEvent(e Event) Route {
	switch e.Type {
	case "session_start":
		return Route{
			Category: "greeting",
			Status:   "ready",
		}
	case "prompt_submit":
		// Sound is determined later by annoyed check; category starts empty.
		return Route{
			Status: "working",
		}
	case "task_complete":
		return Route{
			Category:    "complete",
			Status:      "done",
			Marker:      "● ",
			Notify:      true,
			NotifyIcon: "complete",
			NotifyMsg:   "Task complete",
		}
	case "permission_needed":
		return Route{
			Category:    "permission",
			Status:      "needs approval",
			Marker:      "● ",
			Notify:      true,
			NotifyIcon: "permission",
			NotifyMsg:   "Permission needed",
		}
	case "idle":
		return Route{
			Status:      "done",
			Marker:      "● ",
			Notify:      true,
			NotifyIcon: "idle",
			NotifyMsg:   "Waiting for input",
		}
	default:
		return Route{}
	}
}
