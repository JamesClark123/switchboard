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
	// schedulerMaxSleep caps one scheduler timer. Go timers run on the monotonic
	// clock, which stops while the host is suspended, so a single sleep until a
	// window opening hours away would fire late by the whole suspend; waking at
	// least this often re-reads the wall clock (SWI-10).
	schedulerMaxSleep = time.Minute
	// historyKeep is how many finished runs are retained per orchestrator.
	historyKeep = 200
	// maxArgumentBytes / maxPromptBytes bound a call's values and the composed prompt.
	maxArgumentBytes = 16 * 1024
	maxPromptBytes   = 64 * 1024
	// maxStepPrompts bounds each of an action's custom pre/post prompt lists (FR-204).
	maxStepPrompts = 10
	// stepRetryEvery is how often a step the worker refused (FAILED_PRECONDITION)
	// is tried again while no status change kicks it (research R6).
	stepRetryEvery = 2 * time.Second
	// stepWaitNote is how long refusals last before the step's note reads
	// "waiting: <reason>" (research R6).
	stepWaitNote = 10 * time.Second
	// inputOnlyIdle is how long the input box must stay empty and ready after a
	// step classified only by a needs-input report before it is finished (the
	// idle-nudge fallback, FR-206b, research R1).
	inputOnlyIdle = 60 * time.Second
	// sentGrace is how long a step typed but not yet classified (SENT) waits for
	// hook evidence before it halts (research R8); the worker's submit check is
	// bounded by deliverTimeout, so a report still on its way lands within it.
	sentGrace = deliverTimeout
	// maxNameLen bounds an orchestrator's name (FR-105).
	maxNameLen = 64

	// --- SWI-18: worker lanes ---
	// maxLanes caps the lanes on one orchestrator (FR-302).
	maxLanes = 32
	// maxLaneDescription bounds a lane's description, in runes (FR-303).
	maxLaneDescription = 200
	// attachTicketTTL is how long the lanes picked at launch or attach wait for
	// the sandbox to be adopted (FR-306b).
	attachTicketTTL = 24 * time.Hour
	// removedMarkTTL is how long a removed sandbox's high-water mark is kept
	// once no listing of its host still shows it (FR-308c).
	removedMarkTTL = 24 * time.Hour
	// laneIDPrefix starts every minted lane id ("ln-" + 12 hex).
	laneIDPrefix = "ln-"
	// lanePushRetryMin is the first delay before a lane display push that
	// failed transiently while its host is up is tried again; it doubles to
	// linkRetryMax while the host keeps failing.
	lanePushRetryMin = 2 * time.Second
	// lanePushMaxAttempts bounds those retries per worker; its next lane
	// change, briefing refresh or reconnect push corrects a worker given up on.
	lanePushMaxAttempts = 6
	// ticketSkew tolerates clock skew between hosts when an attach ticket's
	// age is checked (research R11).
	ticketSkew = 10 * time.Minute
	// maxAttachTickets caps the unexpired attach tickets per orchestrator.
	maxAttachTickets = 256
)

// CapabilityLanes is advertised in DaemonInfo.capabilities by a daemon that
// serves worker lanes (FR-318a).
const CapabilityLanes = "lanes"
