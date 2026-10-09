package sessionrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rendr17/dioffice/apps/api/internal/executionmanifest"
)

// Prompt field caps keep one runaway task record from producing an
// unbounded provider request; truncation is explicit in the text.
const (
	promptFieldLimit = 4000
	promptItemLimit  = 500
	promptItemMax    = 25
	promptOverallCap = 24000
)

func clipText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "… [truncated]"
}

// BuildInitialPrompt composes the first instruction for a task session from
// durable task facts and the validated manifest — the minimum context the
// runtime spec requires. It never embeds secrets, host paths, or internal
// error detail; criteria arrive as the task's stored JSONB array. A non-empty
// changeRequest marks a continuation attempt: the Owner's review feedback is
// quoted so Deni revises the preserved worktree instead of starting over.
func BuildInitialPrompt(title, description string, acceptanceCriteria json.RawMessage,
	employeeName, employeeRole, branchName, changeRequest string,
	manifest *executionmanifest.Manifest) (string, error) {
	if manifest == nil {
		return "", errors.New("manifest is required to build the instruction")
	}
	var criteria []string
	if len(acceptanceCriteria) > 0 {
		var raw []json.RawMessage
		if err := json.Unmarshal(acceptanceCriteria, &raw); err != nil {
			return "", fmt.Errorf("parse acceptance criteria: %w", err)
		}
		for _, item := range raw {
			var text string
			if err := json.Unmarshal(item, &text); err == nil {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					criteria = append(criteria, trimmed)
				}
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, %s at DiOffice — a persistent AI employee working on the repository checked out in this session's directory.\n\n", employeeName, employeeRole)
	fmt.Fprintf(&b, "Task: %s\n\n", clipText(title, promptItemLimit))
	if desc := clipText(description, promptFieldLimit); desc != "" {
		fmt.Fprintf(&b, "Description:\n%s\n\n", desc)
	}
	if len(criteria) > 0 {
		b.WriteString("Acceptance criteria:\n")
		for i, c := range criteria {
			if i >= promptItemMax {
				b.WriteString("- … [further criteria truncated]\n")
				break
			}
			fmt.Fprintf(&b, "- %s\n", clipText(c, promptItemLimit))
		}
		b.WriteString("\n")
	}
	if change := clipText(changeRequest, promptFieldLimit); change != "" {
		fmt.Fprintf(&b, "Requested changes (Owner review of the previous submission):\n%s\n\n", change)
		b.WriteString("This is a continuation attempt: the worktree still contains your previous work " +
			"and its pull request is already open. Revise it — address the feedback in new commits on " +
			"the same branch, then re-run the required checks.\n\n")
	}
	checkIDs := make([]string, 0, len(manifest.Commands.Checks))
	for _, check := range manifest.Commands.Checks {
		checkIDs = append(checkIDs, check.ID)
	}
	fmt.Fprintf(&b, "Rules:\n"+
		"- Work only on the checked-out branch %s inside this workspace; commit your changes with clear messages.\n"+
		"- Follow %s: working directory %q; required checks: %s.\n"+
		"- Do not push to remotes, open pull requests, modify other branches, or access files outside this workspace.\n"+
		"- Do not expose secrets or credentials in code, commits, or output.\n"+
		"- When implementation is complete and the required checks pass for your final commit, stop and summarize: what changed, check results, and the final commit SHA.\n",
		branchName, executionmanifest.Path, manifest.WorkingDirectory, strings.Join(checkIDs, ", "))
	prompt := b.String()
	if len(prompt) > promptOverallCap {
		prompt = prompt[:promptOverallCap] + "… [truncated]"
	}
	return prompt, nil
}
