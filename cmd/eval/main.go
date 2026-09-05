// Command eval scores TARS against the frozen probes in eval/probes and
// reports pass rates.
//
// eval/prompts/ already describes 17 failure shapes with explicit pass and
// fail criteria — the design was never the missing piece. What was missing
// is that the verdict lived in a human reading a table, so "it got worse
// lately" stayed unfalsifiable and the cheapest answer to it was always to
// start the project over. This turns each probe's prose criteria into
// checks a program can run.
//
// A probe asserts two different kinds of thing, because the prompts do:
//
//   - verify: what the workspace must look like afterwards. Reuses
//     mission.Check, so an empty file still counts as a failure.
//
//   - trace: which tools were actually used. Half of eval/README.md's
//     judging table is this shape — move_file rather than shell mv,
//     list_files rather than run_shell ls, start_background rather than a
//     run_shell that will time out. A run can produce a correct workspace
//     the wrong way, and that predicts the next failure.
//
// The verdict is never the mission's own check: a subtask's check is
// chosen by the model, and a model that writes its own exam writes an easy
// one (live 2026-07-14: acceptance demanded src/main.ts, the check tested
// package.json for "vite", the subtask went green with src/main.ts
// absent). Probes are frozen by hand and the harness runs them itself.
//
// Because a weak model is stochastic, -runs repeats each probe and reports
// a rate. A single pass proves very little.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"tars/internal/llm"
	"tars/internal/mission"
	"tars/internal/roles"
	"tars/internal/tools"
	"tars/internal/workspace"
)

// Probe is one frozen scenario. It pairs with a human-readable write-up in
// eval/prompts/; Prompt points at that file so the two can't drift apart
// silently.
type Probe struct {
	Name string `json:"-"`

	// Prompt is the eval/prompts/*.md file this probe mechanizes.
	Prompt string `json:"prompt"`

	// Task is the instruction, exactly as a user would type it.
	Task string `json:"task"`

	// Mission runs the planner/worker pipeline instead of the direct
	// agent loop — probes 15-17 in eval/prompts.
	Mission bool `json:"mission"`

	// Seed is a directory copied in as the starting workspace, relative to
	// the repo root. Empty means start from an empty directory.
	Seed string `json:"seed"`

	// Verify is the workspace verdict. Every check must pass.
	Verify []mission.Check `json:"verify"`

	// Trace is the tool-usage verdict. Every check must pass.
	Trace []TraceCheck `json:"trace"`

	// Answer asserts on the run's final text. Several probes are about
	// what got reported rather than what changed on disk — "reports
	// ~150000 bytes", "correct commit list in the final answer" — and a
	// read-only probe has no workspace change to assert on at all.
	Answer []AnswerCheck `json:"answer"`

	// MaxSteps fails a run that took more round-trips than the probe
	// considers reasonable. Zero disables the limit. Finishing correctly
	// after twenty steps of thrashing is worth knowing about.
	MaxSteps int `json:"max_steps"`

	TimeoutSec int `json:"timeout_sec"`
}

// TraceCheck asserts that a tool was, or was not, used.
type TraceCheck struct {
	Tool string `json:"tool"`

	// Mode is "required", "forbidden", or empty when only MaxCalls
	// applies.
	Mode string `json:"mode"`

	// MaxCalls caps how many times the tool may be called; zero means no
	// cap. Several probes' fail conditions are about repetition rather
	// than presence — "blind patch_file that errors 3+ times without
	// strategy change", "more than 2 ask_user calls" — and a total step
	// budget can't express those without also punishing runs that took a
	// legitimately longer route.
	MaxCalls int `json:"max_calls,omitempty"`

	// ArgsRegex narrows the check to calls whose JSON arguments match —
	// run_shell is legitimate in general and forbidden for `ls`, so the
	// tool name alone can't express the rule.
	ArgsRegex string `json:"args_regex,omitempty"`

	// Why is quoted in the failure so a red result explains itself
	// without opening eval/prompts.
	Why string `json:"why,omitempty"`
}

