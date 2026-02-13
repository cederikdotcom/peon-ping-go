// peon-ping bridge plugin for OpenCode
// Translates OpenCode events into peon-ping generic JSON format
// for sounds, notifications, and action bar integration.

import { spawnSync } from "child_process";
import { existsSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const peonBin = (() => {
  const known = join(homedir(), ".claude", "hooks", "peon-ping", "peon");
  if (existsSync(known)) return known;
  return "peon";
})();

function sendToPeon(event: Record<string, unknown>) {
  try {
    spawnSync(peonBin, [], {
      input: JSON.stringify(event),
      timeout: 5000,
      stdio: ["pipe", "ignore", "ignore"],
    });
  } catch {}
}

export const PeonPing = async (ctx: any) => {
  const cwd = ctx.directory || process.cwd();
  const lastStatus = new Map<string, string>();

  return {
    event: async ({ event }: { event: any }) => {
      const props = event.properties || {};
      const sessionId = props.sessionID || props.info?.id || "opencode";

      switch (event.type) {
        case "session.created":
          sendToPeon({ type: "session_start", cwd, session_id: sessionId });
          break;

        case "session.deleted":
          sendToPeon({ type: "session_end", cwd, session_id: sessionId });
          lastStatus.delete(sessionId);
          break;

        case "session.idle":
          sendToPeon({
            type: "task_complete",
            cwd,
            session_id: sessionId,
          });
          break;

        case "session.status": {
          const status = props.status?.type;
          if (status === "busy" && lastStatus.get(sessionId) !== "busy") {
            sendToPeon({ type: "prompt_submit", cwd, session_id: sessionId });
          }
          lastStatus.set(sessionId, status || "");
          break;
        }

        case "permission.updated":
          sendToPeon({
            type: "permission_needed",
            cwd,
            session_id: sessionId,
            message: props.title || "Permission needed",
          });
          break;
      }
    },
  };
};
