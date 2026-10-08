package events

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// payloadRule is the subset used by the frozen v1.0.0 contract, not a general
// JSON Schema engine. Unknown properties are projected out before delivery.
type payloadRule struct {
	Ref         string                 `json:"$ref"`
	Type        any                    `json:"type"`
	Enum        []any                  `json:"enum"`
	Const       json.RawMessage        `json:"const"`
	OneOf       []payloadRule          `json:"oneOf"`
	Required    []string               `json:"required"`
	Properties  map[string]payloadRule `json:"properties"`
	Items       *payloadRule           `json:"items"`
	MinLength   int                    `json:"minLength"`
	MaxLength   int                    `json:"maxLength"`
	MinItems    int                    `json:"minItems"`
	MaxItems    int                    `json:"maxItems"`
	UniqueItems bool                   `json:"uniqueItems"`
	Minimum     *float64               `json:"minimum"`
	Maximum     *float64               `json:"maximum"`
	Pattern     string                 `json:"pattern"`
	Format      string                 `json:"format"`
}

var payloadDefinitions = func() struct {
	Check  payloadRule            `json:"check"`
	Events map[string]payloadRule `json:"eventData"`
} {
	var result struct {
		Check  payloadRule            `json:"check"`
		Events map[string]payloadRule `json:"eventData"`
	}
	if err := json.Unmarshal([]byte(payloadContractJSON), &result); err != nil {
		panic("invalid compiled event contract")
	}
	return result
}()

func sanitizePayload(eventType string, raw json.RawMessage) (json.RawMessage, error) {
	rule, ok := payloadDefinitions.Events[eventType]
	if !ok || len(raw) == 0 || len(raw) > 2<<20 {
		return nil, ErrUnsafeEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrUnsafeEvent
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrUnsafeEvent
	}
	safe, err := projectPayload(rule, value)
	if err != nil {
		return nil, err
	}
	if eventType == "command.output" {
		text := safe.(map[string]any)["text"].(string)
		if len(text) > 32768 {
			return nil, ErrUnsafeEvent
		}
	}
	return json.Marshal(safe)
}

func allowsPayloadType(spec any, kind string) bool {
	if spec == nil {
		return true
	}
	if name, ok := spec.(string); ok {
		return name == kind
	}
	if names, ok := spec.([]any); ok {
		for _, name := range names {
			if name == kind {
				return true
			}
		}
	}
	return false
}

