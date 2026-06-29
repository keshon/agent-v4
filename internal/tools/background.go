package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
)

// syncBuffer is an io.Writer safe for a running process to write to while
// check_background concurrently reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type bgProc struct {
	cmd     *exec.Cmd
	output  *syncBuffer
	exited  bool
	exitErr error
}

// BackgroundProcesses tracks commands started without blocking on them —
// a dev server or watcher that's meant to keep running, not a command
// with a finite exit. Shared by StartBackground/CheckBackground/
// StopBackground; construct one and pass the same pointer to all three.
type BackgroundProcesses struct {
	mu    sync.Mutex
	procs map[string]*bgProc
	next  int
}

func NewBackgroundProcesses() *BackgroundProcesses {
	return &BackgroundProcesses{procs: make(map[string]*bgProc)}
}

// start launches cmd and returns immediately with an id — it does not
// wait for the process to exit. cmd must use a context independent of any
// single tool call's lifetime, or the process would die the instant the
// call that started it returns.
func (p *BackgroundProcesses) start(cmd *exec.Cmd) (id string, output *syncBuffer) {
	buf := &syncBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	setNewProcessGroup(cmd)

	p.mu.Lock()
	p.next++
	id = fmt.Sprintf("bg%d", p.next)
	proc := &bgProc{cmd: cmd, output: buf}
	p.procs[id] = proc
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		p.mu.Lock()
		proc.exited = true
		proc.exitErr = err
		p.mu.Unlock()
		return id, buf
	}

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		proc.exited = true
		proc.exitErr = err
		p.mu.Unlock()
	}()
	return id, buf
}

func (p *BackgroundProcesses) status(id string) (output string, exited bool, exitErr error, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	proc, ok := p.procs[id]
	if !ok {
		return "", false, nil, fmt.Errorf("unknown background process id %q", id)
	}
	return proc.output.String(), proc.exited, proc.exitErr, nil
}

func (p *BackgroundProcesses) stop(id string) error {
	p.mu.Lock()
	proc, ok := p.procs[id]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown background process id %q", id)
	}
	if proc.cmd.Process == nil {
		return fmt.Errorf("process %q never started", id)
	}
	return killProcessGroup(proc.cmd)
}
