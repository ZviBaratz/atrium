package tmux

import (
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// Structured hook state (#290 Phase 2).
//
// Phase 1 shipped a one-word state file ("working"/"ready"). Phase 2 upgrades it to a
// small JSON record that additionally carries the SET of in-flight sub-agent ids, so
// the poller can tell a genuinely-finished turn (Stop with an empty set) from one that
// only *looks* finished because a background sub-agent is still running (Stop with a
// non-empty set — the #290 mislabel).
//
// Writers are N concurrent hook subprocesses (the parent session's injected --settings
// fires for every sub-agent lifecycle event too), so every mutation is a locked
// read-modify-write: a bare shell counter can't stay correct — macOS has no flock(1),
// and unmatched SubagentStops (nested/internal agents with no matching Start) would
// drive a ++/-- counter negative. A SET keyed by agent_id is correct where a counter is
// not: add on Start, discard on Stop, unmatched discards are no-ops, and a missing Stop
// is reaped by liveness / the watchdog rather than corrupting the count. The lock and
// the JSON parse are why the hook command is the atrium binary itself, not a printf.

// Hook event names carried on the `atrium hook --event <name>` command line. They map a
// Claude Code hook to the mutation it performs on the state record. Exported because the
// hidden `hook` subcommand (main package) forwards its --event flag straight through and
// must know which events read an agent_id from stdin.
const (
	// HookEventWorking latches state="working" and bumps the heartbeat (#311), fired by the
	// tool-use working edges: PreToolUse / PostToolUse. These fire within a resolved turn, so
	// they also record its effort (see applyHookEvent's write rule).
	HookEventWorking = "working"
	// HookEventPromptSubmit is the UserPromptSubmit edge: identical to HookEventWorking
	// (latch working + bump heartbeat) except it NEVER records effort. UserPromptSubmit fires
	// before the turn's effort resolves, so its $CLAUDE_EFFORT carries the model default
	// rather than the session's real level — verified against claude 2.1.209, where a
	// `--effort low` session reports `high` here. The value is present-and-WRONG, not absent,
	// so recordEffort's `effort != ""` guard cannot catch it; splitting the event is the only
	// thing that can, since all three working edges otherwise arrive indistinguishably as
	// "working".
	HookEventPromptSubmit = "prompt-submit"
	// HookEventReady latches state="ready" (Stop / StopFailure — a clean or API-error
	// end-of-turn). Gated on the in-flight set by the poller, never terminal on its own.
	HookEventReady = "ready"
	// HookEventSubagentStart adds the stdin payload's agent_id to the in-flight set.
	HookEventSubagentStart = "subagent-start"
	// HookEventSubagentStop discards the stdin payload's agent_id from the in-flight set.
	HookEventSubagentStop = "subagent-stop"
	// HookEventResetInflight empties the in-flight set. Not wired to any Claude hook; the
	// watchdog uses it (via Session.ClearInflight) to deterministically clear a stuck set
	// so a reconciled session can't re-enter pending and oscillate (#46).
	HookEventResetInflight = "reset-inflight"
)

// HookEventReadsAgentID reports whether an event's hook subprocess must parse the stdin
// payload for an agent_id. Only the sub-agent lifecycle events do; the working/ready
// latches are payload-free, so their subprocess never touches stdin (and so can never
// block on it).
func HookEventReadsAgentID(event string) bool {
	return event == HookEventSubagentStart || event == HookEventSubagentStop
}

// hookRecord is the on-disk hook state: the working/ready latch, the set of currently
// in-flight sub-agent ids, and a heartbeat timestamp. Written atomically by
// UpdateHookState, read lock-free by the poller (rename atomicity protects it from a torn
// read).
type hookRecord struct {
	State    string   `json:"state,omitempty"`
	Inflight []string `json:"inflight,omitempty"`
	// LastHeartbeat is the unix-second wall-clock time the working latch was last set (any
	// UserPromptSubmit/PreToolUse/PostToolUse edge). The poller trusts it only as a
	// version-independent FRESHNESS signal that HOLDS working (never as a "done"/"dead"
	// authority — those stay with ready+empty, tmux liveness, and the watchdog): a hook that
	// fired within heartbeatTTL proves the main turn is live without scraping the pane (#311).
	// A crashed/killed writer stops bumping it, so it goes stale and the hold releases —
	// preserving the #46 no-stuck-latch guarantee. Zero (a Phase-1 bare-word file, or a
	// record predating this field) reads as stale, so those degrade to the scrape fallback.
	LastHeartbeat int64 `json:"heartbeat,omitempty"`
	// Effort is the reasoning-effort level claude reported for the last resolved main-session
	// turn ("" = unknown), read from the hook subprocess's $CLAUDE_EFFORT. It is claude's own
	// post-downgrade truth — the level actually in force, after any silent downgrade for the
	// selected model — so it survives an in-session /effort switch and knows the level a
	// session inherited from settings.json, neither of which the --effort flag can see.
	// Empty (a Phase-1 bare-word file, a record predating this field, or a model without
	// effort support) means the chip falls back to the flag.
	Effort string `json:"effort,omitempty"`
}

// UpdateHookState applies one hook event to the record at stateFile under an exclusive
// cross-process lock, then writes it back atomically. It is the single mutation entry
// point shared by the hook subprocesses (via the `atrium hook` subcommand) and the
// watchdog's ClearInflight. agentID is used only by the sub-agent events; it is ignored
// (and may be "") for the others. effort is the subprocess's $CLAUDE_EFFORT, recorded only
// by the events applyHookEvent's write rule admits. Best-effort by contract: callers (hooks)
// must not fail the agent on an error here, so the error is returned for logging but never
// blocks.
func UpdateHookState(stateFile, event, agentID, effort string) error {
	unlock, err := hookStateLock(stateFile)
	if err != nil {
		return err
	}
	defer unlock()

	rec, _ := readHookRecordFile(stateFile) // zero record on a missing/corrupt file
	applyHookEvent(&rec, event, agentID, effort, time.Now().Unix())
	return writeHookRecordAtomic(stateFile, rec)
}

// applyHookEvent mutates rec in place for one event; now is the unix-second wall-clock time
// used to stamp the heartbeat on a working edge. Unknown events are ignored (a forward-compat
// no-op). A sub-agent event with an empty agent_id is skipped: we can't key the set without
// an id, and adding "" would strand a phantom member the matching Stop could never discard —
// the watchdog/liveness backstop covers the untracked agent.
//
// The effort write rule (recordEffort) is confined to the working/ready latches: those are
// the main session's own resolved turns. The sub-agent edges are deliberately excluded — a
// sub-agent's effort is its own, and subagent-stop additionally empties the in-flight set,
// which would sneak its level past recordEffort's gate.
func applyHookEvent(rec *hookRecord, event, agentID, effort string, now int64) {
	switch event {
	case HookEventWorking:
		rec.State = hookStateWorking
		// Bump the heartbeat only on a working edge (UserPromptSubmit/PreToolUse/PostToolUse).
		// ready must NOT bump it, or a still-fresh heartbeat from the PostToolUse just before
		// Stop could mask a clean turn-end; the poller resolves that via ready+empty anyway,
		// but keeping ready heartbeat-free keeps the record honest (#311).
		rec.LastHeartbeat = now
		rec.recordEffort(effort)
	case HookEventPromptSubmit:
		// The same latch + heartbeat as HookEventWorking, minus the effort write: this edge's
		// $CLAUDE_EFFORT is a stale pre-resolution value (see the constant).
		rec.State = hookStateWorking
		rec.LastHeartbeat = now
	case HookEventReady:
		rec.State = hookStateReady
		rec.recordEffort(effort)
	case HookEventSubagentStart:
		if agentID != "" {
			rec.Inflight = addAgent(rec.Inflight, agentID)
		}
	case HookEventSubagentStop:
		if agentID != "" {
			rec.Inflight = removeAgent(rec.Inflight, agentID)
		}
	case HookEventResetInflight:
		rec.Inflight = nil
	}
}

// recordEffort applies the effort write rule to a main-session latch edge. Both clauses
// guard a distinct real failure:
//
//   - effort == "" — a model without effort support reports nothing. An empty read must not
//     clear the last known truth (the same stance SetModelMeta takes). This is only a
//     backstop; the stale-UserPromptSubmit defense is HookEventPromptSubmit, because that
//     value is non-empty and would sail through this check.
//   - a non-empty in-flight set — skill/sub-agent frontmatter can set its own effort, and
//     per #290 a background sub-agent's PreToolUse fires in the MAIN session against this
//     very record. Gating on the set keeps the chip on the main session's level instead of
//     flickering to a sub-agent's.
//
// The in-flight clause costs one turn in a narrow case, which is the accepted price of the
// #290 gate: a turn that still has sub-agents running when its own Stop fires records
// nothing (Stop is gated, and the SubagentStop that drains the set is itself a no-op here),
// so the level lands on the next ordinary turn instead. Harmless in practice — a turn's
// first PreToolUse usually fires before anything is spawned — and it degrades to a stale or
// flag-derived chip, never to another session's level.
func (rec *hookRecord) recordEffort(effort string) {
	if effort != "" && len(rec.Inflight) == 0 {
		rec.Effort = effort
	}
}

// addAgent returns set with id present exactly once (idempotent add).
func addAgent(set []string, id string) []string {
	for _, e := range set {
		if e == id {
			return set
		}
	}
	return append(set, id)
}

// removeAgent returns set with every occurrence of id removed. An id not in the set
// (an unmatched SubagentStop from a nested/internal agent) yields the set unchanged.
// A drained set collapses to nil so it marshals away via omitempty.
func removeAgent(set []string, id string) []string {
	var out []string
	for _, e := range set {
		if e != id {
			out = append(out, e)
		}
	}
	return out
}

// readHookRecordFile reads and parses the record at path, returning (zero, false) when
// the file is absent, empty, or unparseable. ok=false means "no usable hook signal" —
// the poller then falls back to the scrape classifier, exactly as with a missing file.
func readHookRecordFile(path string) (hookRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return hookRecord{}, false
	}
	return parseHookRecord(b)
}