// AnswerCheck asserts that the final answer does, or does not, match a
// pattern. Case-insensitive unless the pattern says otherwise.
type AnswerCheck struct {
	Regex string `json:"regex"`

	// Mode is "required" or "forbidden".
	Mode string `json:"mode"`

	Why string `json:"why,omitempty"`
}

// call is one tool invocation as it actually happened.
type call struct {
	tool string
	args string
}

type Result struct {
	Probe   string  `json:"probe"`
	Run     int     `json:"run"`
	Passed  bool    `json:"passed"`
	Steps   int     `json:"steps"`
	Seconds float64 `json:"seconds"`

	// Errored means the run never reached the model — a dead backend, a
	// bad seed path — so it says nothing about the agent and must not be
	// scored. Without this the first thing a stopped koboldcpp produces
	// is a table of red rows and failed checks, which reads exactly like
	// a model that got worse. An instrument that cannot tell "the agent
	// failed" from "nothing ran" is the same disease as a model grading
	// its own work.
	Errored bool `json:"errored,omitempty"`

	RunError string   `json:"run_error,omitempty"`
	Failures []string `json:"failures,omitempty"`
	Trace    string   `json:"trace_log,omitempty"`
}

const defaultTimeout = 15 * time.Minute

func main() {
	backend := flag.String("backend", "http://localhost:5001", "koboldcpp/llama.cpp base URL")
	model := flag.String("model", "local", "model name (often ignored by local servers)")
	dir := flag.String("probes", "eval/probes", "directory of probe .json files")
	only := flag.String("only", "", "run only probes whose name contains this substring")
	runs := flag.Int("runs", 1, "runs per probe — a weak model is stochastic, so one pass "+
		"proves very little")
	outDir := flag.String("out", "eval/results", "directory for results.jsonl and per-run traces")
	maxTokens := flag.Int("max-tokens", 8192, "generation budget per response")
	dry := flag.Bool("dry", false, "load and validate every probe, then exit — checking a probe "+
		"should not cost a model round-trip")
	flag.Parse()

	if *dry {
		// Deliberately ignores -only: the point is to validate the whole
		// set before an overnight run, not the subset you were editing.
		probes, err := loadProbes(*dir, "")
		if err != nil {
			log.Fatalf("load probes: %v", err)
		}
		for _, p := range probes {
			mode := "agent"
			if p.Mission {
				mode = "mission"
			}
			fmt.Printf("  %-22s %-7s %d verify, %d trace, %d answer, max %d steps\n",
				p.Name, mode, len(p.Verify), len(p.Trace), len(p.Answer), p.MaxSteps)
		}
		fmt.Printf("%d probes OK\n", len(probes))
		return
	}

	probes, err := loadProbes(*dir, *only)
	if err != nil {
		log.Fatalf("load probes: %v", err)
	}
	if len(probes) == 0 {
		log.Fatalf("no probes matched in %s", *dir)
	}

	runDir := filepath.Join(*outDir, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		log.Fatalf("create results dir: %v", err)
	}
	resultsPath := filepath.Join(runDir, "results.jsonl")
	results, err := os.Create(resultsPath)
	if err != nil {
		log.Fatalf("create results file: %v", err)
	}
	defer results.Close()

	// A pass rate without the model that produced it is worse than no
	// number — comparing today's score against one from a different
	// quantization is the exact mistake this tool exists to prevent.
	probe := llm.NewKoboldClient(*backend, *model)
	ctx := context.Background()
	contextLimit, _ := probe.MaxContextLength(ctx)
	backendModel, err := probe.ModelName(ctx)
	if err != nil {
		backendModel = "(unknown)"
	}
	fmt.Printf("backend %s\nmodel   %s\ncontext %d\n%d probes x %d runs\n\n",
		*backend, backendModel, contextLimit, len(probes), *runs)
	writeMeta(runDir, *backend, backendModel, contextLimit)

	var all []Result
	for _, p := range probes {
		for run := 1; run <= *runs; run++ {
			r := runOnce(ctx, p, run, runDir, *backend, *model, contextLimit, *maxTokens)
			all = append(all, r)

			line, _ := json.Marshal(r)
			fmt.Fprintln(results, string(line))

			status := "PASS"
			switch {
			case r.Errored:
				status = "ERR "
			case !r.Passed:
				status = "FAIL"
			}
			fmt.Printf("  %s  %-22s run %d  %6.1fs  %2d steps", status, p.Name, run, r.Seconds, r.Steps)
			if len(r.Failures) > 0 {
				fmt.Printf("  %s", r.Failures[0])
			} else if r.RunError != "" {
				fmt.Printf("  (%s)", r.RunError)
			}
			fmt.Println()
		}
	}

	fmt.Printf("\n%s\n\nresults: %s\n", summarize(all), resultsPath)
}

