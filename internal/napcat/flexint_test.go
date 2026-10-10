package napcat

import (
	"encoding/json"
	"testing"
)

// NapCat sends `level` as a string and `qq_level` as a number in the same
// object, so the member list must tolerate both.
func TestGroupMemberDecodesMixedLevelTypes(t *testing.T) {
	raw := []byte(`[
	  {"user_id":54321,"nickname":"a","card":"","level":"7","qq_level":42,"join_time":1791632992},
	  {"user_id":99999,"nickname":"b","card":"","level":"0","qq_level":3,"join_time":0},
	  {"user_id":11111,"nickname":"c","card":"","level":"","qq_level":0,"join_time":5}
	]`)

	var ms []GroupMember
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("want 3 members, got %d", len(ms))
	}
	if ms[0].Level != 7 || ms[0].QQLevel != 42 || ms[0].JoinTime != 1791632992 {
		t.Errorf("first member mis-decoded: %+v", ms[0])
	}
	if ms[2].Level != 0 || ms[2].QQLevel != 0 {
		t.Errorf("empty level should decode to 0: %+v", ms[2])
	}
}

// A non-numeric level must not abort decoding of the whole roster.
func TestGroupMemberToleratesGarbageLevel(t *testing.T) {
	raw := []byte(`[{"user_id":1,"nickname":"a","level":"unknown","qq_level":1}]`)

	var ms []GroupMember
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatalf("a garbage level must not fail the decode: %v", err)
	}
	if len(ms) != 1 || ms[0].QQ != 1 || ms[0].Level != 0 {
		t.Errorf("unexpected: %+v", ms)
	}
}
