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
	// per-tool working edges: PreToolUse / PostToolUse. These fire within a resolved turn, so
	// they also record its effort (see applyHookEvent's write rule). Deliberately stdin-free,
	// so they carry no permission_mode — see HookEventReadsStdin.
	HookEventWorking = "working"
	// HookEventPromptSubmit is the UserPromptSubmit edge: it latches working and bumps the
	// heartbeat exactly like HookEventWorking, but takes the OPPOSITE rule on each of the two
	// payload fields. The split earns its keep once per issue:
	//
	//   - It NEVER records effort (#325). UserPromptSubmit fires before the turn's effort
	//     resolves, so its $CLAUDE_EFFORT carries the model default rather than the session's
	//     real level — verified against claude 2.1.209, where a `--effort low` session reports
	//     `high` here. The value is present-and-WRONG, not absent, so recordEffort's
	//     `effort != ""` guard cannot catch it; splitting the event is the only thing that can,
	//     since all three working edges otherwise arrive indistinguishably as "working".
	//   - It DOES record the permission mode (#324), because it is the once-per-turn edge and
	//     so the cheap place to read stdin: the per-tool edges stay on HookEventWorking and
	//     stay stdin-free, keeping the N-per-turn hot path free of a payload read (a
	//     PostToolUse payload embeds the whole tool_response).
	//
	// The rules are opposite because the CARRIERS are: effort rides $CLAUDE_EFFORT, an env var
	// resolved late, whereas permission_mode rides the stdin payload — claude's own
	// toolPermissionContext at hook time, correct from this first edge onward.
	HookEventPromptSubmit = "prompt-submit"
	// HookEventReady latches state="ready" (Stop / StopFailure — a clean or API-error
	// end-of-turn) and records both the permission mode (#324) and the turn's effort (see
	// applyHookEvent's write rule). Gated on the in-flight set by the poller, never terminal
	// on its own.
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

// HookPayload is the subset of a Claude hook's stdin payload the state record consumes:
// agent_id for the in-flight set (#290), permission_mode for the mode chip (#324). Both are
// optional, and "" always means "no information" rather than a value — SubagentStart and
// StopFailure omit permission_mode entirely, and the main loop's own events omit agent_id
// (both verified live against claude 2.1.209).
type HookPayload struct {
	AgentID        string `json:"agent_id"`
	PermissionMode string `json:"permission_mode"`
}