func projectPayload(rule payloadRule, value any) (any, error) {
	if rule.Ref != "" {
		if rule.Ref != "#/$defs/check" {
			return nil, ErrUnsafeEvent
		}
		return projectPayload(payloadDefinitions.Check, value)
	}
	if len(rule.OneOf) > 0 {
		for _, choice := range rule.OneOf {
			if result, err := projectPayload(choice, value); err == nil {
				return result, nil
			}
		}
		return nil, ErrUnsafeEvent
	}
	if len(rule.Const) > 0 {
		var expected any
		if err := json.Unmarshal(rule.Const, &expected); err != nil || !reflect.DeepEqual(value, expected) {
			return nil, ErrUnsafeEvent
		}
	}
	if rule.Enum != nil {
		matched := false
		for _, expected := range rule.Enum {
			if reflect.DeepEqual(value, expected) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrUnsafeEvent
		}
	}
	switch value := value.(type) {
	case nil:
		if !allowsPayloadType(rule.Type, "null") {
			return nil, ErrUnsafeEvent
		}
		return nil, nil
	case string:
		if !allowsPayloadType(rule.Type, "string") {
			return nil, ErrUnsafeEvent
		}
		length := utf8.RuneCountInString(value)
		if length < rule.MinLength || (rule.MaxLength > 0 && length > rule.MaxLength) {
			return nil, ErrUnsafeEvent
		}
		if !validPayloadString(rule, value) {
			return nil, ErrUnsafeEvent
		}
		if rule.Format != "" || rule.Pattern != "" {
			// These strings already passed structural validation. Credential/object
			// filtering still applies, but a relative application route is not a host path.
			safe := value
			for _, pattern := range redactionPatterns[:len(redactionPatterns)-2] {
				safe = pattern.ReplaceAllString(safe, "[REDACTED]")
			}
			if rule.Format != "" && safe != value {
				return nil, ErrUnsafeEvent
			}
			if rule.MaxLength > 0 && utf8.RuneCountInString(safe) > rule.MaxLength {
				safe = string([]rune(safe)[:rule.MaxLength])
			}
			return safe, nil
		}
		if rule.MaxLength > 0 {
			return boundedText(value, rule.MaxLength), nil
		}
		return RedactText(value), nil
	case json.Number:
		if !allowsPayloadType(rule.Type, "integer") {
			return nil, ErrUnsafeEvent
		}
		integer, err := value.Int64()
		if err != nil || (rule.Minimum != nil && float64(integer) < *rule.Minimum) || (rule.Maximum != nil && float64(integer) > *rule.Maximum) {
			return nil, ErrUnsafeEvent
		}
		return value, nil
	case bool:
		if !allowsPayloadType(rule.Type, "boolean") {
			return nil, ErrUnsafeEvent
		}
		return value, nil
	case []any:
		if !allowsPayloadType(rule.Type, "array") || len(value) < rule.MinItems || (rule.MaxItems > 0 && len(value) > rule.MaxItems) || rule.Items == nil {
			return nil, ErrUnsafeEvent
		}
		result := make([]any, 0, len(value))
		seen := map[string]bool{}
		for _, item := range value {
			projected, err := projectPayload(*rule.Items, item)
			if err != nil {
				return nil, err
			}
			if rule.UniqueItems {
				key, _ := json.Marshal(projected)
				if seen[string(key)] {
					return nil, ErrUnsafeEvent
				}
				seen[string(key)] = true
			}
			result = append(result, projected)
		}
		return result, nil
	case map[string]any:
		if !allowsPayloadType(rule.Type, "object") {
			return nil, ErrUnsafeEvent
		}
		for _, required := range rule.Required {
			if _, ok := value[required]; !ok {
				return nil, ErrUnsafeEvent
			}
		}
		result := make(map[string]any, len(rule.Properties))
		for name, childRule := range rule.Properties {
			child, exists := value[name]
			if !exists {
				continue
			}
			projected, err := projectPayload(childRule, child)
			if err != nil {
				return nil, err
			}
			result[name] = projected
		}
		return result, nil
	}
	return nil, ErrUnsafeEvent
}

func validPayloadString(rule payloadRule, value string) bool {
	switch rule.Format {
	case "uuid":
		if !validUUID(value) {
			return false
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return false
		}
	case "uri":
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	if rule.Pattern != "" {
		if strings.Contains(rule.Pattern, "(?!") {
			decoded, err := url.PathUnescape(value)
			if err != nil || strings.ContainsAny(decoded, "\\\x00\r\n") || strings.HasPrefix(decoded, "//") {
				return false
			}
			for _, component := range strings.Split(decoded, "/") {
				if component == ".." {
					return false
				}
			}
			if strings.HasPrefix(rule.Pattern, "^/") {
				return strings.HasPrefix(decoded, "/")
			}
			return !strings.HasPrefix(decoded, "/") && !(len(decoded) > 1 && decoded[1] == ':')
		}
		matched, err := regexp.MatchString(rule.Pattern, value)
		if err != nil || !matched {
			return false
		}
	}
	return true
}

func sanitizeEnvelope(e *Envelope) error {
	if e.SchemaVersion != "1.0.0" || e.StreamSequence < 1 {
		return ErrUnsafeEvent
	}
	switch e.Producer {
	case "api", "workflow", "gateway", "worker", "runtime_adapter", "github_webhook", "reconciler":
	default:
		return ErrUnsafeEvent
	}
	var actor struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if len(e.Actor) > 1024 || json.Unmarshal(e.Actor, &actor) != nil || actor.ID == "" || utf8.RuneCountInString(actor.ID) > 128 {
		return ErrUnsafeEvent
	}
	switch actor.Type {
	case "owner", "employee", "system", "integration":
	default:
		return ErrUnsafeEvent
	}
	actor.ID = boundedText(actor.ID, 128)
	safeActor, err := json.Marshal(actor)
	if err != nil {
		return ErrUnsafeEvent
	}
	safeData, err := SanitizeData(e.EventType, e.Data)
	if err != nil {
		return err
	}
	e.Actor, e.Data = safeActor, safeData
	return nil
}
