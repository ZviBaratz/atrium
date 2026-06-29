//go:build windows

package tmux

import (
	"github.com/ZviBaratz/atrium/log"
	"os"
	"time"

	"golang.org/x/term"
)

// monitorWindowSize monitors and handles window resize events while attached.
func (t *Session) monitorWindowSize() {
	// Use the current terminal height and width.
	doUpdate := func() {
		cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil {
			log.ErrorLog.Printf("failed to update window size: %v", err)
		} else {
			if err := t.updateWindowSize(cols, rows); err != nil {
				log.ErrorLog.Printf("failed to update window size: %v", err)
			}
		}
	}

	// Do one at the start to set the initial size
	doUpdate()

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		// On Windows there is no SIGWINCH, so poll for size changes. The ticker is
		// created and stopped INSIDE the goroutine that uses it: monitorWindowSize
		// returns immediately after launching this goroutine, so a function-scoped
		// `defer ticker.Stop()` would fire right away and leave the loop spinning on
		// a dead ticker (it never ticks again).
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		lastCols, lastRows, _ := term.GetSize(int(os.Stdin.Fd()))
		for {
			select {
			case <-t.ctx.Done():
				return
			case <-ticker.C:
				cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
				if err != nil {
					continue
				}
				if cols != lastCols || rows != lastRows {
					lastCols, lastRows = cols, rows
					doUpdate()
				}
			}
		}
	}()
}