// HookEventReadsStdin reports whether an event's hook subprocess must parse the stdin
// payload: the sub-agent events need an agent_id, and the prompt-submit/ready edges need a
// permission_mode. HookEventWorking is deliberately excluded — it is the only event that
// fires N times per turn (PreToolUse/PostToolUse), so keeping it payload-free keeps the hot
// path off stdin entirely, and away from the fat tool_response a PostToolUse embeds.
//
// The "a hook can never block on stdin" property this predicate used to buy by abstention is
// now enforced by construction in the reader itself (main.parseHookPayload is bounded in both
// size and time), which is what makes it safe for the once-per-turn edges to read at all.
func HookEventReadsStdin(event string) bool {
	switch event {
	case HookEventSubagentStart, HookEventSubagentStop, HookEventPromptSubmit, HookEventReady:
		return true
	}
	return false
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
	// PermissionMode is the live effective permission mode the last main-turn edge carried
	// (#324) — claude's own toolPermissionContext at hook time, so it tracks an in-session
	// Shift+Tab rather than the launch flag, and it reports the stable enum ("default") rather
	// than the footer's display label ("manual mode on"). Unlike Effort, this is only the
	// FALLBACK source for its chip: the footer scrape stays primary because it refreshes every
	// 500ms whereas this refreshes only when a hook fires — no Claude hook fires on a mode
	// switch itself (see Session.RuntimePermissionMode). Effort has no such scrape to defer to
	// (it is not rendered in the pane at all), which is why the two chips arbitrate
	// differently. "" = never recorded: a hookless session, a Phase-1 bare-word file, or a
	// record predating this field.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// UpdateHookState applies one hook event to the record at stateFile under an exclusive
// cross-process lock, then writes it back atomically. It is the single mutation entry
// point shared by the hook subprocesses (via the `atrium hook` subcommand) and the
// watchdog's ClearInflight.
//
// The two value params are deliberately separate because their CARRIERS are: p holds the
// stdin-derived fields (agent_id, permission_mode) and may be zero — each event uses only
// the ones it needs, see HookEventReadsStdin — while effort is the subprocess's
// $CLAUDE_EFFORT, an env var every event has for free. Folding them into one struct would
// hide that split, and it is the whole reason the per-tool hot path can carry effort without
// touching stdin.
//
// Best-effort by contract: callers (hooks) must not fail the agent on an error here, so the
// error is returned for logging but never blocks.
func UpdateHookState(stateFile, event string, p HookPayload, effort string) error {
	unlock, err := hookStateLock(stateFile)
	if err != nil {
		return err
	}
	defer unlock()

	rec, _ := readHookRecordFile(stateFile) // zero record on a missing/corrupt file
	applyHookEvent(&rec, event, p, effort, time.Now().Unix())
	return writeHookRecordAtomic(stateFile, rec)
}

// applyHookEvent mutates rec in place for one event; now is the unix-second wall-clock time
// used to stamp the heartbeat on a working edge. Unknown events are ignored (a forward-compat
// no-op — which is also how a downgraded binary tolerates a prompt-submit hook baked in by a
// newer one). A sub-agent event with an empty agent_id is skipped: we can't key the set
// without an id, and adding "" would strand a phantom member the matching Stop could never
// discard — the watchdog/liveness backstop covers the untracked agent.
//
// The two field write rules both exclude the sub-agent edges, but by different tests, and the
// difference is forced by the carrier rather than chosen:
//
//   - recordEffort gates on an EMPTY IN-FLIGHT SET. Effort arrives on the stdin-free per-tool
//     edges, where there is no payload and therefore no agent_id to test — the set is the only
//     signal available.
//   - setPermissionMode gates on the payload's AGENT_ID. Mode only ever rides events that
//     already read stdin, so it can test the writer directly instead of inferring from a set a
//     missed SubagentStart would leave empty — and it need not suppress a legitimate main-turn
//     update merely because background work is in flight.
func applyHookEvent(rec *hookRecord, event string, p HookPayload, effort string, now int64) {
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
		// The same latch + heartbeat as HookEventWorking, with each field on its opposite rule
		// (see the constant): no effort write, because this edge's $CLAUDE_EFFORT is a stale
		// pre-resolution value; but this IS the edge that records the mode, because it is the
		// once-per-turn one and its payload's permission_mode is already correct.
		rec.State = hookStateWorking
		rec.LastHeartbeat = now
		setPermissionMode(rec, p)
	case HookEventReady:
		rec.State = hookStateReady
		rec.recordEffort(effort)
		setPermissionMode(rec, p)
	case HookEventSubagentStart:
		if p.AgentID != "" {
			rec.Inflight = addAgent(rec.Inflight, p.AgentID)
		}
	case HookEventSubagentStop:
		if p.AgentID != "" {
			rec.Inflight = removeAgent(rec.Inflight, p.AgentID)
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

// setPermissionMode records a main-turn edge's permission_mode (#324). Two guards, both
// load-bearing and both grounded in a live probe of claude 2.1.209:
//
//   - An empty mode is "no information", never a value. StopFailure omits the field
//     entirely, so an errored turn-end must leave the last known mode standing rather than
//     blank the chip. Any future event that drops the field is absorbed by the same guard,
//     with no per-event knowledge here to drift.
//   - A payload carrying an agent_id came from a SUB-AGENT, whose hooks fire on the PARENT's
//     state file (#290) and whose permission_mode is resolved through its own
//     permissionLayers — i.e. a mode the user never chose for this session. The main loop's
//     own events carry no agent_id, so this cleanly separates the two.
//
// The agent_id test is preferred over gating on the in-flight set: it is a direct property of
// the writer rather than an inference from a set that a missed SubagentStart would leave
// empty, and it does not suppress a legitimate main-turn update merely because a background
// sub-agent happens to be in flight.
func setPermissionMode(rec *hookRecord, p HookPayload) {
	if p.PermissionMode == "" || p.AgentID != "" {
		return
	}
	rec.PermissionMode = p.PermissionMode
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
