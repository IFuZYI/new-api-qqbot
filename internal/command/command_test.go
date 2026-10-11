package command

import "testing"

// A literal pattern matches the message exactly, ignoring surrounding
// whitespace: QQ clients routinely append a space or a newline to what the
// member typed.
func TestLiteralMatchesExactTrimmedMessage(t *testing.T) {
	set, err := Parse(">>绑定<<")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, raw := range []string{">>绑定<<", " >>绑定<< ", ">>绑定<<\n", "\t>>绑定<<\r\n"} {
		if !set.Matches(raw) {
			t.Errorf("%q should match a literal pattern", raw)
		}
	}
}

// Extra content must not trigger a literal: a member asking about the command
// is not issuing it.
func TestLiteralRejectsExtraContent(t *testing.T) {
	set, err := Parse(">>绑定<<")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, raw := range []string{">>绑定<< 你好", "你好 >>绑定<<", ">>绑定<<abc", ">>绑定", "绑定<<", "绑定", ">> 绑定 <<"} {
		if set.Matches(raw) {
			t.Errorf("%q must not match the literal pattern", raw)
		}
	}
}

// Several patterns, one per line, are all live at once.
func TestMultiplePatternsAllMatch(t *testing.T) {
	set, err := Parse("/绑定\n<绑定>\n>>绑定<<\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(set.patterns) != 3 {
		t.Fatalf("want 3 patterns, got %d", len(set.patterns))
	}
	for _, raw := range []string{"/绑定", "<绑定>", ">>绑定<<"} {
		if !set.Matches(raw) {
			t.Errorf("%q should match one of the configured patterns", raw)
		}
	}
	if set.Matches("/解绑") {
		t.Error("an unconfigured command must not match")
	}
}

// Blank lines (and a trailing newline) are ignored, so an operator can lay the
// list out however they like.
func TestBlankLinesAreIgnored(t *testing.T) {
	set, err := Parse("\n\n  \n/签到\n\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(set.patterns) != 1 {
		t.Fatalf("want 1 pattern, got %d", len(set.patterns))
	}
}

// A slash-delimited pattern is a regular expression.
func TestRegexPatternMatches(t *testing.T) {
	set, err := Parse(`/^绑定\s*\d*$/`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !set.Matches("绑定 123") {
		t.Error("regex should match 绑定 123")
	}
	if set.Matches("我要绑定") {
		t.Error("the anchored regex must not match extra text")
	}
}

// A regex is matched against the whole trimmed message, so an unanchored
// expression behaves as a search rather than an equality test.
func TestRegexIsASearchWhenUnanchored(t *testing.T) {
	set, err := Parse(`/绑定/`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !set.Matches("我要绑定") {
		t.Error("an unanchored regex should match anywhere in the message")
	}
}

// The `i` flag makes a regex case-insensitive.
func TestRegexCaseInsensitiveFlag(t *testing.T) {
	set, err := Parse("/bind/i")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, raw := range []string{"bind", "BIND", "Bind"} {
		if !set.Matches(raw) {
			t.Errorf("%q should match a case-insensitive regex", raw)
		}
	}
}

// A broken expression is an error, not a silently dead pattern.
func TestInvalidRegexIsRejected(t *testing.T) {
	if _, err := Parse("/([unclosed/"); err == nil {
		t.Fatal("an invalid regular expression must be reported")
	}
}

// A literal that merely contains a slash must stay literal: only a
// slash-delimited line is read as an expression.
func TestLiteralWithSlashStaysLiteral(t *testing.T) {
	set, err := Parse("绑定/解绑")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !set.Matches("绑定/解绑") {
		t.Error("a literal containing a slash should still match literally")
	}
}

// An empty message never matches, however permissive the pattern.
func TestEmptyMessageNeverMatches(t *testing.T) {
	set := MustParse("/.*/")
	if set.Matches("") || set.Matches("   ") || set.Matches("\n") {
		t.Error("an empty message must not match anything")
	}
}

// An empty configuration matches nothing at all.
func TestEmptyConfigMatchesNothing(t *testing.T) {
	set, err := Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !set.Empty() {
		t.Error("an empty configuration should report Empty")
	}
	if set.Matches("/绑定") {
		t.Error("an empty configuration must match nothing")
	}
}

// The shipped defaults are valid and cover the documented commands.
func TestDefaultPatternsAreValid(t *testing.T) {
	if DefaultBindPatterns == "" || DefaultCheckinPatterns == "" {
		t.Fatal("the defaults must not be empty")
	}
	set := MustParse(DefaultBindPatterns)
	if !set.Matches("/绑定") || !set.Matches("<绑定>") || !set.Matches(">>绑定<<") {
		t.Errorf("the default bind patterns must cover the documented commands: %q", DefaultBindPatterns)
	}
	cs := MustParse(DefaultCheckinPatterns)
	if !cs.Matches("/签到") || !cs.Matches("<签到>") || !cs.Matches(">>签到<<") {
		t.Errorf("the default check-in patterns must cover the documented commands: %q", DefaultCheckinPatterns)
	}
}