// The return value is named so the deferred timing write lands in what
// the caller receives: with an unnamed result, `return res` copies before
// the defer runs and every duration is reported as zero.
func runOnce(ctx context.Context, p Probe, run int, runDir, backend, model string,
	contextLimit, maxTokens int) (res Result) {

	res = Result{Probe: p.Name, Run: run}
	started := time.Now()
	defer func() { res.Seconds = time.Since(started).Seconds() }()

	// A fresh copy per run. Without it the second run starts from the
	// first run's output and measures nothing.
	work, err := os.MkdirTemp("", "tars-eval-")
	if err != nil {
		res.RunError = "mkdir temp: " + err.Error()
		return res
	}
	defer os.RemoveAll(work)
	if p.Seed != "" {
		if err := copyTree(p.Seed, work); err != nil {
			res.RunError = "seed workspace: " + err.Error()
			return res
		}
	}
	ws, err := workspace.New(work)
	if err != nil {
		res.RunError = "workspace: " + err.Error()
		return res
	}

	client := llm.NewKoboldClient(backend, model)
	client.Grammar = llm.DefaultGrammar

	// Every model call, verbatim. When a probe regresses this is the only
	// thing that says why — reading traces is how the three harness bugs
	// fixed on 2026-09-04 were found.
	tracePath := filepath.Join(runDir, fmt.Sprintf("%s-run%d.log", p.Name, run))
	if tf, err := os.Create(tracePath); err == nil {
		defer tf.Close()
		client.Debug = tf
		res.Trace = tracePath
	}

	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	procs := tools.NewBackgroundProcesses()
	// A probe that leaves a dev server running holds its temp workspace
	// open, so the next run inherits a directory that won't delete and a
	// port that's taken. Both look like model failures and are neither.
	defer procs.StopAll()

	var calls []call
	record := func(step int, msg llm.Message) {
		if step+1 > res.Steps {
			res.Steps = step + 1
		}
		for _, tc := range msg.ToolCalls {
			calls = append(calls, call{tool: tc.Name, args: string(tc.Arguments)})
		}
	}

	var answer string
	if p.Mission {
		runner := &mission.Runner{
			Client:       client,
			WS:           ws,
			Dir:          filepath.Join(work, ".agent", "eval"),
			Procs:        procs,
			ContextLimit: contextLimit,
			MaxTokens:    maxTokens,
			// No ApprovePlan: an eval that stops for a human measures the
			// human. Review stays on — it is part of what is scored.
			OnStep: func(_ string, step int, msg llm.Message) { record(step, msg) },
		}
		m := &mission.Mission{
			ID:    fmt.Sprintf("%s-%d", p.Name, run),
			Task:  p.Task,
			Phase: mission.PhasePlan,
		}
		report, err := runner.Run(runCtx, m)
		answer = report
		if err != nil {
			res.RunError = firstLine(err.Error())
		}
	} else {
		// roles.Interactive is what cmd/agent builds too, so the probe
		// scores the agent that ships. This used to be a hand-copied
		// config here and it had already drifted: no ask_user, no
		// delegate_task, while two probes are specifically about whether
		// those get reached for when they shouldn't be.
		//
		// The responder says plainly that nobody is there rather than
		// faking an answer — dropping the tool would change the tool set,
		// and inventing an answer would make the probe score the answer.
		// Whether ask_user was called at all stays visible in the trace,
		// which is what probe 12 measures.
		env := evalEnv(client, ws, procs, contextLimit, maxTokens, record)
		a := roles.Interactive(env, "", "", func(string) (string, error) {
			return "This is an automated evaluation run; no human is available. " +
				"State your assumption and proceed with the smallest reasonable action.", nil
		}, nil)
		final, err := a.Run(runCtx, p.Task)
		answer = final
		if err != nil {
			res.RunError = firstLine(err.Error())
		}
	}

	// Zero steps with an error means no model response ever arrived, so
	// the workspace checks below would only be measuring the seed. Report
	// it as an error rather than scoring it.
	if res.Steps == 0 && res.RunError != "" {
		res.Errored = true
		return res
	}

	// The agent ran but did not finish — it hit MaxSteps, or the backend
	// died mid-run. The workspace may still satisfy every check by luck,
	// which is how probe 06 scored green on a run whose own error said
	// "reached max steps (25) without finishing". A run the harness had
	// to cut off is not a pass.
	if res.RunError != "" {
		res.Failures = append(res.Failures, "run did not complete: "+res.RunError)
	}

	res.Failures = append(res.Failures, checkWorkspace(runCtx, p, ws)...)
	res.Failures = append(res.Failures, checkTrace(p, calls)...)
	res.Failures = append(res.Failures, checkAnswer(p, answer)...)
	if p.MaxSteps > 0 && res.Steps > p.MaxSteps {
		res.Failures = append(res.Failures,
			fmt.Sprintf("took %d steps, probe allows %d", res.Steps, p.MaxSteps))
	}
	res.Passed = len(res.Failures) == 0
	return res
}

