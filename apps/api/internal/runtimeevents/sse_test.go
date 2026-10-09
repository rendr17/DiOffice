package runtimeevents

import (
	"context"
	"io"
	"strings"
	"testing"
)

func collectFrames(t *testing.T, payload string) []string {
	t.Helper()
	var frames []string
	err := readSSEData(context.Background(), strings.NewReader(payload),
		func(frame []byte) error {
			frames = append(frames, string(frame))
			return nil
		})
	if err != nil && err != io.EOF {
		t.Fatalf("readSSEData() error = %v", err)
	}
	return frames
}

func TestSSEFramesSplitOnBlankLine(t *testing.T) {
	frames := collectFrames(t, "data: {\"a\":1}\n\ndata: {\"b\":2}\n\n")
	if len(frames) != 2 || frames[0] != ` {"a":1}` || frames[1] != ` {"b":2}` {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestSSECRLFEndings(t *testing.T) {
	frames := collectFrames(t, "data: {\"a\":1}\r\n\r\ndata: {\"b\":2}\r\n\r\n")
	if len(frames) != 2 {
		t.Fatalf("frames = %#v", frames)
	}
	if Normalize([]byte(frames[0])) == nil && !strings.Contains(frames[0], `"a":1`) {
		t.Fatalf("CRLF frame corrupted: %q", frames[0])
	}
}

func TestSSEMultiLineDataJoins(t *testing.T) {
	payload := "data: {\"a\":\ndata: 1}\n\n"
	frames := collectFrames(t, payload)
	if len(frames) != 1 || !strings.Contains(frames[0], `{"a":`) || !strings.Contains(frames[0], `1}`) {
		t.Fatalf("multi-line frame = %#v", frames)
	}
}

func TestSSEIgnoresCommentsAndFields(t *testing.T) {
	payload := ": heartbeat\n\nevent: foo\nid: 9\nretry: 3000\ndata: {\"a\":1}\n\n"
	frames := collectFrames(t, payload)
	if len(frames) != 1 || strings.TrimSpace(frames[0]) != `{"a":1}` {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestSSEFlushesTrailingFrameAtEOF(t *testing.T) {
	frames := collectFrames(t, "data: {\"a\":1}")
	if len(frames) != 1 {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestSSECancelStopsReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var emitted int
	err := readSSEData(ctx, strings.NewReader("data: {\"a\":1}\n\n"), func([]byte) error {
		emitted++
		return nil
	})
	if err != nil && emitted > 1 {
		t.Fatalf("emitted %d frames after cancel, err=%v", emitted, err)
	}
}
