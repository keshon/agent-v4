package agent

import (
	"strings"
	"testing"
)

func TestTruncateMiddle_ShortUnchanged(t *testing.T) {
	s := "short"
	if got := TruncateMiddle(s, 300); got != s {
		t.Fatalf("got %q, want %q", got, s)
	}
}

func TestTruncateMiddle_ZeroMeansNoLimit(t *testing.T) {
	s := strings.Repeat("a", 1000)
	if got := TruncateMiddle(s, 0); got != s {
		t.Fatalf("expected full string, got len %d", len(got))
	}
}

func TestTruncateMiddle_LongHasEllipsis(t *testing.T) {
	s := strings.Repeat("x", 500) + strings.Repeat("y", 500)
	got := TruncateMiddle(s, 100)
	if len(got) > 100 {
		t.Fatalf("len %d, want <= 100", len(got))
	}
	if !strings.Contains(got, " ... ") {
		t.Fatalf("expected middle ellipsis, got %q", got)
	}
	if !strings.HasPrefix(got, "xxx") || !strings.HasSuffix(got, "yyy") {
		t.Fatalf("expected head and tail preserved, got %q", got)
	}
}
