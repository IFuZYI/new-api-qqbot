package service

import (
	"qqbot/internal/store"
	"testing"
)

func TestDirectoryPreservesEveryOrphanBinding(t *testing.T) {
	for _, member := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			t.Run(map[bool]string{false: "no-member", true: "member"}[member]+map[bool]string{false: "no-cache", true: "cached"}[cached], func(t *testing.T) {
				svc, st, _, _ := newTestService(t)
				if member {
					if err := st.UpsertMembers(1, []store.Member{{QQ: 11}}); err != nil {
						t.Fatal(err)
					}
				}
				for _, qq := range []int64{11, 22} {
					if err := st.SetBinding(store.Binding{QQ: qq, NewAPIUserID: 1, NewAPIUsername: "snapshot", NewAPIEmail: "snapshot@example.org", Source: "manual"}); err != nil {
						t.Fatal(err)
					}
				}
				if cached {
					if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{{ID: 1, Username: "latest", Email: "latest@example.org"}}); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.RecordCheckin(store.Checkin{QQ: 22, Date: "2026-01-01", Source: "message"}); err != nil {
					t.Fatal(err)
				}
				rows, err := svc.UserDirectory(UserFilter{}, "2026-01-01")
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 2 {
					t.Fatalf("lost bound QQs: %+v", rows)
				}
				for i, qq := range []int64{11, 22} {
					row := rows[i]
					if row.QQ != qq || !row.Bound || row.NewAPIUserID != 1 || row.BindSource != "manual" {
						t.Errorf("missing binding: %+v", row)
					}
					if cached && (row.NewAPIUser != "latest" || row.NewAPIEmail != "latest@example.org") {
						t.Errorf("stale cache identity: %+v", row)
					}
					if !cached && row.NewAPIUser != "snapshot" {
						t.Errorf("lost fallback identity: %+v", row)
					}
				}
				if rows[1].InGroup || rows[1].Kind != "account" || !rows[1].CheckedToday || rows[1].CheckinDays != 1 {
					t.Errorf("wrong orphan metadata: %+v", rows[1])
				}
			})
		}
	}
}
