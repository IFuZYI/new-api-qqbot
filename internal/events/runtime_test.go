package events

import (
	"encoding/json"
	"testing"
	"time"
)

type datedService struct {
	*featureSvc
	now time.Time
	loc *time.Location
}

func (s datedService) MessageIsToday(timestamp int64) bool {
	return time.Unix(timestamp, 0).In(s.loc).Format("2006-01-02") == s.now.In(s.loc).Format("2006-01-02")
}

func TestMessageCheckinRequiresCurrentLocalDay(t *testing.T) {
	loc := time.FixedZone("east", 8*60*60)
	now := time.Date(2026, 10, 12, 0, 30, 0, 0, loc)
	for _, tc := range []struct {
		name string
		at   any
		want int
	}{
		{"yesterday", now.Add(-time.Hour).Unix(), 0},
		{"tomorrow", now.Add(24 * time.Hour).Unix(), 0},
		{"same day different UTC date", now.Add(2 * time.Hour).Unix(), 1},
		{"epoch", int64(0), 0},
		{"negative", int64(-1), 0},
		{"legacy missing time", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &featureSvc{monitored: map[int64]bool{1: true}, anyMessage: true}
			l := New(func() (string, string) { return "", "" }, datedService{svc, now, loc})
			msg := map[string]any{"post_type": "message", "message_type": "group", "group_id": 1, "user_id": 2, "raw_message": "hi"}
			if tc.at != nil {
				msg["time"] = tc.at
			}
			raw, _ := json.Marshal(msg)
			l.Handle(raw)
			if got := len(svc.checkinCalls()); got != tc.want {
				t.Fatalf("checkins=%d want %d", got, tc.want)
			}
		})
	}
}

func TestMalformedEventIdentityHasNoEffects(t *testing.T) {
	for _, raw := range []string{
		`{"post_type":"message","message_type":"private","group_id":1,"user_id":2,"raw_message":"hi"}`,
		`{"post_type":"message","message_type":"unknown","group_id":1,"user_id":2,"raw_message":"hi"}`,
		`{"post_type":"message","message_type":"group","group_id":-1,"user_id":2,"raw_message":"hi"}`,
		`{"post_type":"message","message_type":"group","group_id":1,"user_id":-2,"raw_message":"hi"}`,
		`{"post_type":"notice","notice_type":"group_increase","group_id":-1,"user_id":2}`,
		`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":-2}`,
	} {
		svc := &featureSvc{monitored: map[int64]bool{1: true, -1: true}, anyMessage: true, bindCommands: map[string]bool{"hi": true}, bindOnJoin: true}
		newFeatureListener(svc).Handle([]byte(raw))
		b, j, e := svc.recorded()
		if len(b)+len(j)+len(e)+len(svc.checkinCalls()) != 0 {
			t.Errorf("malformed event caused effects: %s", raw)
		}
	}
}
