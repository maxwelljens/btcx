// Package pipeline hosts the end-to-end security pipeline tests described in
// AGENTS.md §10: hostile RFC 5322 fixtures through mailparse, sanitize and
// render, plus compose round trips.
//
// It has no non-test code; the package clause exists so the directory is a
// valid Go package.
package pipeline
