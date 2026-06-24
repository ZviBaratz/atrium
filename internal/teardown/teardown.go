// Package teardown accumulates failures from a best-effort, multi-step teardown.
// Each step continues past a failure; the collected errors are logged at the point
// of failure and folded into one error at the end.
//
// It exists so the session/git/tmux teardown paths (Instance.pause, Instance.Kill,
// Worktree.Cleanup, Session.Close) share one accumulate-log-combine helper instead
// of each hand-rolling the pattern and its own error-joining tail.
//
// A step's failure is logged exactly once, at the point of failure. When one
// teardown path composes another that is itself teardown-based (Instance.Kill
// calling Session.Close and Worktree.Cleanup), the outer path uses Wrap rather
// than Record so the already-logged aggregate is not logged a second time.
package teardown

import (
	"errors"
	"fmt"

	"github.com/ZviBaratz/atrium/log"
)

// Errors collects failures from a multi-step teardown. The zero value is ready to
// use, and every method is a no-op on a nil error argument, so callers can pass a
// result straight through without pre-checking.
type Errors struct {
	errs []error
}

// Record wraps a non-nil err as "failed to <op>: err", logs it via log.ErrorLog,
// and retains it. It is a no-op for a nil err and reports whether anything was
// recorded, which lets a caller fold the wrap, log, and early return into one line:
//
//	if t.Record("commit changes", err) {
//		return t.Err()
//	}
func (e *Errors) Record(op string, err error) bool {
	if wrapped := e.wrap(op, err); wrapped != nil {
		log.ErrorLog.Print(wrapped)
		return true
	}
	return false
}

// Wrap is Record without the logging: it wraps and retains a non-nil err the same
// way but does not log it, for a step whose own failure was already logged at the
// point of failure — typically a callee that is itself a teardown path. It is a
// no-op for a nil err and reports whether anything was recorded.
func (e *Errors) Wrap(op string, err error) bool {
	return e.wrap(op, err) != nil
}

// wrap retains a non-nil err as "failed to <op>: err" and returns the wrapped
// error (nil for a nil err), without logging. It backs Record and Wrap.
func (e *Errors) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("failed to %s: %w", op, err)
	e.errs = append(e.errs, wrapped)
	return wrapped
}

// Add retains and logs an already-formed error verbatim (no "failed to" prefix),
// for the sites that build their own message or append more than one error for a
// single step. It is a no-op for a nil err.
func (e *Errors) Add(err error) {
	if err == nil {
		return
	}
	e.errs = append(e.errs, err)
	log.ErrorLog.Print(err)
}

// Err folds the collected errors into a single error, or nil when none were
// recorded. It uses errors.Join, so the result unwraps via errors.Is / errors.As.
func (e *Errors) Err() error {
	return errors.Join(e.errs...)
}
