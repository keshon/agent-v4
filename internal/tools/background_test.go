package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-v4/internal/workspace"
)

func TestBackgroundProcesses_StartCheckStop(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	procs := NewBackgroundProcesses()

	start := StartBackground{WS: ws, Procs: procs, SettleTime: 100 * time.Millisecond}
	args, _ := json.Marshal(map[string]string{"command": "sleep 5"})
	out, err := start.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out, "running in background") {
		t.Fatalf("expected still running right after start, got: %q", out)
	}

	var id string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
	}
	if id == "" {
		t.Fatalf("could not parse id from: %q", out)
	}

	check := CheckBackground{Procs: procs}
	cArgs, _ := json.Marshal(map[string]string{"id": id})
	checkOut, err := check.Run(context.Background(), cArgs)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(checkOut, "still running") {
		t.Fatalf("expected still running, got: %q", checkOut)
	}

	stop := StopBackground{Procs: procs}
	if _, err := stop.Run(context.Background(), cArgs); err != nil {
		t.Fatalf("stop: %v", err)
	}

	time.Sleep(200 * time.Millisecond) // let Wait() observe the kill
	checkOut2, err := check.Run(context.Background(), cArgs)
	if err != nil {
		t.Fatalf("check after stop: %v", err)
	}
	if strings.Contains(checkOut2, "still running") {
		t.Fatalf("expected process to be stopped, got: %q", checkOut2)
	}
}

func TestBackgroundProcesses_CapturesOutput(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	procs := NewBackgroundProcesses()

	start := StartBackground{WS: ws, Procs: procs, SettleTime: 300 * time.Millisecond}
	args, _ := json.Marshal(map[string]string{"command": "echo hello-from-bg"})
	out, err := start.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out, "hello-from-bg") {
		t.Fatalf("expected captured output, got: %q", out)
	}
}

func TestCheckBackground_UnknownID(t *testing.T) {
	check := CheckBackground{Procs: NewBackgroundProcesses()}
	args, _ := json.Marshal(map[string]string{"id": "bg999"})
	if _, err := check.Run(context.Background(), args); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestCheckURL_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool := CheckURL{}
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "200") {
		t.Fatalf("expected status 200, got: %q", out)
	}
}

func TestCheckURL_ConnectionRefused(t *testing.T) {
	tool := CheckURL{}
	// Port 1 is reserved and essentially guaranteed nothing is listening.
	args, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:1"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run returned a Go error, want a normal diagnostic result: %v", err)
	}
	if !strings.Contains(out, "request failed") {
		t.Fatalf("expected a connection failure message, got: %q", out)
	}
}

func TestBackgroundProcesses_StopKillsWholeProcessTree(t *testing.T) {
	// Regression test for the actual bug found: "sh -c '...'" forks a
	// child instead of exec-replacing itself. Killing only the wrapper
	// PID leaves the real child running as an orphan — and that orphan
	// holding the output pipe open is exactly what makes cmd.Wait() hang
	// forever. stop() must kill the whole process group.
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	procs := NewBackgroundProcesses()
	marker := dir + "/still-alive"

	start := StartBackground{WS: ws, Procs: procs, SettleTime: 200 * time.Millisecond}
	cmd := "sh -c 'while true; do date +%s > " + marker + "; sleep 0.1; done'"
	args, _ := json.Marshal(map[string]string{"command": cmd})
	out, err := start.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var id string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
	}

	stop := StopBackground{Procs: procs}
	sArgs, _ := json.Marshal(map[string]string{"id": id})
	if _, err := stop.Run(context.Background(), sArgs); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Exactly what hung indefinitely before the process-group fix.
	check := CheckBackground{Procs: procs}
	done := make(chan struct{})
	go func() {
		for {
			out, _ := check.Run(context.Background(), sArgs)
			if !strings.Contains(out, "still running") {
				close(done)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("check_background never reported the process as exited within 3s")
	}
}
