package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func eventSchema(t *testing.T) map[string]any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../../docs/schemas/event-envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func schemaExample(rule map[string]any, defs map[string]any) any {
	if ref, ok := rule["$ref"].(string); ok {
		return schemaExample(defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any), defs)
	}
	if value, ok := rule["const"]; ok {
		return value
	}
	if values, ok := rule["enum"].([]any); ok {
		return values[0]
	}
	if choices, ok := rule["oneOf"].([]any); ok {
		return schemaExample(choices[0].(map[string]any), defs)
	}
	kind := rule["type"]
	if values, ok := kind.([]any); ok {
		kind = values[0]
	}
	switch kind {
	case "object":
		value := map[string]any{}
		properties := rule["properties"].(map[string]any)
		for _, field := range rule["required"].([]any) {
			key := field.(string)
			value[key] = schemaExample(properties[key].(map[string]any), defs)
		}
		return value
	case "array":
		items := []any{}
		if minimum, ok := rule["minItems"].(float64); ok && minimum > 0 {
			for i := 0; i < int(minimum); i++ {
				items = append(items, schemaExample(rule["items"].(map[string]any), defs))
			}
		}
		return items
	case "integer":
		if minimum, ok := rule["minimum"]; ok {
			return minimum
		}
		return 1
	case "boolean":
		return true
	case "null":
		return nil
	case "string":
		switch rule["format"] {
		case "uuid":
			return "00000000-0000-4000-8000-000000000001"
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "uri":
			return "https://github.com/fixture/repo/pull/1"
		}
		if pattern, ok := rule["pattern"].(string); ok {
			if strings.Contains(pattern, "64") && !strings.Contains(pattern, "40") {
				return strings.Repeat("a", 64)
			}
			if strings.Contains(pattern, "40") {
				return strings.Repeat("a", 40)
			}
			if strings.HasPrefix(pattern, "^/") {
				return "/fixture"
			}
			return "src/fixture.ts"
		}
		return "safe fixture"
	}
	panic("unsupported fixture rule")
}

func TestSanitizeDataCoversCanonicalRegistry(t *testing.T) {
	schema := eventSchema(t)
	defs := schema["$defs"].(map[string]any)
	registry := defs["eventData"].(map[string]any)
	for eventType, spec := range registry {
		t.Run(eventType, func(t *testing.T) {
			rule := spec.(map[string]any)
			data := schemaExample(rule, defs).(map[string]any)
			data["rawProviderOutput"] = map[string]string{"secret": "never delivered"}
			data["objectKey"] = "private-object"
			raw, _ := json.Marshal(data)
			projected, err := SanitizeData(eventType, raw)
			if err != nil {
				t.Fatalf("canonical %s payload rejected: %v", eventType, err)
			}
			var fields map[string]any
			if err := json.Unmarshal(projected, &fields); err != nil {
				t.Fatal(err)
			}
			for key := range fields {
				if _, ok := rule["properties"].(map[string]any)[key]; !ok {
					t.Errorf("noncanonical field %s", key)
				}
			}
			for _, key := range rule["required"].([]any) {
				if _, ok := fields[key.(string)]; !ok {
					t.Errorf("required field %s missing", key)
				}
			}
		})
	}
}

func TestPayloadContractMatchesAuthoritativeSchema(t *testing.T) {
	var compiled map[string]any
	if err := json.Unmarshal([]byte(payloadContractJSON), &compiled); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(compiled, eventSchema(t)["$defs"]) {
		t.Fatal("compiled event contract drifted from authoritative schema")
	}
}

func TestSanitizeDataPreservesSafeStructuredPathsAndMaximumTaskCriteria(t *testing.T) {
	for _, test := range []struct{ kind, data, expected string }{
		{"file.activity", `{"operation":"read","path":"src/internal/config.ts"}`, "src/internal/config.ts"},
		{"preview.failed", `{"route":"/internal/preview","failureCode":"safe"}`, "/internal/preview"},
		{"pull_request.created", `{"provider":"github","number":1,"url":"https://github.com/internal/repo/pull/1","headSha":"` + strings.Repeat("a", 40) + `","baseSha":"` + strings.Repeat("b", 40) + `","state":"OPEN"}`, "https://github.com/internal/repo/pull/1"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			projected, err := SanitizeData(test.kind, json.RawMessage(test.data))
			if err != nil || !strings.Contains(string(projected), test.expected) {
				t.Fatal("redaction corrupted a safe structured path:", err)
			}
		})
	}
	criteria := make([]string, 100)
	for i := range criteria {
		criteria[i] = strings.Repeat("<", 2000)
	}
	raw, _ := json.Marshal(map[string]any{"title": "safe", "description": "safe", "assigneeEmployeeId": "deni", "priority": "NORMAL", "initialState": "DRAFT", "acceptanceCriteria": criteria})
	if _, err := SanitizeData("task.created", raw); err != nil {
		t.Fatal("legal maximum criteria rejected:", err)
	}
}

func TestSanitizeDataRejectsMalformedCanonicalPayloads(t *testing.T) {
	for _, test := range []struct{ kind, data string }{
		{"agent.message", `{"kind":"progress","summary":{"raw":"provider output"}}`},
		{"agent.message", `{"kind":"progress"}`},
		{"agent.message", `{"kind":"internal_reasoning","summary":"private"}`},
		{"command.output", `{"commandId":"00000000-0000-4000-8000-000000000001","stream":"stdout","chunkSequence":0,"text":"safe","redacted":false}`},
		{"file.activity", `{"operation":"read","path":"../private"}`},
		{"file.activity", `{"operation":"read","path":"C:\\internal\\private"}`},
		{"file.activity", `{"operation":"read","path":"..\\private"}`},
		{"agent.started", `{}`},
	} {
		t.Run(test.kind+test.data, func(t *testing.T) {
			if _, err := SanitizeData(test.kind, json.RawMessage(test.data)); err == nil {
				t.Fatal("malformed or noncanonical payload accepted")
			}
		})
	}
}
