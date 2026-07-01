package tools

import (
	"agent-v4/internal/agent"
	"agent-v4/internal/workspace"
)

// Base returns the standard tool set shared by the main agent and
// subagents. Pass extra tools (ask_user, delegate_task) for the main agent only.
func Base(ws *workspace.Workspace, procs *BackgroundProcesses, extra ...agent.Tool) *agent.Registry {
	tools := []agent.Tool{
		ReadFile{WS: ws},
		WriteFile{WS: ws},
		PatchFile{WS: ws},
		PatchLines{WS: ws},
		ListFiles{WS: ws},
		MoveFile{WS: ws},
		RunShell{WS: ws},
		GrepFiles{WS: ws},
		StartBackground{WS: ws, Procs: procs},
		CheckBackground{Procs: procs},
		StopBackground{Procs: procs},
		CheckURL{},
	}
	tools = append(tools, extra...)
	return agent.NewRegistry(tools...)
}
