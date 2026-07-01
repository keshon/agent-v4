package tools

import (
	"encoding/json"
	"fmt"
)

// rejectUnknownFields errors when args contain keys outside the allowed
// set — catches models that send plausible-but-wrong field names (e.g.
// "command" instead of "pattern") instead of silently ignoring them.
func rejectUnknownFields(args json.RawMessage, allowed ...string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return fmt.Errorf("expected object, got invalid JSON: %w", err)
	}
	allow := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		allow[k] = true
	}
	for k := range raw {
		if !allow[k] {
			return fmt.Errorf("unknown field %q (expected: %s)", k, joinOr(allowed))
		}
	}
	return nil
}

func joinOr(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	default:
		out := parts[0]
		for i := 1; i < len(parts)-1; i++ {
			out += ", " + parts[i]
		}
		return out + ", or " + parts[len(parts)-1]
	}
}
