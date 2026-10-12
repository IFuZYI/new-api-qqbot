package service

import "testing"

func TestDirectoryUsesLatestAccountIdentity(t *testing.T) {
	svc, st, na := seedUserDirectory(t)
	na.users()[1].Username = "renamed-account"
	na.users()[1].Email = "current@example.org"
	if _, err := svc.SyncNewAPIUsers(); err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"renamed-account", "current@example.org"} {
		rows, err := svc.UserDirectory(UserFilter{Keyword: keyword}, "2026-01-01")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].QQ != 111 || rows[0].NewAPIUser != "renamed-account" || rows[0].NewAPIEmail != "current@example.org" {
			t.Errorf("stale identity for %q: %+v", keyword, rows)
		}
	}
	if err := st.ReplaceNewAPIUsers(nil); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.UserDirectory(UserFilter{Keyword: "alice"}, "2026-01-01")
	if err != nil || len(rows) != 1 || rows[0].NewAPIUser != "alice" || rows[0].NewAPIEmail != "111@qq.com" {
		t.Fatalf("missing-cache fallback lost: %+v %v", rows, err)
	}
}
