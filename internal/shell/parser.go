package shell

import (
	"strings"
	"unicode"
)

// Parse parses a command line into tokens using shell-like rules
func Parse(line string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	escaped := false

	for _, char := range line {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}

		if char == '\\' {
			escaped = true
			continue
		}

		if inQuote {
			if char == quoteChar {
				inQuote = false
				quoteChar = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}

		if char == '"' || char == '\'' {
			inQuote = true
			quoteChar = char
			continue
		}

		if unicode.IsSpace(char) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteRune(char)
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}
