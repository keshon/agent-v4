package agent

import (
	"tars/internal/llm"
	"tars/internal/prompts"
)

// compactHistory drops older assistant-led step groups from history while
// keeping the system prompt, original user task, and the most recent
// keepSteps groups intact. Tool results are never separated from their
// matching assistant tool_call.
func compactHistory(history []llm.Message, keepSteps int) []llm.Message {
	if keepSteps <= 0 || len(history) <= 2 {
		return history
	}

	prefix := append([]llm.Message(nil), history[:2]...)
	rest := history[2:]
	groups := stepGroups(rest)
	if len(groups) <= keepSteps {
		return history
	}

	kept := groups[len(groups)-keepSteps:]
	out := append([]llm.Message(nil), prefix...)
	out = append(out, llm.Message{Role: llm.RoleUser, Content: prompts.CompactNotice})
	for _, g := range kept {
		out = append(out, g...)
	}
	return out
}

// stepGroups splits messages into assistant-led steps: each group starts
// with an assistant message and includes every following non-assistant
// message until the next assistant turn.
func stepGroups(msgs []llm.Message) [][]llm.Message {
	var groups [][]llm.Message
	i := 0
	for i < len(msgs) {
		start := i
		i++
		for i < len(msgs) && msgs[i].Role != llm.RoleAssistant {
			i++
		}
		groups = append(groups, msgs[start:i])
	}
	return groups
}
