package tmux

import (
	"fmt"
	"github.com/ZviBaratz/atrium/log"
)

// Rename renames the live tmux session and its window to match newName, then swaps the
// cached sanitizedName/windowName. The write lock is held across the rename-session
// subprocess AND the field swap, so a concurrent reader (the metadata poll loop) never
// observes the brief window where the old session name no longer exists — which would
// otherwise read as a "lost session". If the session isn't live (e.g. paused after a
// reboot) it updates the cached names only, so a later restore targets the new name.
func (t *Session) Rename(newName string) error {
	newSanitized := toSanitizedName(newName)

	t.mu.Lock()
	defer t.mu.Unlock()

	oldSanitized := t.sanitizedName
	if newSanitized != oldSanitized {
		// Only touch tmux if the session is actually live. has-session is inlined here
		// rather than calling DoesSessionExist, which would re-acquire the read lock and
		// deadlock (sync.RWMutex is not reentrant).
		if t.cmdExec.Run(tmuxCommand("has-session", fmt.Sprintf("-t=%s", oldSanitized))) == nil {
			if err := t.cmdExec.Run(tmuxCommand("rename-session", "-t", oldSanitized, newSanitized)); err != nil {
				return fmt.Errorf("failed to rename tmux session %q to %q: %w", oldSanitized, newSanitized, err)
			}
			// The window name is cosmetic (the conf disables auto-rename); log on failure
			// but don't abort an otherwise-successful rename.
			if err := t.cmdExec.Run(tmuxCommand("rename-window", "-t", newSanitized, newName)); err != nil {
				log.ErrorLog.Printf("failed to rename tmux window to %q: %v", newName, err)
			}
		}
	}

	t.sanitizedName = newSanitized
	t.windowName = newName
	return nil
}
