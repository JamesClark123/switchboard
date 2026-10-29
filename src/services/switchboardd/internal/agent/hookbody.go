package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// Reading a hook's forwarded input (SWI-17, research R13). Every hook now
// forwards Claude Code's hook input, and a PreToolUse body carries the whole
// tool_input, which can be megabytes (a Write of a large file). The body is
// read as a stream of JSON tokens: only the top-level fields the daemon needs
// are kept, every other value is skipped byte by byte without being buffered,
// and nothing past maxHookRead is read. A body that is not a JSON object, or
// ends early, yields the fields found before that point.

// maxHookRead is the hard cap on a hook body read.
const maxHookRead = 16 << 20

// maxHookField bounds a kept field's value (a session id, a source, the
// background task list); a larger one is skipped as if absent.
const maxHookField = 64 << 10

var errHookBody = errors.New("malformed hook body")

// errHookDone stops the read once every field the event needs was seen.
var errHookDone = errors.New("hook body read")

// readHookInput reads the fields of hookInput from a hook body. It stops once
// the session id, the event name and that event's own field (hookInputDone)
// were seen: Claude Code sends them ahead of a PreToolUse's tool_input.
func readHookInput(r io.Reader) hookInput {
	sk := &skim{r: bufio.NewReader(io.LimitReader(r, maxHookRead))}
	var in hookInput
	seen := map[string]bool{}
	_ = sk.object(func(key string) error {
		switch key {
		case "hook_event_name":
			in.HookEventName = sk.str()
		case "session_id":
			in.SessionID = sk.str()
		case "notification_type":
			in.NotificationType = sk.str()
		case "source":
			in.Source = sk.str()
		case "background_tasks":
			raw, whole, err := sk.capture()
			if err != nil {
				return err
			}
			switch {
			case !whole:
				// Too many to read: some are surely still running.
				in.BackgroundTasks = []backgroundTask{{Status: "running"}}
			default:
				_ = json.Unmarshal(raw, &in.BackgroundTasks)
			}
		default:
			return sk.skipValue(nil)
		}
		if sk.err != nil {
			return sk.err
		}
		if seen[key] = true; hookInputDone(in.HookEventName, seen) {
			return errHookDone
		}
		return nil
	})
	return in
}

// hookInputDone reports whether every field readHookInput keeps for event has
// been seen.
func hookInputDone(event string, seen map[string]bool) bool {
	if !seen["session_id"] || !seen["hook_event_name"] {
		return false
	}
	switch event {
	case "SessionStart":
		return seen["source"]
	case "Notification":
		return seen["notification_type"]
	case "Stop":
		return seen["background_tasks"]
	}
	return true
}

// skim is a minimal forward-only JSON reader.
type skim struct {
	r   *bufio.Reader
	err error // a str() failure, surfaced by the object loop
}

// next returns the next non-whitespace byte.
func (s *skim) next() (byte, error) {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c, nil
	}
}

// object reads a top-level object, calling fn with each key positioned at its
// value; fn must consume the value.
func (s *skim) object(fn func(key string) error) error {
	if c, err := s.next(); err != nil || c != '{' {
		return errHookBody
	}
	for {
		c, err := s.next()
		if err != nil {
			return err
		}
		if c == '}' {
			return nil
		}
		if c != '"' {
			return errHookBody
		}
		_ = s.r.UnreadByte()
		raw, whole, err := s.capture()
		if err != nil {
			return err
		}
		var key string
		if whole {
			_ = json.Unmarshal(raw, &key)
		}
		if c, err := s.next(); err != nil || c != ':' {
			return errHookBody
		}
		if err := fn(key); err != nil {
			return err
		}
		switch c, err := s.next(); {
		case err != nil:
			return err
		case c == '}':
			return nil
		case c != ',':
			return errHookBody
		}
	}
}

// str reads a string value (empty when it is not one, or is too long).
func (s *skim) str() string {
	raw, whole, err := s.capture()
	if err != nil {
		s.err = err
		return ""
	}
	var v string
	if !whole || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v
}

// capture reads one value, keeping its raw bytes up to maxHookField; whole is
// false when it was longer (the rest is skipped, not kept).
func (s *skim) capture() (raw []byte, whole bool, err error) {
	rec := &valueRecorder{}
	if err := s.skipValue(rec); err != nil {
		return nil, false, err
	}
	return rec.buf, !rec.over, nil
}

// valueRecorder keeps the bytes of a value up to maxHookField.
type valueRecorder struct {
	buf  []byte
	over bool
}

func (r *valueRecorder) add(c byte) {
	if r == nil || r.over {
		return
	}
	if len(r.buf) >= maxHookField {
		r.over, r.buf = true, nil
		return
	}
	r.buf = append(r.buf, c)
}

// skipValue reads past one JSON value, recording it into rec (if any).
func (s *skim) skipValue(rec *valueRecorder) error {
	c, err := s.next()
	if err != nil {
		return err
	}
	rec.add(c)
	switch c {
	case '"':
		return s.skipString(rec)
	case '{', '[':
		depth := 1
		for depth > 0 {
			b, err := s.r.ReadByte()
			if err != nil {
				return err
			}
			rec.add(b)
			switch b {
			case '"':
				if err := s.skipString(rec); err != nil {
					return err
				}
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		return nil
	case '}', ']', ',', ':':
		return errHookBody
	}
	// A literal: a number, true, false or null.
	for {
		b, err := s.r.ReadByte()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch b {
		case ',', '}', ']', ' ', '\t', '\r', '\n':
			_ = s.r.UnreadByte()
			return nil
		}
		rec.add(b)
	}
}

// skipString reads past the rest of a string whose opening quote was read.
func (s *skim) skipString(rec *valueRecorder) error {
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return err
		}
		rec.add(b)
		switch b {
		case '\\':
			e, err := s.r.ReadByte()
			if err != nil {
				return err
			}
			rec.add(e)
		case '"':
			return nil
		}
	}
}
