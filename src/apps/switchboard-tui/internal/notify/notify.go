// Package notify raises OS desktop notifications (FR-026a). The in-TUI
// notification list is the guaranteed channel; the desktop notification is a
// best-effort addition that must never break the TUI when no notification
// daemon is present (e.g. headless), so errors are swallowed.
package notify

import "github.com/gen2brain/beeep"

// Notifier raises an OS desktop notification. Implementations MUST be safe to
// call even when no desktop is available.
type Notifier interface {
	Notify(title, message string)
}

// beeepNotifier sends real desktop notifications via beeep.
type beeepNotifier struct{}

// New returns the default (beeep-backed) Notifier.
func New() Notifier { return beeepNotifier{} }

// Notify raises a desktop notification, ignoring errors (the in-TUI list remains
// the guaranteed channel — spec Assumption).
func (beeepNotifier) Notify(title, message string) {
	_ = beeep.Notify(title, message, "")
}

// NoopNotifier discards notifications (used when desktop notifications are
// explicitly disabled, and as a test double).
type NoopNotifier struct{}

func (NoopNotifier) Notify(string, string) {}
