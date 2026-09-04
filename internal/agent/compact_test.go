package agent

import (
	"testing"

	"tars/internal/llm"
	"tars/internal/prompts"
)

func assistantStep(id string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo"}}},
		{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
	}
}

func TestCompactHistory_KeepsPrefixAndRecentGroups(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 15; i++ {
		history = append(history, assistantStep("c")...)
	}

	got := compactHistory(history, 8)
	if len(got) < 2+1+8*2 {
		t.Fatalf("too short: %d messages", len(got))
	}
	if got[0].Content != "sys" || got[1].Content != "task" {
		t.Fatal("prefix lost")
	}
	if got[2].Content != prompts.CompactNotice {
		t.Fatalf("expected compact notice at [2], got %q", got[2].Content)
	}
}

func TestCompactHistory_NoOrphanToolResults(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 10; i++ {
		id := "call"
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo"}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
		)
	}

	got := compactHistory(history, 3)
	for i, m := range got {
		if m.Role != llm.RoleTool {
			continue
		}
		if i == 0 || got[i-1].Role != llm.RoleAssistant {
			t.Fatalf("orphan tool result at %d", i)
		}
	}
}

func TestCompactHistory_DisabledWhenKeepStepsZero(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "hi"},
	}
	if got := compactHistory(history, 0); len(got) != len(history) {
		t.Fatal("expected unchanged history")
	}
}

func TestStepGroups_SplitsOnAssistant(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleTool, Content: "t1"},
		{Role: llm.RoleUser, Content: "nudge"},
		{Role: llm.RoleAssistant, Content: "a2"},
	}
	groups := stepGroups(msgs)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if len(groups[0]) != 3 || len(groups[1]) != 1 {
		t.Fatalf("unexpected group sizes: %d, %d", len(groups[0]), len(groups[1]))
	}
}
