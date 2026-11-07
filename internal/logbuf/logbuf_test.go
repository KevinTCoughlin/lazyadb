package logbuf

import (
	"testing"

	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

func line(serial string, level byte, tag, text string) fleet.Line {
	return fleet.Line{Serial: serial, Level: level, Tag: tag, Text: text}
}

// The reason this package exists: two devices crashing at once interleave
// line by line, and a naive buffer leaves both traces unreadable.
func TestInterleavedTracesStayIntact(t *testing.T) {
	b := New(100)

	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("B", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.run(Foo.java:1)"))
	b.Append(line("B", 'E', "AndroidRuntime", "\tat com.b.Bar.run(Bar.java:9)"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.main(Foo.java:2)"))
	b.Append(line("B", 'E', "AndroidRuntime", "\t... 3 more"))

	entries := b.View(Filter{})
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (one trace per device): %+v", len(entries), entries)
	}

	a, bEntry := entries[0], entries[1]
	if a.Line.Serial != "A" || bEntry.Line.Serial != "B" {
		t.Fatalf("entries in wrong order: %q, %q", a.Line.Serial, bEntry.Line.Serial)
	}
	if len(a.Frames) != 2 {
		t.Errorf("device A frames = %+v, want 2", a.Frames)
	}
	if len(bEntry.Frames) != 2 {
		t.Errorf("device B frames = %+v, want 2", bEntry.Frames)
	}
	// Crucially, no frame crossed between devices.
	for _, f := range a.Frames {
		if f == "at com.b.Bar.run(Bar.java:9)" {
			t.Error("device B's frame attached to device A's trace")
		}
	}
}

func TestContinuationNeedsMatchingTag(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("A", 'E', "SomethingElse", "\tat com.other.Thing.run(T.java:1)"))

	if got := b.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2: a different tag is a different record", got)
	}
}

func TestUnindentedLineIsNotAFrame(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("A", 'E', "AndroidRuntime", "at the end of the day we shipped"))

	if got := b.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2: prose starting with \"at \" is not a stack frame", got)
	}
}

// Info-level chatter is not a throwable, so indented text under it is its own
// record rather than a frame.
func TestFramesOnlyAttachToPlausibleThrowables(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'I', "ActivityManager", "Start proc"))
	b.Append(line("A", 'I', "ActivityManager", "\tat com.a.Foo.run(Foo.java:1)"))

	if got := b.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
}

func TestCausedByAndMoreAttach(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.run(Foo.java:1)"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tCaused by: java.lang.NullPointerException"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tSuppressed: java.io.IOException"))
	b.Append(line("A", 'E', "AndroidRuntime", "\t... 12 more"))

	entries := b.View(Filter{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if len(entries[0].Frames) != 4 {
		t.Errorf("Frames = %+v, want 4", entries[0].Frames)
	}
	if !entries[0].IsTrace() {
		t.Error("IsTrace() = false")
	}
}

func TestEvictionDoesNotStrandContinuationTargets(t *testing.T) {
	b := New(2)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("B", 'I', "Tag", "one"))
	b.Append(line("C", 'I', "Tag", "two")) // evicts device A's entry

	// A late frame must not attach to an entry that is no longer in the
	// buffer, or it would vanish from the view entirely.
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.run(Foo.java:1)"))

	entries := b.View(Filter{})
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (cap)", len(entries))
	}
	last := entries[len(entries)-1]
	if last.Line.Serial != "A" {
		t.Fatalf("last entry serial = %q, want the orphaned frame kept as its own entry", last.Line.Serial)
	}
}

func TestBufferCap(t *testing.T) {
	b := New(3)
	for i := 0; i < 10; i++ {
		b.Append(line("A", 'I', "Tag", string(rune('a'+i))))
	}
	if got := b.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if got := b.View(Filter{})[0].Line.Text; got != "h" {
		t.Errorf("oldest kept = %q, want \"h\"", got)
	}
}

func TestFilterByDevice(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'I', "Tag", "from a"))
	b.Append(line("B", 'I', "Tag", "from b"))

	got := b.View(Filter{Serial: "B"})
	if len(got) != 1 || got[0].Line.Text != "from b" {
		t.Errorf("View(Serial: B) = %+v", got)
	}
}

func TestFilterByLevel(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'V', "Tag", "verbose"))
	b.Append(line("A", 'I', "Tag", "info"))
	b.Append(line("A", 'W', "Tag", "warn"))
	b.Append(line("A", 'E', "Tag", "error"))
	b.Append(line("A", 0, "", "--------- beginning of main"))

	got := b.View(Filter{MinLevel: 'W'})
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (warn and error): %+v", len(got), got)
	}
	for _, e := range got {
		if e.Line.Level != 'W' && e.Line.Level != 'E' {
			t.Errorf("level %q survived MinLevel W", e.Line.Level)
		}
	}
}

// Searching for a class name that only appears deep in a trace is the common
// case, so a filter has to see inside folded frames.
func TestFilterMatchesInsideFoldedFrames(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.example.deep.Widget.inflate(Widget.java:42)"))

	if got := b.View(Filter{Text: "widget"}); len(got) != 1 {
		t.Errorf("View(Text: widget) = %+v, want the trace whose frame matches", got)
	}
	if got := b.View(Filter{Text: "nothing-here"}); len(got) != 0 {
		t.Errorf("View(Text: nothing-here) = %+v, want none", got)
	}
}

