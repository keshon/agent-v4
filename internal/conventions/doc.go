// Package conventions turns the checkable half of docs/conventions.md into a
// test, so the document's claims are facts rather than intentions. It has no
// runtime role and nothing imports it.
//
// A rule belongs here only if violating it can be detected mechanically; the
// rest stay prose and are enforced by review, which the document says plainly
// rather than implying tooling that does not exist.
//
// The wording of each enforced rule is read back out of the document for the
// failure message, so the sentence a contributor reads is the one the build
// quotes at them. TestDocumentAndChecksAgree keeps the two in step.
package conventions