// evalEnv adapts the harness's step recorder, which ignores labels
// because a probe scores one agent at a time, to the labelled callback
// roles.Env hands out.
func evalEnv(client llm.Client, ws *workspace.Workspace, procs *tools.BackgroundProcesses,
	contextLimit, maxTokens int, record func(int, llm.Message)) roles.Env {

	return roles.Env{
		Client:       client,
		WS:           ws,
		Procs:        procs,
		MaxTokens:    maxTokens,
		ContextLimit: contextLimit,
		OnStep:       func(_ string, step int, msg llm.Message) { record(step, msg) },
	}
}

func checkWorkspace(ctx context.Context, p Probe, ws *workspace.Workspace) []string {
	var out []string
	for _, c := range p.Verify {
		if msg, ok := mission.RunCheck(ctx, c, ws); !ok {
			out = append(out, firstLine(msg))
		}
	}
	return out
}

func checkTrace(p Probe, calls []call) []string {
	var out []string
	for _, tc := range p.Trace {
		var re *regexp.Regexp
		if tc.ArgsRegex != "" {
			var err error
			if re, err = regexp.Compile(tc.ArgsRegex); err != nil {
				out = append(out, fmt.Sprintf("bad args_regex %q: %v", tc.ArgsRegex, err))
				continue
			}
		}
		matched := 0
		for _, c := range calls {
			if c.tool != tc.Tool {
				continue
			}
			if re != nil && !re.MatchString(c.args) {
				continue
			}
			matched++
		}
		if tc.MaxCalls > 0 && matched > tc.MaxCalls {
			out = append(out, describe(tc,
				fmt.Sprintf("called %dx, probe allows %d", matched, tc.MaxCalls)))
		}
		switch tc.Mode {
		case "required":
			if matched == 0 {
				out = append(out, describe(tc, "never called"))
			}
		case "forbidden":
			if matched > 0 {
				out = append(out, describe(tc, fmt.Sprintf("called %dx", matched)))
			}
		case "":
			// MaxCalls-only check; presence is not asserted either way.
		default:
			out = append(out, fmt.Sprintf("probe %s: unknown trace mode %q", p.Name, tc.Mode))
		}
	}
	return out
}

