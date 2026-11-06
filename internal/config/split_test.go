package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr string
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace only", in: " \t\n ", want: nil},
		{name: "single word", in: "nvim", want: []string{"nvim"}},
		{name: "simple args", in: "alacritty -e nvim {file}", want: []string{"alacritty", "-e", "nvim", "{file}"}},
		{name: "tabs", in: "pass\tshow\tmail", want: []string{"pass", "show", "mail"}},
		{name: "multiple spaces", in: "pass   show  mail", want: []string{"pass", "show", "mail"}},
		{name: "leading and trailing space", in: "  pass show  ", want: []string{"pass", "show"}},
		{name: "single quotes", in: "pass show 'my mail'", want: []string{"pass", "show", "my mail"}},
		{name: "double quotes", in: `pass show "my mail"`, want: []string{"pass", "show", "my mail"}},
		{name: "quoted empty", in: "pass show ''", want: []string{"pass", "show", ""}},
		{name: "adjacent quoted segments", in: `a"b c"d`, want: []string{"ab cd"}},
		{name: "escaped space", in: `pass show my\ mail`, want: []string{"pass", "show", "my mail"}},
		{name: "escaped quote in double quotes", in: `sh -c "echo \"hi\""`, want: []string{"sh", "-c", `echo "hi"`}},
		{name: "backslash literal in single quotes", in: `a '\n'`, want: []string{"a", `\n`}},
		{name: "shell metacharacters are literal", in: `sh -c "rm -rf /" | cat`, want: []string{"sh", "-c", "rm -rf /", "|", "cat"}},
		{name: "dollar is not expanded", in: `echo $HOME`, want: []string{"echo", "$HOME"}},
		{name: "semicolon is literal", in: "a;b c", want: []string{"a;b", "c"}},
		{name: "unterminated single quote", in: "pass show 'my mail", wantErr: "unterminated ' quote"},
		{name: "unterminated double quote", in: `pass show "my mail`, wantErr: "unterminated \" quote"},
		{name: "trailing backslash", in: `pass \`, wantErr: "trailing backslash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitCommand(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("SplitCommand(%q) = %v, want error %q", tt.in, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitCommand(%q) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitCommand(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// SplitCommand must never gain shell behaviour: a crafted editor template
// stays a single argv, so no shell parses it.
func TestSplitCommandNoShellSemantics(t *testing.T) {
	got, err := SplitCommand(`sh -c "rm -rf /"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sh", "-c", "rm -rf /"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitCommand = %#v, want %#v", got, want)
	}
	// The dangerous part must be one argument, not three.
	if len(got) != 3 || got[2] != "rm -rf /" {
		t.Fatalf("argument grouping changed: %#v", got)
	}
}
