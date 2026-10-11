package service

import (
	"encoding/json"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// TestCorruptSourceGroupsDoesNotWidenTheRule is the regression test for a
// fail-open path: a rule whose stored source_groups blob cannot be decoded must
// NOT silently become "any group".
//
// The Conditions branch already guards this exact class ("Mark the rule
// unusable rather than widening it", rules_run.go). SourceGroups did not, and
// empty SourceGroups means 任意分组 — so a corrupt blob turned a targeted rule
// into a blanket conversion that moves every bound member's account into the
// target group.
func TestCorruptSourceGroupsDoesNotWidenTheRule(t *testing.T) {
	row := store.PromotionRule{
		ID:           1,
		Name:         "targeted",
		Enabled:      true,
		OnBind:       true,
		SourceGroups: "{not json", // corrupt
		TargetGroup:  "vip",
		Conditions:   "[]",
	}
	r := ruleFromStore(row)

	if r.DecodeError == "" {
		t.Fatalf("a corrupt source_groups blob must mark the rule unusable, got DecodeError=%q SourceGroups=%v",
			r.DecodeError, r.SourceGroups)
	}

	// And the decoded (unusable) rule must not match a member sitting in some
	// unrelated group.
	member := MemberContext{
		QQ:    12345,
		Bound: true,
		User:  &newapi.User{Group: "default"},
	}
	if r.Matches(member) {
		t.Fatalf("a rule with an unreadable source_groups must match nobody, but it matched %+v", member)
	}
}

// TestUnreadableRuleStillMarshalsListsAsArrays pins the wire format of a rule
// whose blob could not be decoded.
//
// The panel types these as arrays and calls `.length` on them with no error
// boundary above it, so a `null` here does not degrade the row — it blanks the
// entire page with "Cannot read properties of null (reading 'length')".
// Reproduced in a real browser: with `source_groups: null` in the payload, the
// nav, main and body all rendered empty.
//
// DecodeError is what marks the rule unusable; the lists stay arrays so the
// failure stays local to the one broken rule.
func TestUnreadableRuleStillMarshalsListsAsArrays(t *testing.T) {
	row := store.PromotionRule{
		ID:           1,
		Name:         "targeted",
		Enabled:      true,
		SourceGroups: "{not json",
		TargetGroup:  "vip",
		Conditions:   "{also not json",
	}
	r := ruleFromStore(row)
	if r.DecodeError == "" {
		t.Fatal("precondition: both blobs are corrupt, so DecodeError should be set")
	}

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"source_groups", "conditions"} {
		if wire[field] == nil {
			t.Errorf("%s marshalled as null (payload: %s); the panel reads .length off it and would blank the page", field, b)
		}
		if _, ok := wire[field].([]any); !ok {
			t.Errorf("%s is %T, want an array", field, wire[field])
		}
	}
}