func checkAnswer(p Probe, answer string) []string {
	var out []string
	for _, ac := range p.Answer {
		// Case-insensitive by default: these assert on prose a model
		// wrote, and "150000 Bytes" is the same answer as "150000 bytes".
		re, err := regexp.Compile("(?i)" + ac.Regex)
		if err != nil {
			out = append(out, fmt.Sprintf("bad answer regex %q: %v", ac.Regex, err))
			continue
		}
		hit := re.MatchString(answer)
		switch {
		case ac.Mode == "required" && !hit:
			out = append(out, explain("answer does not match "+ac.Regex, ac.Why))
		case ac.Mode == "forbidden" && hit:
			out = append(out, explain("answer matches "+ac.Regex, ac.Why))
		case ac.Mode != "required" && ac.Mode != "forbidden":
			out = append(out, fmt.Sprintf("probe %s: unknown answer mode %q", p.Name, ac.Mode))
		}
	}
	return out
}

func explain(what, why string) string {
	if why != "" {
		return what + " — " + why
	}
	return what
}

func describe(tc TraceCheck, what string) string {
	label := tc.Tool
	if tc.ArgsRegex != "" {
		label += " matching " + tc.ArgsRegex
	}
	if tc.Why != "" {
		return fmt.Sprintf("%s %s — %s", label, what, tc.Why)
	}
	return fmt.Sprintf("%s %s", label, what)
}

// summarize reports per-probe rates as well as the total. The per-probe
// numbers matter more: a change that fixes one shape of task and breaks
// another shows up there and nowhere else.
func summarize(all []Result) string {
	type tally struct{ pass, total int }
	byProbe := map[string]*tally{}
	var names []string
	overall := tally{}

	errored := 0
	for _, r := range all {
		// A run that never reached the model is not evidence either way.
		// Counting it as a failure would mean a stopped backend silently
		// reports the agent as broken.
		if r.Errored {
			errored++
			continue
		}
		t, seen := byProbe[r.Probe]
		if !seen {
			t = &tally{}
			byProbe[r.Probe] = t
			names = append(names, r.Probe)
		}
		t.total++
		overall.total++
		if r.Passed {
			t.pass++
			overall.pass++
		}
	}
	sort.Strings(names)

	if overall.total == 0 {
		return fmt.Sprintf("=== no scored runs ===\n  %d run(s) never reached the model — "+
			"check the backend is up, then re-run. No pass rate is reported because "+
			"there is nothing to report.", errored)
	}

	var b strings.Builder
	b.WriteString("=== pass rate ===\n")
	for _, n := range names {
		t := byProbe[n]
		fmt.Fprintf(&b, "  %-22s %d/%d  %3.0f%%\n", n, t.pass, t.total,
			100*float64(t.pass)/float64(t.total))
	}
	fmt.Fprintf(&b, "  %-22s %d/%d  %3.0f%%", "TOTAL", overall.pass, overall.total,
		100*float64(overall.pass)/float64(overall.total))
	if errored > 0 {
		fmt.Fprintf(&b, "\n\n  %d run(s) never reached the model and are excluded — "+
			"this rate covers %d of %d attempted.", errored, overall.total, overall.total+errored)
	}
	return b.String()
}

// writeMeta records what produced these numbers, next to the numbers.
func writeMeta(runDir, backend, model string, contextLimit int) {
	meta := map[string]any{
		"backend":       backend,
		"model":         model,
		"context_limit": contextLimit,
		"started":       time.Now().Format(time.RFC3339),
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "meta.json"), data, 0o644)
}

func loadProbes(dir, only string) ([]Probe, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Probe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var p Probe
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if strings.TrimSpace(p.Task) == "" {
			return nil, fmt.Errorf("%s: task is empty", path)
		}
		// A probe with nothing to assert passes unconditionally, which is
		// worse than not having the probe at all.
		if len(p.Verify) == 0 && len(p.Trace) == 0 && len(p.Answer) == 0 && p.MaxSteps == 0 {
			return nil, fmt.Errorf("%s: nothing asserted — it would score itself", path)
		}
		p.Name = name
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// copyTree copies src into dst, creating dst if needed.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return fmt.Errorf("seed %s does not exist", src)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
