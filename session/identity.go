package session

import (
	"fmt"
	"strings"
)

// identity is the family of name-shaped strings a session is known by: the two that
// a deep rename rewrites (title, branch) and the two cosmetic labels that ride along
// with them (displayName, note).
//
// They are one struct behind one lock because they are written together and read
// together. AdoptRename writes title and branch in a single statement pair, and the
// rename handler (app's renameDoneMsg case) calls SetDisplayName and SetNote in the
// two statements after it — so a reader that took each field separately could observe
// a post-rename title beside a pre-rename branch. Identity returns the whole family at
// once for the callers that need it consistent.
//
// The fields are unexported and this file is their only access site (enforced by
// TestIdentityFieldsAreTouchedOnlyByTheirAccessors): every other reader in the tree
// goes through the accessors below, which is what makes "guarded" a property of the
// field rather than an argument about the goroutine each reader happens to run on.
// See #795, and #719 for the census of readers that argument used to have to cover.
type identity struct {
	// title is the stable identifier used as the storage key and to seed the git branch
	// and tmux session names at creation. A deep rename (Rename + AdoptRename) is the
	// only thing that changes it after the instance has started.
	title string
	// displayName is an optional, purely cosmetic label shown in the list in place of
	// title. Unlike title it can be changed at any time because it is decoupled from the
	// git branch, worktree, and tmux session. Empty means "show title".
	displayName string
	// note is an optional freeform annotation surfaced on the session's row
	// (e.g. "blocked on review"). Like displayName it is cosmetic, mutable at
	// any time, and decoupled from the git branch / tmux session.
	note string
	// branch is the git branch of the instance: published by Start once the worktree
	// has minted it, and rewritten by a deep rename. Empty for a direct session, which
	// has no worktree to derive one from.
	branch string
}

// Identity is a consistent snapshot of the identity fields, taken under one lock.
//
// Use it wherever two of them are needed together — the session brief, the repo-script
// context, the InstanceData marshalling — so the pair cannot straddle a rename. Use it
// also to pin a value that will be read on another goroutine: a lock makes a read safe,
// it does not make it current, so anything crossing a goroutine boundary still wants a
// value snapshotted on the update thread rather than a live accessor call (#718).
type Identity struct {
	Title       string
	DisplayName string
	Note        string
	Branch      string
}

// Identity returns the instance's identity fields as one snapshot.
func (i *Instance) Identity() Identity {
	i.identityMu.RLock()
	defer i.identityMu.RUnlock()
	return Identity{
		Title:       i.ident.title,
		DisplayName: i.ident.displayName,
		Note:        i.ident.note,
		Branch:      i.ident.branch,
	}
}

// Title returns the instance's stable identifier: the storage key, and the seed for its
// git branch and tmux session names.
func (i *Instance) Title() string {
	i.identityMu.RLock()
	defer i.identityMu.RUnlock()
	return i.ident.title
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc. — after that the
// only route is a deep rename (Rename, then AdoptRename).
func (i *Instance) SetTitle(title string) error {
	if i.isStarted() {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.identityMu.Lock()
	defer i.identityMu.Unlock()
	i.ident.title = title
	return nil
}

// Branch returns the instance's git branch, or "" for a direct session.
func (i *Instance) Branch() string {
	i.identityMu.RLock()
	defer i.identityMu.RUnlock()
	return i.ident.branch
}

// SetBranch publishes the branch the worktree minted. Start calls it from its own
// goroutine — the second identity writer besides the update thread's AdoptRename, and
// the reason this family is behind a mutex rather than a single-writer atomic.
func (i *Instance) SetBranch(branch string) {
	i.identityMu.Lock()
	defer i.identityMu.Unlock()
	i.ident.branch = branch
}

// DisplayName returns the cosmetic label shown for the instance, falling back to Title when
// no custom label has been set. The fallback is why this method is a route onto title and
// not only onto displayName.
func (i *Instance) DisplayName() string {
	i.identityMu.RLock()
	defer i.identityMu.RUnlock()
	if i.ident.displayName != "" {
		return i.ident.displayName
	}
	return i.ident.title
}

// SetDisplayName sets the cosmetic display label. Unlike SetTitle it works at any time
// (even after the instance has started) because the label is decoupled from the git branch
// and tmux session. Whitespace is trimmed; an empty value clears the label so the name
// reverts to Title.
func (i *Instance) SetDisplayName(name string) {
	name = strings.TrimSpace(name)
	i.identityMu.Lock()
	defer i.identityMu.Unlock()
	i.ident.displayName = name
}

// Note returns the freeform annotation shown on the session's row, or "" when unset.
func (i *Instance) Note() string {
	i.identityMu.RLock()
	defer i.identityMu.RUnlock()
	return i.ident.note
}

// SetNote sets the freeform annotation. Whitespace is trimmed; an empty value clears it.
// Like SetDisplayName it works at any time and is independent of the git branch and tmux
// session.
func (i *Instance) SetNote(note string) {
	note = strings.TrimSpace(note)
	i.identityMu.Lock()
	defer i.identityMu.Unlock()
	i.ident.note = note
}

// RenamedIdentity is the identity a completed deep rename has earned but not yet
// adopted: the I/O is done, and these are the fields the main loop must write.
// It exists so Rename can run off the update thread without touching the identity
// fields at all — see AdoptRename.
type RenamedIdentity struct {
	Title    string
	Branch   string
	TmuxName string
}

// AdoptRename writes the identity a successful Rename earned.
//
// Title and Branch are written under one acquisition of identityMu, so no reader can
// observe a half-adopted identity — a new title beside the old branch. A zero Branch is
// left alone: a direct session has no worktree to derive one from, so overwriting would
// blank a field the rename never owned.
//
// This used to be documented as main-loop-only, because Title and Branch were plain
// exported fields with no lock and every off-thread reader had to carry its own argument
// for why it could not overlap this write (#718 converted one such reader; #719 was the
// census of the rest). That is no longer the rule: the fields are unexported, every read
// goes through the accessors in this file, and a second writer — Start's SetBranch, on
// Start's own goroutine — is now serialised against this one rather than merely
// improbable.
//
// What has NOT changed is that a lock makes a read safe, not current. A value that will
// be used on another goroutine still wants to be snapshotted on the thread that has it
// (frameTarget.termTitle for TerminalPane.EnsureSession, app's customCommandSpec strings),
// because what those need is the identity as of the frame, not whichever one wins a race
// with the adopt.
//
// It deliberately leaves both sibling names alone: termName and runName are owned rather
// than derived, so the shell and the dev server keep the names they were created under and
// stay reachable by the teardowns that must kill them (#389, #708). Their tmux sessions are
// not renamed on the socket either — the same call hooks make for a stronger reason
// (tmux_rename.go). Nothing here may start chasing them without also moving the sessions.
func (i *Instance) AdoptRename(renamed RenamedIdentity) {
	i.identityMu.Lock()
	i.ident.title = renamed.Title
	if renamed.Branch != "" {
		i.ident.branch = renamed.Branch
	}
	i.identityMu.Unlock()

	i.mu.Lock()
	i.tmuxName = renamed.TmuxName
	i.mu.Unlock()
}
