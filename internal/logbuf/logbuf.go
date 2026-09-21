// Package logbuf holds the merged fleet log and the logic that makes it
// readable.
//
// Merging is what creates the hard problem. A Java stack trace is many
// consecutive logcat lines, and interleaving two devices' output shreds both
// of them: you get a frame from one phone, a frame from the other, and
// neither trace can be followed. So the buffer groups each trace back onto
// the line that started it, per device, and the UI can then show it as one
// collapsible entry regardless of what else was arriving at the time.
package logbuf

import (
	"strings"

	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

// Entry is one logical log record: a primary line plus any continuation
// lines (stack frames) that belong to it.
type Entry struct {
	Line fleet.Line
	// Frames are the continuation lines, with their original leading
	// whitespace trimmed.
	Frames []string
	// Expanded is view state, owned by the UI. It lives here so a trace stays
	// open as new lines arrive underneath it.
	Expanded bool
}

// IsTrace reports whether this entry carries a folded stack trace.
func (e *Entry) IsTrace() bool { return len(e.Frames) > 0 }

// Buffer is a bounded ring of entries.
type Buffer struct {
	entries []*Entry
	max     int
	// last is the most recent entry per device, so a continuation can find
	// the line it belongs to even when other devices logged in between.
	last map[string]*Entry
}

// New returns a Buffer holding at most max entries.
func New(max int) *Buffer {
	if max <= 0 {
		max = 1000
	}
	return &Buffer{max: max, last: make(map[string]*Entry)}
}

// Len is the number of entries held.
func (b *Buffer) Len() int { return len(b.entries) }

// Clear drops everything.
func (b *Buffer) Clear() {
	b.entries = nil
	b.last = make(map[string]*Entry)
}

// Append adds a line, folding it into the preceding entry when it is a
// continuation of that device's trace.
func (b *Buffer) Append(line fleet.Line) {
	if prev, ok := b.last[line.Serial]; ok && isContinuation(prev, line) {
		prev.Frames = append(prev.Frames, strings.TrimSpace(line.Text))
		return
	}

	entry := &Entry{Line: line}
	b.entries = append(b.entries, entry)
	b.last[line.Serial] = entry

	if len(b.entries) > b.max {
		dropped := b.entries[:len(b.entries)-b.max]
		b.entries = b.entries[len(b.entries)-b.max:]
		// Evicted entries must not stay reachable as continuation targets,
		// or a late frame would attach to an entry no longer in the buffer.
		for _, old := range dropped {
			if b.last[old.Line.Serial] == old {
				delete(b.last, old.Line.Serial)
			}
		}
	}
}

// isContinuation decides whether line extends prev. Both the device and the
// tag must match: frames from one device's crash must never be glued onto
// another's, and within a device the tag distinguishes a trace from
// unrelated chatter arriving between frames.
func isContinuation(prev *Entry, line fleet.Line) bool {
	if prev.Line.Serial != line.Serial || prev.Line.Tag != line.Tag {
		return false
	}
	if !looksLikeFrame(line.Text) {
		return false
	}
	// Only attach to something that could plausibly have thrown: a warning
	// or worse, or an entry already accumulating frames.
	return prev.IsTrace() || isThrowable(prev.Line.Level)
}

func looksLikeFrame(text string) bool {
	// Frames are indented in logcat; an unindented line is a new record.
	if text == "" || (text[0] != ' ' && text[0] != '\t') {
		return false
	}
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "at "):
		return true
	case strings.HasPrefix(trimmed, "Caused by:"):
		return true
	case strings.HasPrefix(trimmed, "Suppressed:"):
		return true
	case strings.HasPrefix(trimmed, "... ") && strings.HasSuffix(trimmed, " more"):
		return true
	}
	return false
}

func isThrowable(level byte) bool { return level == 'W' || level == 'E' || level == 'F' }

// levelRank orders severities for filtering. Lines we could not parse a level
// from rank lowest, so raising the minimum level hides them along with the
// verbose chatter they sit among.
func levelRank(level byte) int {
	switch level {
	case 'V':
		return 1
	case 'D':
		return 2
	case 'I':
		return 3
	case 'W':
		return 4
	case 'E':
		return 5
	case 'F':
		return 6
	}
	return 0
}

// Filter narrows the view.
type Filter struct {
	// Serial limits to one device; empty means the whole fleet.
	Serial string
	// Text is a case-insensitive substring matched against the message, the
	// tag, and any folded frames.
	Text string
	// MinLevel hides anything less severe. Zero keeps everything.
	MinLevel byte
}

// Matches reports whether an entry survives the filter.
func (f Filter) Matches(e *Entry) bool {
	if f.Serial != "" && e.Line.Serial != f.Serial {
		return false
	}
	if f.MinLevel != 0 && levelRank(e.Line.Level) < levelRank(f.MinLevel) {
		return false
	}
	if f.Text == "" {
		return true
	}

	needle := strings.ToLower(f.Text)
	if strings.Contains(strings.ToLower(e.Line.Text), needle) ||
		strings.Contains(strings.ToLower(e.Line.Tag), needle) {
		return true
	}
	// A trace must stay findable by anything inside it — searching for a
	// class name that only appears three frames down is the common case.
	for _, frame := range e.Frames {
		if strings.Contains(strings.ToLower(frame), needle) {
			return true
		}
	}
	return false
}

// View returns the entries passing f, oldest first.
func (b *Buffer) View(f Filter) []*Entry {
	out := make([]*Entry, 0, len(b.entries))
	for _, e := range b.entries {
		if f.Matches(e) {
			out = append(out, e)
		}
	}
	return out
}
