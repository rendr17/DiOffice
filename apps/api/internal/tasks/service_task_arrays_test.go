package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeAndValidateDefaultsOptionalTaskArrays(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		acceptanceCriteria json.RawMessage
		requiredChecks     json.RawMessage
	}{
		{name: "omitted"},
		{name: "null acceptance criteria", acceptanceCriteria: json.RawMessage(`null`), requiredChecks: json.RawMessage(`[]`)},
		{name: "null required checks", acceptanceCriteria: json.RawMessage(`[]`), requiredChecks: json.RawMessage(`null`)},
		{name: "both null", acceptanceCriteria: json.RawMessage(`null`), requiredChecks: json.RawMessage(`null`)},
		{name: "whitespace around null", acceptanceCriteria: json.RawMessage(" \nnull\t "), requiredChecks: json.RawMessage("\t null\n")},
		{name: "empty arrays", acceptanceCriteria: json.RawMessage(`[]`), requiredChecks: json.RawMessage(`[]`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := testCreateDraftInput(
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000002",
				"00000000-0000-4000-8000-000000000003",
				"00000000-0000-4000-8000-000000000004",
				"optional-task-arrays",
			)
			input.AcceptanceCriteria = testCase.acceptanceCriteria
			input.RequiredChecks = testCase.requiredChecks

			normalized, err := normalizeAndValidate(input)
			if err != nil {
				t.Fatalf("normalizeAndValidate() error = %v", err)
			}
			if string(normalized.AcceptanceCriteria) != "[]" || string(normalized.RequiredChecks) != "[]" {
				t.Fatalf("normalized task arrays = %s/%s, want []/[]", normalized.AcceptanceCriteria, normalized.RequiredChecks)
			}
		})
	}
}

func TestCreateDraftDefaultsNullTaskArrays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newTaskTestDatabase(t, ctx)
	organizationID, projectID, ownerID, employeeID := createTaskFixtures(t, ctx, db)
	service := NewService(db)
	input := testCreateDraftInput(organizationID, projectID, ownerID, employeeID, "null-task-arrays")
	input.AcceptanceCriteria = json.RawMessage(`null`)
	input.RequiredChecks = json.RawMessage(`null`)
	input.ReferenceImages = []ReferenceImage{validReferenceImage()}

	task, replayed, err := service.CreateDraft(ctx, input)
	if err != nil {
		t.Fatalf("CreateDraft() with null task arrays error = %v", err)
	}
	if replayed || string(task.AcceptanceCriteria) != "[]" || string(task.RequiredChecks) != "[]" {
		t.Fatalf("CreateDraft() replay/arrays = %t/%s/%s, want false/[]/[]", replayed, task.AcceptanceCriteria, task.RequiredChecks)
	}

	var acceptanceCriteria, requiredChecks string
	if err := db.QueryRowContext(ctx, `
		SELECT acceptance_criteria::text, required_checks::text FROM tasks
		WHERE organization_id = $1 AND id = $2`, organizationID, task.ID).Scan(&acceptanceCriteria, &requiredChecks); err != nil {
		t.Fatalf("read persisted task arrays: %v", err)
	}
	if acceptanceCriteria != "[]" || requiredChecks != "[]" {
		t.Fatalf("persisted task arrays = %s/%s, want []/[]", acceptanceCriteria, requiredChecks)
	}

	input.AcceptanceCriteria = json.RawMessage(`[]`)
	input.RequiredChecks = json.RawMessage(`[]`)
	replayedTask, replayed, err := service.CreateDraft(ctx, input)
	if err != nil {
		t.Fatalf("CreateDraft() replay with empty task arrays error = %v", err)
	}
	if !replayed || replayedTask.ID != task.ID {
		t.Fatalf("CreateDraft() replay/task ID = %t/%q, want true/%q", replayed, replayedTask.ID, task.ID)
	}
}
