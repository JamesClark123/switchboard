package orchestrator

import "time"

// Every bound of the feature is a package constant (research R15) — never an
// environment variable.
const (
	// linkDialTimeout bounds one SSH dial attempt (matches the TUI's sign-in).
	linkDialTimeout = 30 * time.Second
	// linkRetryMin / linkRetryMax bound the backoff while a host link is down.
	linkRetryMin = 15 * time.Second
	linkRetryMax = 2 * time.Minute
	// linkPingInterval is the OrchestratorLink keepalive cadence, both ways.
	linkPingInterval = 30 * time.Second
	// deliverTimeout bounds DeliverWorkerPrompt end to end.
	deliverTimeout = 15 * time.Second
	// reviewGrace is how long an idle, unreported run waits before it is handed
	// to the developer as awaiting review (a report may still be in flight).
	reviewGrace = 10 * time.Second
	// reasoningDestroyTimeout bounds the destroy half of a reasoning rebuild
	// or an orchestrator removal.
	reasoningDestroyTimeout = 2 * time.Minute
	// reasoningLaunchTimeout bounds the first (background) launch after Create.
	reasoningLaunchTimeout = 30 * time.Minute
	// reconcileTimeout bounds the in-flight reconciliation after a link comes up.
	reconcileTimeout = 30 * time.Second
	// unaryTimeout bounds the other worker-side unary calls over a link.
	unaryTimeout = 30 * time.Second
	// minIntervalSeconds is the smallest timed-event interval.
	minIntervalSeconds = 60
	// windowSearchDays bounds the search for a timed event's next window opening
	// (SWI-10); a week plus a day always finds one.
	windowSearchDays = 8
	// historyKeep is how many finished runs are retained per orchestrator.
	historyKeep = 200
	// maxArgumentBytes / maxPromptBytes bound a call's values and the composed prompt.
	maxArgumentBytes = 16 * 1024
	maxPromptBytes   = 64 * 1024
	// maxNameLen bounds an orchestrator's name (FR-105).
	maxNameLen = 64
)
