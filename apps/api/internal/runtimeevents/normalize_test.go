package runtimeevents

import (
	"strings"
	"testing"
)

func normalizeOne(t *testing.T, frame string) []Fact {
	t.Helper()
	facts := Normalize([]byte(frame))
	return facts
}

func TestNormalizeTextPartCompletedBecomesAgentMessage(t *testing.T) {
	facts := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"p1","sessionID":"s1","type":"text",
		"text":"Implemented the panel and ran smoke checks.",
		"time":{"start":1,"end":2}}}}`)
	if len(facts) != 1 || facts[0].EventType != "agent.message" {
		t.Fatalf("facts = %+v", facts)
	}
	if facts[0].Data["kind"] != "progress" ||
		!strings.Contains(facts[0].Data["summary"].(string), "panel") {
		t.Fatalf("data = %+v", facts[0].Data)
	}
	if facts[0].DedupeKey != "part:p1:text" {
		t.Fatalf("dedupe = %q", facts[0].DedupeKey)
	}
}

func TestNormalizeStreamingTextIsSkipped(t *testing.T) {
	facts := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"p1","sessionID":"s1","type":"text","text":"partial",
		"time":{"start":1}}}}`)
	if len(facts) != 0 {
		t.Fatalf("streaming text should not emit, got %+v", facts)
	}
}

func TestNormalizeBashLifecycle(t *testing.T) {
	started := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"t1","sessionID":"s1","type":"tool","tool":"bash",
		"state":{"status":"running","input":{"command":"pnpm test"}}}}}`)
	if len(started) != 1 || started[0].EventType != "command.started" {
		t.Fatalf("started = %+v", started)
	}
	if started[0].Data["classification"] != "check" ||
		started[0].Data["displayCommand"] != "pnpm test" {
		t.Fatalf("started data = %+v", started[0].Data)
	}
	completed := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"t1","sessionID":"s1","type":"tool","tool":"bash",
		"state":{"status":"completed","input":{"command":"pnpm test"},
			"metadata":{"exit":0},"time":{"start":10,"end":12.5}}}}}`)
	if len(completed) != 1 || completed[0].EventType != "command.completed" {
		t.Fatalf("completed = %+v", completed)
	}
	if completed[0].Data["exitCode"] != 0 || completed[0].Data["durationMs"] != int64(2500) {
		t.Fatalf("completed data = %+v", completed[0].Data)
	}
	if completed[0].Data["commandId"] != started[0].Data["commandId"] {
		t.Fatal("commandId must correlate started and completed")
	}
	failed := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"t1","sessionID":"s1","type":"tool","tool":"bash",
		"state":{"status":"completed","input":{"command":"pnpm test"},
			"metadata":{"exit":2},"time":{"start":10,"end":11}}}}}`)
	if len(failed) != 1 || failed[0].EventType != "command.failed" ||
		failed[0].Data["errorCode"] != "exit_nonzero" {
		t.Fatalf("failed = %+v", failed)
	}
}

func TestNormalizeFileToolActivity(t *testing.T) {
	facts := normalizeOne(t, `{"type":"message.part.updated","properties":{"part":{
		"id":"f1","sessionID":"s1","type":"tool","tool":"edit",
		"state":{"status":"completed","input":{"filePath":"src/App.tsx"}}}}}`)
	if len(facts) != 1 || facts[0].EventType != "file.activity" {
		t.Fatalf("facts = %+v", facts)
	}
	if facts[0].Data["operation"] != "modified" || facts[0].Data["path"] != "src/App.tsx" {
		t.Fatalf("data = %+v", facts[0].Data)
	}
}

func TestNormalizeRejectsUnsafePathsAndUnknownTypes(t *testing.T) {
	for _, frame := range []string{
		`{"type":"message.part.updated","properties":{"part":{"id":"x","type":"tool","tool":"edit",
			"state":{"status":"completed","input":{"filePath":"../secrets.txt"}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"x","type":"tool","tool":"edit",
			"state":{"status":"completed","input":{"filePath":"/etc/passwd"}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"x","type":"reasoning","text":"hidden"}}}`,
		`{"type":"lsp.updated","properties":{}}`,
		`not json`,
		`{"type":"message.part.updated","properties":{"part":{"type":"tool","tool":"bash",
			"state":{"status":"running","input":{}}}}}`,
	} {
		if facts := Normalize([]byte(frame)); len(facts) != 0 {
			t.Fatalf("frame should emit nothing: %s → %+v", frame, facts)
		}
	}
}

func TestNormalizeSessionTerminalEvents(t *testing.T) {
	idle := normalizeOne(t, `{"type":"session.idle","properties":{"sessionID":"s1"}}`)
	if len(idle) != 1 || idle[0].EventType != "session.completed" || !idle[0].Complete {
		t.Fatalf("idle = %+v", idle)
	}
	failed := normalizeOne(t, `{"type":"session.error","properties":{"sessionID":"s1","error":{"message":"boom"}}}`)
	if len(failed) != 1 || failed[0].EventType != "session.failed" ||
		failed[0].Failed != "runtime_session_error" ||
		failed[0].Data["retryable"] != true {
		t.Fatalf("error = %+v", failed)
	}
}
