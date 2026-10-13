package store

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

// captureQueries records the SQL of every query GORM runs, so a test can assert
// what a real call path reads rather than what a hand-written query would read.
//
// A hand-written probe is vacuous here: it asserts the test's own SQL, so
// removing the WHERE clause from attachGroups would not fail it.
func captureQueries(t *testing.T, s *Store) *[]string {
	t.Helper()
	var seen []string
	cb := s.db.Callback().Query().After("gorm:query")
	if err := cb.Register("test:capture_sql", func(tx *gorm.DB) {
		seen = append(seen, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register capture callback: %v", err)
	}
	t.Cleanup(func() { _ = s.db.Callback().Query().Remove("test:capture_sql") })
	return &seen
}

// attachGroups must scope its membership read to the members being returned.
//
// The reviewer's finding: it loaded every row of member_groups and filtered in
// Go, so a one-group listing read every other group's links on every panel
// request. Correctness is identical either way, so only the SQL distinguishes
// them — this asserts on the statement the real call path executes.
func TestAttachGroupsScopesItsMembershipQuery(t *testing.T) {
	s := newTestStore(t)
	for i := int64(0); i < 20; i++ {
		if err := s.UpsertMembers(200+i, []Member{{QQ: 8000 + i, Nickname: "other"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertMembers(1, []Member{{QQ: 111, Nickname: "target"}}); err != nil {
		t.Fatal(err)
	}

	seen := captureQueries(t, s)
	ms, err := s.MembersInGroup(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].QQ != 111 {
		t.Fatalf("MembersInGroup(1) = %+v, want just 111", ms)
	}

	var membershipSQL []string
	for _, q := range *seen {
		if strings.Contains(q, "member_groups") {
			membershipSQL = append(membershipSQL, q)
		}
	}
	if len(membershipSQL) == 0 {
		t.Fatalf("no member_groups query was captured; the instrument is not observing: %v", *seen)
	}
	// The membership read must carry the member filter. GORM renders an IN list
	// as `qq IN (111)`, and a JOIN-based listing may instead constrain the
	// join — so accept either shape, but require SOME constraint on qq.
	scoped := false
	for _, q := range membershipSQL {
		if strings.Contains(q, "qq") && (strings.Contains(q, "IN") || strings.Contains(q, "= ?") || strings.Contains(q, "member_groups.qq")) {
			scoped = true
		}
	}
	if !scoped {
		t.Errorf("the membership read is not scoped to the members returned; captured: %v", membershipSQL)
	}
	// And it must NOT be a bare table scan.
	for _, q := range membershipSQL {
		trimmed := strings.TrimSpace(q)
		if strings.HasSuffix(trimmed, "FROM `member_groups`") || strings.HasSuffix(trimmed, "FROM member_groups") {
			t.Errorf("the membership read is an unscoped full scan: %q", q)
		}
	}
}

// The same property for the whole-list path, which must also carry a filter
// rather than reading the table unconditionally.
func TestListMembersScopesItsMembershipQuery(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertMembers(1, []Member{{QQ: 111, Nickname: "a"}, {QQ: 222, Nickname: "b"}}); err != nil {
		t.Fatal(err)
	}

	seen := captureQueries(t, s)
	if _, err := s.ListMembers(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range *seen {
		if !strings.Contains(q, "member_groups") {
			continue
		}
		found = true
		trimmed := strings.TrimSpace(q)
		if strings.HasSuffix(trimmed, "FROM `member_groups`") || strings.HasSuffix(trimmed, "FROM member_groups") {
			t.Errorf("ListMembers read member_groups without a filter: %q", q)
		}
	}
	if !found {
		t.Fatalf("no member_groups query captured; the instrument is not observing: %v", *seen)
	}
}
