package mission

import "testing"

// The commands below are verbatim from probe 16-mission-fix traces on
// 2026-09-05 (eval/results/20260905-140056), lowercased the way the
// validator sees them. The rejected one killed the mission three runs out
// of three; the accepted ones came from the same plans and must keep
// working, or this guard trades one broken probe for several.
func TestMutatingShellCheck(t *testing.T) {
	for _, tc := range []struct {
		cmd    string
		reject bool
	}{
		// The offender: the check performed the subtask.
		{`python -c "with open('numbers.txt', 'w') as f: f.write('3\n5\n34\n')"`, true},

		// Real checks from the same runs. All observe.
		{`grep -q '^3$' numbers.txt && grep -q '^5$' numbers.txt`, false},
		{`python3 stats.py | grep -q '^42$'`, false},
		{`python stats.py | findstr 42 || python stats.py | grep 42`, false},
		{`python stats.py`, false},
		{`go test ./internal/tools/`, false},

		// Other writes.
		{`echo 42 > out.txt`, true},
		{`cat a.txt >> b.txt`, true},
		{`touch marker`, true},
		{`mkdir build`, true},
		{`rm -rf dist`, true},

		// Must not trip: redirection lookalikes and mutating words that
		// are not the command.
		{`go build ./... 2>&1`, false},
		{`grep -q '=>' handler.js`, false},
		{`test $(wc -l < f) -ge 3`, false},
		{`grep -q 'mkdir' setup.sh`, false},
		{`python -c "print(open('numbers.txt').read())"`, false},
	} {
		got := mutatingShellCheck(tc.cmd) != ""
		if got != tc.reject {
			verb := "rejected"
			if !got {
				verb = "accepted"
			}
			t.Errorf("%s %q; want reject=%v", verb, tc.cmd, tc.reject)
		}
	}
}
