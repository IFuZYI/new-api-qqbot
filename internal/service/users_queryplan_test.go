package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/logger"

	"qqbot/internal/store"
)

// countingLogger counts the SQL statements GORM actually executes, so a test
// can assert the shape of a query plan rather than guess at it.
//
// The user directory used to issue a GetCheckin + CountCheckins pair inside the
// per-member loop: 2 queries per member to answer two questions that one
// set-wise read each already answer. Nothing failed — the page was just O(n)
// round-trips, on every debounced filter change. A test that only checks the
// returned rows cannot see that, so this counts statements.
type countingLogger struct {
	mu      sync.Mutex
	queries []string
}

func (l *countingLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *countingLogger) Info(context.Context, string, ...interface{})  {}
func (l *countingLogger) Warn(context.Context, string, ...interface{})  {}
func (l *countingLogger) Error(context.Context, string, ...interface{}) {}

func (l *countingLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if fc == nil {
		return
	}
	sql, _ := fc()
	l.mu.Lock()
	l.queries = append(l.queries, sql)
	l.mu.Unlock()
}

// count returns how many executed statements contain sub (case-insensitive).
func (l *countingLogger) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, q := range l.queries {
		if strings.Contains(strings.ToLower(q), strings.ToLower(sub)) {
			n++
		}
	}
	return n
}

// TestUserDirectoryDoesNotQueryPerMember pins the directory's query plan.
//
// The per-member form of this function grew linearly with the roster: with 40
// members it issued 80 extra statements. The assertion below is deliberately
// about the SHAPE (a bounded number of statements, and none of them a
// per-member Count), not about an exact total, so an unrelated new
// set-wise read does not make the test brittle.
func TestUserDirectoryDoesNotQueryPerMember(t *testing.T) {
	svc, st, _, _ := newTestService(t)

	const members = 40
	for i := 0; i < members; i++ {
		qq := int64(100000 + i)
		if err := st.UpsertMembers(1, []store.Member{{QQ: qq, Nickname: "m"}}); err != nil {
			t.Fatalf("seed member %d: %v", i, err)
		}
		// Give every member a check-in, so the per-member path would have had
		// something to find and the counts cannot pass by accident.
		if err := st.RecordCheckin(store.Checkin{QQ: qq, Date: "2026-10-11", Source: "board"}); err != nil {
			t.Fatalf("seed checkin %d: %v", i, err)
		}
	}

	// Install the counter only for the measured call.
	log := &countingLogger{}
	prev := st.DB().Config.Logger
	st.DB().Config.Logger = log
	t.Cleanup(func() { st.DB().Config.Logger = prev })

	rows, err := svc.UserDirectory(UserFilter{}, "2026-10-11")
	if err != nil {
		t.Fatalf("UserDirectory: %v", err)
	}
	if len(rows) != members {
		t.Fatalf("expected %d rows, got %d", members, len(rows))
	}
	// The data must still be right: every member checked in on that date, so
	// every row must report it, and the lifetime total must be 1.
	for _, r := range rows {
		if !r.CheckedToday {
			t.Fatalf("row %d lost its check-in: %+v", r.QQ, r)
		}
		if r.CheckinDays != 1 {
			t.Fatalf("row %d has CheckinDays=%d, want 1", r.QQ, r.CheckinDays)
		}
	}

	total := log.count("")
	perMember := log.count("count(*) from `checkins` where qq =")
	if perMember != 0 {
		t.Errorf("directory issued %d per-member COUNT(checkins) statements; it must use one grouped read", perMember)
	}
	// 40 members must not cost anywhere near 40 statements. The real plan is
	// ~5; allow generous headroom for GORM's own bookkeeping but keep the bound
	// far below O(n).
	if total > members/2 {
		t.Errorf("directory ran %d statements for %d members (queries: %v)", total, members, log.queries)
	}
}
