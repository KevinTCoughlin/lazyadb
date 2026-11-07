package fleet

import "testing"

func TestParseLine(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		level byte
		tag   string
		pid   int
		text  string
	}{
		{
			name:  "brief format",
			raw:   "I/ActivityManager(  914): Start proc com.example",
			level: 'I',
			tag:   "ActivityManager",
			pid:   914,
			text:  "Start proc com.example",
		},
		{
			name:  "error level",
			raw:   "E/AndroidRuntime( 3121): FATAL EXCEPTION: main",
			level: 'E',
			tag:   "AndroidRuntime",
			pid:   3121,
			text:  "FATAL EXCEPTION: main",
		},
		{
			name:  "unpadded five-digit pid",
			raw:   "D/OkHttp(12345): --> GET https://example.com",
			level: 'D',
			tag:   "OkHttp",
			pid:   12345,
			text:  "--> GET https://example.com",
		},
		{
			name:  "tag padded with spaces before the pid",
			raw:   "W/System.err  ( 7001): at Foo",
			level: 'W',
			tag:   "System.err",
			pid:   7001,
			text:  "at Foo",
		},
		{
			name:  "tag containing a parenthesis keeps it",
			raw:   "I/Weird(Tag)(  42): hello",
			level: 'I',
			tag:   "Weird(Tag)",
			pid:   42,
			text:  "hello",
		},
		{
			name:  "no pid group",
			raw:   "I/NoPid: hello",
			level: 'I',
			tag:   "NoPid",
			pid:   0,
			text:  "hello",
		},
		{
			name:  "non-numeric pid is ignored",
			raw:   "I/Tag( abc): hello",
			level: 'I',
			tag:   "Tag",
			pid:   0,
			text:  "hello",
		},
		{
			name:  "unparsed line is kept verbatim",
			raw:   "--------- beginning of main",
			level: 0,
			tag:   "",
			text:  "--------- beginning of main",
		},
		{
			name:  "unknown level is kept verbatim",
			raw:   "X/Weird( 1): something",
			level: 0,
			tag:   "",
			text:  "X/Weird( 1): something",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLine("serial123", tt.raw)
			if got.Serial != "serial123" {
				t.Errorf("Serial = %q", got.Serial)
			}
			if got.Level != tt.level {
				t.Errorf("Level = %q, want %q", got.Level, tt.level)
			}
			if got.Tag != tt.tag {
				t.Errorf("Tag = %q, want %q", got.Tag, tt.tag)
			}
			if got.PID != tt.pid {
				t.Errorf("PID = %d, want %d", got.PID, tt.pid)
			}
			if got.Text != tt.text {
				t.Errorf("Text = %q, want %q", got.Text, tt.text)
			}
		})
	}
}

func TestEmitDropsWhenFull(t *testing.T) {
	f := New(nil, 1)
	f.emit(Line{Serial: "a"})
	f.emit(Line{Serial: "b"})

	if got := f.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1", got)
	}
	if line := <-f.Lines(); line.Serial != "a" {
		t.Errorf("buffered line = %q, want the first one", line.Serial)
	}
}
