package job

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogBufferBoundsBytesAndLines(t *testing.T) {
	buffer := newLogBuffer(96, 8)
	for range 100 {
		buffer.append(LogStreamStdout, "")
	}
	if buffer.bytes > 96 {
		t.Fatalf("retained bytes = %d, want <= 96", buffer.bytes)
	}
	if buffer.count > 2 {
		t.Fatalf("retained empty lines = %d, want <= 2 with metadata accounting", buffer.count)
	}

	entry := buffer.append(LogStreamStderr, strings.Repeat("界", 100))
	if len(entry.Line) > 8 {
		t.Fatalf("line bytes = %d, want <= 8", len(entry.Line))
	}
	if !utf8.ValidString(entry.Line) {
		t.Fatalf("line is not valid UTF-8: %q", entry.Line)
	}
	if buffer.bytes > 96 {
		t.Fatalf("retained bytes after large line = %d, want <= 96", buffer.bytes)
	}
}

func TestUTF8BoundsInvalidAndMultibyteInput(t *testing.T) {
	invalid := strings.Repeat(string([]byte{0xff, 0x80}), 100)
	for name, value := range map[string]string{
		"prefix": utf8Prefix(invalid+strings.Repeat("界", 100), 17),
		"suffix": utf8Suffix(strings.Repeat("界", 100)+invalid, 17),
		"tail":   appendUTF8Tail("previous\n", strings.Repeat("界", 100)+invalid, 17),
	} {
		t.Run(name, func(t *testing.T) {
			if len(value) > 17 {
				t.Fatalf("bytes = %d, want <= 17", len(value))
			}
			if !utf8.ValidString(value) {
				t.Fatalf("value is not valid UTF-8: %q", value)
			}
		})
	}
}