// parseHookRecord decodes the state file's bytes. It accepts both the Phase 2 JSON
// record and the Phase 1 bare word ("working"/"ready"): a session already running when
// atrium is upgraded keeps its old printf hooks (which write the bare word) until it is
// relaunched, so tolerating both keeps that session's status correct across the upgrade
// instead of silently dropping it to the scrape fallback.
func parseHookRecord(b []byte) (hookRecord, bool) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return hookRecord{}, false
	}
	if trimmed[0] == '{' {
		var rec hookRecord
		if err := json.Unmarshal(trimmed, &rec); err != nil {
			return hookRecord{}, false
		}
		return rec, true
	}
	switch string(trimmed) {
	case hookStateWorking:
		return hookRecord{State: hookStateWorking}, true
	case hookStateReady:
		return hookRecord{State: hookStateReady}, true
	}
	return hookRecord{}, false
}

// writeHookRecordAtomic marshals rec to a temp file and renames it over stateFile, so a
// lock-free reader (the poller) always sees a complete record — the old one or the new
// one, never a torn write. A fixed ".tmp" suffix is safe because every writer holds the
// exclusive hookStateLock while it runs, so only one temp exists at a time.
func writeHookRecordAtomic(stateFile string, rec hookRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := stateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, stateFile)
}
