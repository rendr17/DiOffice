// Package runtimeevents ingests the OpenCode server event stream for each
// RUNNING session and normalizes provider activity into canonical durable
// facts. Only registered event types are emitted; raw reasoning, streaming
// deltas, and provider internals are never persisted.
package runtimeevents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Fact is one normalized provider observation ready to persist.
type Fact struct {
	EventType string
	Data      map[string]any
	// DedupeKey uniquely identifies this fact within the agent session so a
	// replayed provider event never persists twice.
	DedupeKey string
	// Complete marks the session turn finished (session.idle observed);
	// the ingester persists session.completed and ends the row.
	Complete bool
	// Failed, when non-empty, is the safe error code for a terminal session
	// failure observed on the stream (session.error).
	Failed string
}

// providerEvent is the generic OpenCode SSE envelope: {"type","properties"}.
type providerEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// part is the subset of an OpenCode message part the normalizer reads.
type part struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Tool      string `json:"tool"`
	Text      string `json:"text"`
	State     struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Metadata struct {
			Exit *int `json:"exit"`
		} `json:"metadata"`
		Time struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
	Time struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"time"`
}

const (
	summaryLimit    = 4000
	displayCmdLimit = 2048
	pathLimit       = 1024
)

func clip(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// commandID derives a deterministic UUID-shaped id for a tool call so
// command.started and command.completed correlate across replays.
func commandID(partID string) string {
	sum := sha256.Sum256([]byte("opencode-command:" + partID))
	hexed := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s-%s-4%s-8%s-%s",
		hexed[0:8], hexed[8:12], hexed[13:16], hexed[17:20], hexed[20:32])
}

func classifyCommand(argv string) string {
	fields := strings.Fields(argv)
	joined := " " + strings.ToLower(strings.Join(fields, " ")) + " "
	switch {
	case strings.Contains(joined, " test") || strings.Contains(joined, " vitest") ||
		strings.Contains(joined, " jest") || strings.Contains(joined, " pytest") ||
		strings.Contains(joined, " playwright") || strings.Contains(joined, " lint") ||
		strings.Contains(joined, " typecheck") || strings.Contains(joined, " tsc"):
		return "check"
	case strings.Contains(joined, " install") || strings.Contains(joined, "pnpm i") ||
		strings.Contains(joined, "npm i") || strings.Contains(joined, "pip install") ||
		strings.Contains(joined, "go get") || strings.Contains(joined, "go mod"):
		return "install"
	case strings.HasPrefix(joined, " git ") || strings.Contains(joined, " git "):
		return "git"
	case strings.Contains(joined, " dev") || strings.Contains(joined, " serve") ||
		strings.Contains(joined, " start") || strings.Contains(joined, " preview"):
		return "preview"
	default:
		return "other"
	}
}

func toolPath(input json.RawMessage) string {
	var fields struct {
		FilePath string `json:"filePath"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	path := fields.FilePath
	if path == "" {
		path = fields.Path
	}
	if path == "" {
		path = fields.Pattern
	}
	return clip(path, pathLimit)
}

func bashCommand(input json.RawMessage) string {
	var fields struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	return clip(fields.Command, displayCmdLimit)
}

func fileOperation(tool string) string {
	switch tool {
	case "write":
		return "created"
	case "read", "glob", "grep", "list":
		return "read"
	default:
		return "modified"
	}
}

func durationMs(start, end float64) int64 {
	if start <= 0 || end <= start {
		return 0
	}
	return int64((end - start) * 1000)
}

// normalizeToolPart maps a completed/running tool call to canonical command
// or file facts. Reasoning, snapshot, and non-tool part types return nothing.
func normalizeToolPart(p part) []Fact {
	dedupeBase := "part:" + p.ID
	switch {
	case p.Tool == "bash" || p.Tool == "shell":
		command := bashCommand(p.State.Input)
		if command == "" {
			return nil
		}
		switch p.State.Status {
		case "running":
			return []Fact{{
				EventType: "command.started",
				Data: map[string]any{
					"commandId":      commandID(p.ID),
					"displayCommand": command,
					"classification": classifyCommand(command),
				},
				DedupeKey: dedupeBase + ":start",
			}}
		case "completed":
			exit := 0
			if p.State.Metadata.Exit != nil {
				exit = *p.State.Metadata.Exit
			}
			if exit != 0 {
				return []Fact{{
					EventType: "command.failed",
					Data: map[string]any{
						"commandId":  commandID(p.ID),
						"errorCode":  "exit_nonzero",
						"durationMs": durationMs(p.State.Time.Start, p.State.Time.End),
					},
					DedupeKey: dedupeBase + ":end",
				}}
			}
			return []Fact{{
				EventType: "command.completed",
				Data: map[string]any{
					"commandId":  commandID(p.ID),
					"exitCode":   exit,
					"durationMs": durationMs(p.State.Time.Start, p.State.Time.End),
				},
				DedupeKey: dedupeBase + ":end",
			}}
		case "error":
			return []Fact{{
				EventType: "command.failed",
				Data: map[string]any{
					"commandId":  commandID(p.ID),
					"errorCode":  "tool_error",
					"durationMs": durationMs(p.State.Time.Start, p.State.Time.End),
				},
				DedupeKey: dedupeBase + ":end",
			}}
		}
	case p.Tool == "write" || p.Tool == "edit" || p.Tool == "patch" ||
		p.Tool == "read" || p.Tool == "glob" || p.Tool == "grep" || p.Tool == "list":
		if p.State.Status != "completed" {
			return nil
		}
		path := toolPath(p.State.Input)
		if path == "" || !safeRelativePath(path) {
			return nil
		}
		return []Fact{{
			EventType: "file.activity",
			Data: map[string]any{
				"operation": fileOperation(p.Tool),
				"path":      path,
			},
			DedupeKey: dedupeBase + ":file",
		}}
	}
	return nil
}

// safeRelativePath mirrors the file.activity contract: relative, repo-local,
// no traversal, no absolute or drive-qualified paths.
func safeRelativePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") ||
		(len(path) > 1 && path[1] == ':') || strings.HasPrefix(path, `\\`) {
		return false
	}
	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

// Normalize maps one provider SSE frame to canonical facts. Unknown and
// unsupported event types return nil — they are noise, not facts.
func Normalize(frame []byte) []Fact {
	var evt providerEvent
	if err := json.Unmarshal(frame, &evt); err != nil {
		return nil
	}
	switch evt.Type {
	case "message.part.updated":
		var props struct {
			Part part `json:"part"`
		}
		if err := json.Unmarshal(evt.Properties, &props); err != nil || props.Part.ID == "" {
			return nil
		}
		p := props.Part
		switch p.Type {
		case "text":
			if p.Time.End <= 0 {
				return nil // still streaming
			}
			summary := clip(p.Text, summaryLimit)
			if summary == "" {
				return nil
			}
			return []Fact{{
				EventType: "agent.message",
				Data:      map[string]any{"kind": "progress", "summary": summary},
				DedupeKey: "part:" + p.ID + ":text",
			}}
		case "tool":
			return normalizeToolPart(p)
		default:
			return nil // reasoning, step markers, snapshots: never persisted
		}
	case "session.idle":
		return []Fact{{
			EventType: "session.completed",
			Data:      map[string]any{"result": "handoff_ready"},
			DedupeKey: "session:idle",
			Complete:  true,
		}}
	case "session.error":
		return []Fact{{
			EventType: "session.failed",
			Data: map[string]any{
				"errorCode": "runtime_session_error", "retryable": true,
			},
			DedupeKey: "session:error",
			Failed:    "runtime_session_error",
		}}
	default:
		return nil
	}
}
