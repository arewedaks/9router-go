package log

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// A log line carrying an invalid byte sequence used to make the whole
// console-log response unmarshalable, so one bad line emptied the Console Log
// tab. Sanitising at captureConsole keeps the buffer marshalable.
func TestCaptureConsoleSanitisesInvalidUTF8(t *testing.T) {
	ClearConsoleLogs()
	defer ClearConsoleLogs()

	// Half of the 2-byte µ (U+00B5), exactly what a byte-offset cut produces.
	bad := "INF [request] GET /models duration=17.2\xb5s"
	bad = bad[:len(bad)-1]
	captureConsole(bad)
	captureConsole("plain ascii line")
	captureConsole("valid \u00b5 and \u00e9 and \u65e5\u672c\u8a9e")

	if _, err := json.Marshal(map[string]any{"success": true, "logs": ConsoleLogs()}); err != nil {
		t.Fatalf("console logs failed to marshal: %v", err)
	}
	lines := ConsoleLogs()
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if strings.Contains(lines[0], "\xb5") {
		t.Errorf("invalid byte survived: %q", lines[0])
	}
	if !strings.Contains(lines[2], "\u65e5\u672c\u8a9e") || !strings.Contains(lines[2], "\u00b5") {
		t.Errorf("valid multi-byte content altered: %q", lines[2])
	}
	if lines[1] != "plain ascii line" {
		t.Errorf("ascii line changed: %q", lines[1])
	}
}
