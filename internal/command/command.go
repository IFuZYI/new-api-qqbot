// Package command decides whether an incoming group message carries one of the
// operator's configured commands.
//
// A command pattern is matched against a single line of text, one pattern per
// line. A pattern wrapped in slashes (/签到/i) is a regular expression; anything
// else is a literal. Matching is exact after trimming surrounding whitespace,
// which is what stops a message that merely mentions the command from
// triggering it.
package command

import (
	"regexp"
	"strings"
)

// DefaultBindPatterns and DefaultCheckinPatterns are the shipped command lists.
// They are the commands the operator-facing docs name, so a fresh instance
// behaves the way the documentation describes before anything is configured.
const (
	DefaultBindPatterns = `/绑定
<绑定>
>>绑定<<`
	DefaultCheckinPatterns = `/签到
<签到>
>>签到<<`
)

// Pattern is one configured command. Exactly one of Literal and re is set.
type Pattern struct {
	// Raw is the pattern as the operator wrote it.
	Raw string
	// Literal is set for a plain-text pattern (the common case).
	Literal string
	// CaseInsensitive records the `i` flag of a regex pattern.
	CaseInsensitive bool

	re *regexp.Regexp
}

// Set is a parsed list of patterns. The zero value matches nothing.
type Set struct {
	patterns []Pattern
}

// Parse splits the configured text into patterns: one per non-empty line,
// trimmed. A line of the form /expr/ or /expr/flags is a regular expression;
// every other line is a literal.
//
// An invalid expression is returned as an error rather than silently dropped:
// a pattern the operator wrote but which can never match is exactly the
// confusing failure this whole feature has to avoid.
func Parse(text string) (Set, error) {
	var out Set
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, err := parsePattern(line)
		if err != nil {
			return Set{}, err
		}
		out.patterns = append(out.patterns, p)
	}
	return out, nil
}

// MustParse is Parse for a pattern list that is known to be valid (the shipped
// defaults). It panics on a bad pattern, which a test catches.
func MustParse(text string) Set {
	set, err := Parse(text)
	if err != nil {
		panic("command: invalid built-in pattern list: " + err.Error())
	}
	return set
}

// parsePattern interprets one line.
func parsePattern(line string) (Pattern, error) {
	body, flags, ok := splitRegex(line)
	if !ok {
		return Pattern{Raw: line, Literal: line}, nil
	}
	expr := body
	if strings.Contains(flags, "i") {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return Pattern{}, err
	}
	return Pattern{Raw: line, CaseInsensitive: strings.Contains(flags, "i"), re: re}, nil
}

// splitRegex recognises /expr/ and /expr/flags. It reports false for anything
// that is not a complete slash-delimited pattern, so a literal that merely
// contains a slash (>>绑定/解绑<<) stays literal.
func splitRegex(line string) (body, flags string, ok bool) {
	if !strings.HasPrefix(line, "/") {
		return "", "", false
	}
	// The closing slash is the last one: /a\/b/ is not supported (escaping the
	// delimiter is not worth the ambiguity), so a literal containing a slash
	// must not be read as a pattern either.
	end := strings.LastIndex(line[1:], "/")
	if end < 0 {
		return "", "", false
	}
	end++ // index in line
	body = line[1:end]
	flags = line[end+1:]
	for _, r := range flags {
		if r != 'i' {
			return "", "", false
		}
	}
	return body, flags, true
}

// Matches reports whether the message carries one of the patterns. The message
// is trimmed first, so surrounding whitespace (and a trailing newline) is
// tolerated while extra content is not.
func (s Set) Matches(message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	for _, p := range s.patterns {
		if p.re != nil {
			if p.re.MatchString(msg) {
				return true
			}
			continue
		}
		if p.Literal == msg {
			return true
		}
	}
	return false
}

// Empty reports whether there are no patterns, which is what a switched-off
// feature looks like.
func (s Set) Empty() bool { return len(s.patterns) == 0 }
