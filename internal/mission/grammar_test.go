package mission

import (
	"encoding/json"
	"strings"
	"testing"
)

// Two encodings of one contract drift unless something reads them
// together. The grammar and the schema must at least agree on the field
// names and the enum, which is what a reader would assume and what a
// backend switch would otherwise silently break.
func TestSchemasMatchTheirGrammars(t *testing.T) {
	for _, tc := range []struct {
		name, schema, grammar string
		fields                []string
	}{
		{"plan", PlanSchema, PlanGrammar,
			[]string{"subtasks", "id", "milestone", "title", "goal", "acceptance", "files_hint", "check"}},
		{"decision", DecisionSchema, DecisionGrammar,
			[]string{"verdict", "notes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(tc.schema), &doc); err != nil {
				t.Fatalf("schema is not valid JSON: %v", err)
			}
			for _, f := range tc.fields {
				if !strings.Contains(tc.schema, `"`+f+`"`) {
					t.Errorf("schema is missing field %q that the grammar requires", f)
				}
				if !strings.Contains(tc.grammar, f) {
					t.Errorf("grammar is missing field %q that the schema declares", f)
				}
			}
		})
	}
}

// The check types the planner may emit are the ones RunCheck implements.
// A schema that admits a sixth would let a plan through that nothing can
// verify.
func TestPlanSchemaAdmitsOnlyImplementedCheckTypes(t *testing.T) {
	for _, ct := range []string{"shell", "file_exists", "content_contains", "http", "none"} {
		if !strings.Contains(PlanSchema, `"`+ct+`"`) {
			t.Errorf("PlanSchema omits check type %q", ct)
		}
	}
	if strings.Count(PlanSchema, `"enum"`) != 1 {
		t.Error("expected exactly one enum in PlanSchema (the check type)")
	}
}
