package mission

// GBNF grammars for the mission's structured model calls. Both are sent
// as per-request grammars on tool-free Chat calls — the risky
// grammar-plus-tools combination koboldcpp's docs are vague about never
// occurs (see ARCHITECTURE.md "Grammar as a lever").
//
// A grammar guarantees syntax only: the model cannot emit malformed JSON,
// but it can still emit a syntactically perfect plan with vacuous goals
// or an `echo ok` check. Structure is the Go validator's job (plan.go);
// semantics are the human approval gate's. Three cheap layers, each
// catching what the previous one can't.
//
// Grammar notes:
//   - {m,n} repetition bounds are llama.cpp GBNF extensions; current
//     koboldcpp bundles a grammar engine that supports them, but this is
//     backend behavior, not Go-testable — confirm the first live run
//     with -debug before trusting it, and fall back to '*' plus Go-side
//     length/count validation if the backend rejects the grammar.
//   - String contents follow llama.cpp's own json.gbnf character class,
//     so any JSON string the grammar admits is decodable by encoding/json.
//   - Key order inside objects is FIXED. A weak model given key freedom
//     spends tokens deciding; forced order turns each key into a
//     zero-entropy continuation.

// PlanGrammar constrains a planning response to a flat subtask list —
// at most 8 subtasks, each with bounded string lengths, at most 5
// acceptance criteria / file hints, and a typed check.
const PlanGrammar = `root ::= "{" ws "\"subtasks\"" ws ":" ws "[" ws subtask (ws "," ws subtask){0,7} ws "]" ws "}"
subtask ::= "{" ws "\"id\"" ws ":" ws str "," ws "\"milestone\"" ws ":" ws str "," ws "\"title\"" ws ":" ws str "," ws "\"goal\"" ws ":" ws str "," ws "\"acceptance\"" ws ":" ws strarr "," ws "\"files_hint\"" ws ":" ws strarr "," ws "\"check\"" ws ":" ws check ws "}"
check ::= "{" ws "\"type\"" ws ":" ws checktype (ws "," ws checkarg)? ws "}"
checktype ::= "\"shell\"" | "\"file_exists\"" | "\"http\"" | "\"none\""
checkarg ::= ("\"cmd\"" | "\"path\"" | "\"url\"") ws ":" ws str
strarr ::= "[" ws (str (ws "," ws str){0,4})? ws "]"
str ::= "\"" schar{1,300} "\""
schar ::= [^"\\\x7F\x00-\x1F] | "\\" (["\\bfnrt/] | "u" [0-9a-fA-F]{4})
ws ::= [ \t\n]{0,4}
`

// DecisionGrammar constrains a verdict response ("is this done?" /
// "does the result have gaps?") to a two-way choice plus a bounded
// note — the narrowest decision a weak model can be asked to make.
const DecisionGrammar = `root ::= "{" ws "\"verdict\"" ws ":" ws ("\"ok\"" | "\"gaps\"") ws "," ws "\"notes\"" ws ":" ws "\"" schar{0,300} "\"" ws "}"
schar ::= [^"\\\x7F\x00-\x1F] | "\\" (["\\bfnrt/] | "u" [0-9a-fA-F]{4})
ws ::= [ \t\n]{0,4}
`
