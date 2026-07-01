package agent

// TruncateMiddle shortens s to at most max bytes, keeping the start and end
// with " ... " in the middle. max <= 0 means no limit.
func TruncateMiddle(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const sep = " ... "
	sepLen := len(sep)
	if max <= sepLen+2 {
		if max <= 3 {
			return s[:max]
		}
		return s[:max-3] + "..."
	}
	side := (max - sepLen) / 2
	return s[:side] + sep + s[len(s)-side:]
}
