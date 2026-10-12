package service

import (
	"encoding/json"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
	"strings"
	"testing"
)

func TestStoredRuleNullListsFailClosed(t *testing.T) {
	for _, field := range []string{"source_groups", "conditions"} {
		for _, value := range []string{"null", " \nnull\t"} {
			t.Run(field+value, func(t *testing.T) {
				row := store.PromotionRule{Name: "null", Enabled: true, OnBind: true, TargetGroup: "vip", SourceGroups: "[]", Conditions: "[]"}
				if field == "source_groups" {
					row.SourceGroups = value
				} else {
					row.Conditions = value
				}
				r := ruleFromStore(row)
				if !strings.Contains(r.DecodeError, field) {
					t.Errorf("missing decode error: %+v", r)
				}
				if r.Matches(MemberContext{Bound: true, User: &newapi.User{Group: "default"}}) {
					t.Error("null widened rule")
				}
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"source_groups", "conditions"} {
					if string(got[name]) != "[]" {
						t.Errorf("UI array %s=%s", name, got[name])
					}
				}
			})
		}
	}
	r := ruleFromStore(store.PromotionRule{Enabled: true, OnBind: true, SourceGroups: "[]", Conditions: "[]"})
	if r.DecodeError != "" || !r.Matches(MemberContext{Bound: true}) {
		t.Fatal("explicit empty lists must remain valid")
	}
}
