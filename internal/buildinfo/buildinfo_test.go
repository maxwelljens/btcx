package buildinfo

import "testing"

func TestDefaults(t *testing.T) {
	if Version() != "(devel)" {
		t.Errorf("Version = %q, want (devel)", Version())
	}
	if Commit() != "unknown" {
		t.Errorf("Commit = %q, want unknown", Commit())
	}
	if Date() != "unknown" {
		t.Errorf("Date = %q, want unknown", Date())
	}
	want := "(devel) (unknown) built unknown"
	if got := String(); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}
