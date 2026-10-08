package httpapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

var syntheticEventSecrets = []string{
	"ghp_" + strings.Repeat("a", 36),
	"sk-proj-" + strings.Repeat("b", 40),
	"AKIA" + strings.Repeat("C", 16),
	"only-a-synthetic-credential-value",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.syntheticSignature123",
	`C:\internal\workers\private.txt`,
	"/home/worker/internal/config.json",
	"task-reference-images/00000000-0000-4000-8000-000000000005",
	"https://storage.example.invalid/private?X-Amz-Signature=syntheticSignature",
}

func syntheticUnsafeText() string {
	patterns := append([]string{}, syntheticEventSecrets[:3]...)
	patterns = append(patterns, syntheticEventSecrets[4:]...)
	return strings.Join(patterns, "\n") + "\npassword=" + syntheticEventSecrets[3]
}

func assertNoEventSecrets(t *testing.T, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	// Compare decoded string values too: JSON escaping cannot hide a leak.
	var inspect func(any)
	inspect = func(v any) {
		switch v := v.(type) {
		case string:
			for _, secret := range syntheticEventSecrets {
				if strings.Contains(v, secret) {
					t.Error("event leaked a synthetic credential, storage key or host path")
				}
			}
		case map[string]any:
			for key, child := range v {
				if key == "objectKey" || key == "storageKey" || key == "rawProviderOutput" || key == "referenceImages" {
					t.Errorf("event exposed noncanonical field %s", key)
				}
				inspect(child)
			}
		case []any:
			for _, child := range v {
				inspect(child)
			}
		}
	}
	inspect(value)
}

func TestEventRedactionPrecedesPersistenceWithoutChangingTaskOrIdempotency(t *testing.T) {
	f := newEventFixture(t)
	criteria, _ := json.Marshal([]string{"api_key=" + syntheticEventSecrets[3], "Bearer " + syntheticEventSecrets[4]})
	input := tasks.CreateDraftInput{
		OrganizationID: f.owner.OrganizationID, ProjectID: f.owner.ProjectID,
		ActorUserID: f.owner.UserID, AssigneeEmployeeID: f.owner.EmployeeID,
		IdempotencyKey: "redacted-event-unredacted-brief", Title: "Inspect " + syntheticEventSecrets[0],
		Description:        syntheticUnsafeText(),
		AcceptanceCriteria: criteria, RequiredChecks: json.RawMessage(`[]`), TaskType: "feature", Priority: "NORMAL",
	}
	task, replayed, err := f.tasks.CreateDraft(f.ctx, input)
	if err != nil || replayed {
		t.Fatal("create synthetic credential fixture:", err)
	}
	var storedCriteria, originalCriteria []string
	if err := json.Unmarshal(task.AcceptanceCriteria, &storedCriteria); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(criteria, &originalCriteria); err != nil {
		t.Fatal(err)
	}
	if task.Title != input.Title || task.Description != input.Description || !reflect.DeepEqual(storedCriteria, originalCriteria) {
		t.Fatal("redaction changed the owner's task brief")
	}
	var persisted, outbox []byte
	if err := f.db.QueryRowContext(f.ctx, `SELECT e.data, o.payload_json FROM agent_events e JOIN event_outbox o USING (event_id) WHERE e.task_id = $1`, task.ID).Scan(&persisted, &outbox); err != nil {
		t.Fatal(err)
	}
	assertNoEventSecrets(t, persisted)
	assertNoEventSecrets(t, outbox)
	if !strings.Contains(string(persisted), "[REDACTED]") {
		t.Fatal("event payload was not redacted before persistence")
	}
	if _, replayed, err := f.tasks.CreateDraft(f.ctx, input); err != nil || !replayed {
		t.Fatal("unchanged task input did not replay:", err)
	}
	input.Description = strings.ReplaceAll(input.Description, syntheticEventSecrets[0], "ghp_"+strings.Repeat("z", 36))
	if _, _, err := f.tasks.CreateDraft(f.ctx, input); !errors.Is(err, tasks.ErrIdempotencyConflict) {
		t.Fatal("redacted event changed the original request hash")
	}
	var count int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM agent_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate create emitted %d events, want 1", count)
	}
	response := f.get("/api/v1/projects/" + f.owner.ProjectID + "/events")
	decodeHistory(t, response)
	assertNoEventSecrets(t, response.Body.Bytes())
}

func TestEventHistoryRedactsLegacyPayloadsAtDeliveryBoundary(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "legacy")
	payload, _ := json.Marshal(map[string]any{
		"title": "Legacy safe title", "description": syntheticUnsafeText(),
		"assigneeEmployeeId": f.owner.EmployeeID, "priority": "NORMAL", "initialState": "DRAFT",
		"acceptanceCriteria": []string{"password=" + syntheticEventSecrets[3]},
		"storageKey":         "private-internal-object", "objectKey": syntheticEventSecrets[7],
		"rawProviderOutput": map[string]string{"raw": "internal provider reasoning"},
		"referenceImages":   []map[string]string{{"objectKey": syntheticEventSecrets[7]}},
	})
	if _, err := f.db.ExecContext(f.ctx, `UPDATE agent_events SET data = $1::jsonb`, payload); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?after=0"} {
		response := f.get("/api/v1/projects/" + f.owner.ProjectID + "/events" + suffix)
		page := decodeHistory(t, response)
		assertNoEventSecrets(t, response.Body.Bytes())
		var data map[string]json.RawMessage
		if err := json.Unmarshal(page.Items[0]["data"], &data); err != nil {
			t.Fatal(err)
		}
		if len(data) != 6 {
			t.Fatalf("task.created has %d fields, want only its 6 canonical required fields", len(data))
		}
	}
	// Defense-in-depth is read-only, not an undocumented legacy migration.
	var stored []byte
	if err := f.db.QueryRowContext(f.ctx, `SELECT data FROM agent_events`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), syntheticEventSecrets[0]) {
		t.Fatal("legacy read mutated durable history")
	}
}

func TestEventHistoryRejectsUnsafeEnvelopeRatherThanLeakingOrSkipping(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, "safe-envelope")
	path := "/api/v1/projects/" + f.owner.ProjectID + "/events"
	for _, statement := range []string{
		`UPDATE agent_events SET schema_version = 'unknown'`,
		`UPDATE agent_events SET schema_version = '1.0.0', producer = 'raw-provider-component'`,
		`UPDATE agent_events SET producer = 'api', actor = '{"type":"internal","id":"private"}'::jsonb`,
	} {
		if _, err := f.db.ExecContext(f.ctx, statement); err != nil {
			t.Fatal(err)
		}
		assertEventError(t, f.get(path), 503, "service_unavailable")
	}
	actor, _ := json.Marshal(map[string]string{"type": "owner", "id": f.owner.UserID, "objectKey": "private-object", "sessionToken": syntheticEventSecrets[0]})
	if _, err := f.db.ExecContext(f.ctx, `UPDATE agent_events SET actor = $1::jsonb`, actor); err != nil {
		t.Fatal(err)
	}
	response := f.get(path)
	page := decodeHistory(t, response)
	assertNoEventSecrets(t, response.Body.Bytes())
	var safeActor map[string]any
	if err := json.Unmarshal(page.Items[0]["actor"], &safeActor); err != nil {
		t.Fatal(err)
	}
	if len(safeActor) != 2 {
		t.Fatal("actor leaked unversioned provider fields")
	}
}
