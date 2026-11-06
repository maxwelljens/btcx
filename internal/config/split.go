package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// SplitCommand splits a command template ("nvim", "alacritty -e nvim {file}")
// into argv using shell-like quoting rules, and never invokes a shell:
//
//   - unquoted whitespace separates arguments;
//   - '...' and "..." group text and are removed, so arguments may contain
//     spaces;
//   - a backslash outside single quotes escapes the next character;
//   - backslashes are literal inside single quotes.
//
// Shell metacharacters (| & ; < > $ ` * ~) have no meaning: they are ordinary
// characters in the resulting argument, and nothing is expanded or evaluated.
// `sh -c "rm -rf /"` therefore becomes the literal argv
// ["sh", "-c", "rm -rf /"] and gains no shell semantics beyond what the named
// program does with its own arguments.
//
// An unterminated quote or a trailing backslash is an error.
func SplitCommand(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			inArg = true
			continue
		}
		switch quote {
		case '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		default:
			switch {
			case r == '\\':
				escaped = true
				inArg = true
			case r == '\'' || r == '"':
				quote = r
				inArg = true
			case unicode.IsSpace(r):
				if inArg {
					args = append(args, cur.String())
					cur.Reset()
					inArg = false
				}
			default:
				cur.WriteRune(r)
				inArg = true
			}
		}
	}
	if escaped {
		return nil, errors.New("trailing backslash")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