func TestFilterMatchesTag(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'I', "ActivityManager", "start"))
	b.Append(line("A", 'I', "OtherTag", "start"))

	if got := b.View(Filter{Text: "activitymanager"}); len(got) != 1 {
		t.Errorf("tag filter = %+v, want 1", got)
	}
}

func TestClear(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.Clear()
	if b.Len() != 0 {
		t.Fatal("Clear did not empty the buffer")
	}
	// A frame arriving after a clear must not resurrect the cleared entry.
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.run(Foo.java:1)"))
	if b.Len() != 1 {
		t.Errorf("Len() = %d, want 1", b.Len())
	}
	if b.View(Filter{})[0].IsTrace() {
		t.Error("frame attached to a cleared entry")
	}
}

func pidLine(serial string, pid int, text string) fleet.Line {
	return fleet.Line{Serial: serial, Level: 'I', Tag: "T", PID: pid, Text: text}
}

func TestPIDFilter(t *testing.T) {
	b := New(100)
	b.Append(pidLine("A", 3121, "from the app"))
	b.Append(pidLine("A", 914, "from system_server"))
	b.Append(pidLine("A", 3150, "from the app's :remote process"))
	b.Append(line("A", 0, "", "--------- beginning of main"))

	got := b.View(Filter{PIDs: []int{3121}})
	if len(got) != 1 || got[0].Line.Text != "from the app" {
		t.Errorf("pid 3121 = %+v, want only the app's line", got)
	}

	got = b.View(Filter{PIDs: []int{3121, 3150}})
	if len(got) != 2 {
		t.Errorf("pids 3121,3150 matched %d entries, want 2", len(got))
	}

	// PID combines with text rather than replacing it.
	got = b.View(Filter{PIDs: []int{3121, 3150}, Text: "remote"})
	if len(got) != 1 || got[0].Line.PID != 3150 {
		t.Errorf("pid + text = %+v, want the :remote line", got)
	}

	if got := b.View(Filter{}); len(got) != 4 {
		t.Errorf("no PID filter kept %d entries, want 4 (including the pid-less line)", len(got))
	}
}

func TestParseQuery(t *testing.T) {
	tests := []struct {
		query string
		text  string
		pids  []int
	}{
		{"", "", nil},
		{"  NullPointer  ", "NullPointer", nil},
		{"two  spaces", "two  spaces", nil},
		{"pid:3121", "", []int{3121}},
		{"PID:3121 crash", "crash", []int{3121}},
		{"okhttp pid:3121 pid:3150 GET", "okhttp GET", []int{3121, 3150}},
		{"pid:abc", "pid:abc", nil},
		{"pid:0", "pid:0", nil},
		{"pid:", "pid:", nil},
	}
	for _, tt := range tests {
		got := ParseQuery(tt.query)
		if got.Text != tt.text {
			t.Errorf("ParseQuery(%q).Text = %q, want %q", tt.query, got.Text, tt.text)
		}
		if len(got.PIDs) != len(tt.pids) {
			t.Errorf("ParseQuery(%q).PIDs = %v, want %v", tt.query, got.PIDs, tt.pids)
			continue
		}
		for i := range tt.pids {
			if got.PIDs[i] != tt.pids[i] {
				t.Errorf("ParseQuery(%q).PIDs = %v, want %v", tt.query, got.PIDs, tt.pids)
			}
		}
	}
}

func TestMutedDeviceLinesAreDropped(t *testing.T) {
	b := New(100)
	b.SetMuted("noisy", true)

	b.Append(line("noisy", 'I', "Spam", "spam 1"))
	b.Append(line("quiet", 'I', "App", "useful"))
	b.Append(line("noisy", 'I', "Spam", "spam 2"))

	got := b.View(Filter{})
	if len(got) != 1 || got[0].Line.Serial != "quiet" {
		t.Fatalf("entries = %+v, want only the unmuted device's line", got)
	}
	if !b.Muted("noisy") || b.Muted("quiet") {
		t.Error("Muted() does not reflect SetMuted")
	}

	// Unmuting resumes from now on; what arrived while muted is gone.
	b.SetMuted("noisy", false)
	b.Append(line("noisy", 'I', "Spam", "spam 3"))
	if got := b.View(Filter{Serial: "noisy"}); len(got) != 1 || got[0].Line.Text != "spam 3" {
		t.Errorf("after unmute = %+v, want only the line logged after unmuting", got)
	}
}

// Frames that arrive after an unmute must not fold onto a trace from before
// the mute: the lines in between were dropped.
func TestUnmuteDoesNotFoldOntoPreMuteTrace(t *testing.T) {
	b := New(100)
	b.Append(line("A", 'E', "AndroidRuntime", "FATAL EXCEPTION: main"))
	b.SetMuted("A", true)
	b.SetMuted("A", false)
	b.Append(line("A", 'E', "AndroidRuntime", "\tat com.a.Foo.run(Foo.java:1)"))

	if got := b.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2: a post-unmute frame is a new record", got)
	}
}

func TestMuteSurvivesClear(t *testing.T) {
	b := New(100)
	b.SetMuted("A", true)
	b.Clear()
	b.Append(line("A", 'I', "T", "x"))
	if b.Len() != 0 {
		t.Error("Clear() unmuted the device")
	}
}
