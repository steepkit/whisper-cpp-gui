package job

import (
	"strings"
	"unicode/utf8"
)

const (
	// A LogEntry contains two string headers (32 bytes on supported 64-bit
	// targets). Counting that fixed cost prevents many empty lines from
	// bypassing the configured byte budget.
	logEntryMetadataBytes      = 32
	logEntrySeparatorBytes     = 1
	minimumLogCapacityBytes    = logEntryMetadataBytes + logEntrySeparatorBytes + 1
	minimumStderrCapacityBytes = 1
)

type logBuffer struct {
	capacity int
	lineMax  int
	bytes    int
	entries  []LogEntry
	head     int
	count    int
}

func newLogBuffer(capacity, lineMax int) logBuffer {
	return logBuffer{capacity: capacity, lineMax: lineMax}
}

func (b *logBuffer) append(stream LogStream, line string) LogEntry {
	if stream != LogStreamStderr {
		stream = LogStreamStdout
	}

	maxLine := min(b.lineMax, b.capacity-logEntryMetadataBytes-logEntrySeparatorBytes)
	line = utf8Prefix(line, maxLine)
	entry := LogEntry{Stream: stream, Line: line}
	size := retainedLogEntryBytes(line)

	for b.count > 0 && b.bytes+size > b.capacity {
		b.bytes -= retainedLogEntryBytes(b.entries[b.head].Line)
		b.entries[b.head] = LogEntry{}
		b.head = (b.head + 1) % len(b.entries)
		b.count--
	}

	b.growIfFull()
	index := (b.head + b.count) % len(b.entries)
	b.entries[index] = entry
	b.count++
	b.bytes += size
	return entry
}

func (b *logBuffer) snapshot() []LogEntry {
	if b.count == 0 {
		return nil
	}
	result := make([]LogEntry, b.count)
	for index := range b.count {
		result[index] = b.entries[(b.head+index)%len(b.entries)]
	}
	return result
}

func (b *logBuffer) growIfFull() {
	if b.count < len(b.entries) {
		return
	}
	size := len(b.entries) * 2
	if size == 0 {
		size = 1
	}
	maxEntries := b.capacity / (logEntryMetadataBytes + logEntrySeparatorBytes)
	if size > maxEntries {
		size = maxEntries
	}
	entries := make([]LogEntry, size)
	for index := range b.count {
		entries[index] = b.entries[(b.head+index)%len(b.entries)]
	}
	b.entries = entries
	b.head = 0
}

func retainedLogEntryBytes(line string) int {
	return logEntryMetadataBytes + logEntrySeparatorBytes + len(line)
}

func utf8Prefix(value string, capacity int) string {
	if capacity <= 0 || value == "" {
		return ""
	}
	if len(value) <= capacity && utf8.ValidString(value) {
		return strings.Clone(value)
	}

	var result strings.Builder
	result.Grow(min(capacity, len(value)))
	for _, r := range value {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = utf8.RuneLen(utf8.RuneError)
			r = utf8.RuneError
		}
		if result.Len()+size > capacity {
			break
		}
		result.WriteRune(r)
	}
	return result.String()
}

func utf8Suffix(value string, capacity int) string {
	if capacity <= 0 || value == "" {
		return ""
	}
	if len(value) <= capacity && utf8.ValidString(value) {
		return strings.Clone(value)
	}

	start := len(value) - min(capacity, len(value))
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	suffix := strings.ToValidUTF8(value[start:], string(utf8.RuneError))
	if len(suffix) <= capacity {
		return strings.Clone(suffix)
	}

	start = len(suffix) - capacity
	for start < len(suffix) && !utf8.RuneStart(suffix[start]) {
		start++
	}
	return strings.Clone(suffix[start:])
}

func appendUTF8Tail(current, line string, capacity int) string {
	if capacity <= 0 {
		return ""
	}
	if capacity == 1 {
		return "\n"
	}
	addition := utf8Suffix(line, capacity-1) + "\n"
	if len(addition) >= capacity {
		return addition
	}

	keep := capacity - len(addition)
	current = utf8Suffix(current, keep)
	var result strings.Builder
	result.Grow(len(current) + len(addition))
	result.WriteString(current)
	result.WriteString(addition)
	return result.String()
}
