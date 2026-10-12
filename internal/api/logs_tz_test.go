package api

import (
	"testing"
	"time"
)

// The log page shows one operator-day, so the response must say which timezone
// that day is in. Without it the rows are rendered in the browser's timezone and
// a record filed under 10-13 can read "10-12 15:54" — the exact confusion the
// day boundary was fixed to avoid.
func TestLogsResponseCarriesTheOperatorTimezone(t *testing.T) {
	env, m := newLogEnv(t)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	m.SetLocation(loc)
	m.Logger().Info("x")

	var got struct {
		Day string `json:"day"`
		TZ  string `json:"tz"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.TZ != "Asia/Shanghai" {
		t.Errorf("tz = %q, want Asia/Shanghai", got.TZ)
	}
	if got.Day == "" {
		t.Error("day is empty")
	}
}

// The days listing carries it too, so the picker can label the files.
func TestLogDaysResponseCarriesTheTimezone(t *testing.T) {
	env, m := newLogEnv(t)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	m.SetLocation(loc)

	var got struct {
		TZ string `json:"tz"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs/days", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.TZ != "Asia/Shanghai" {
		t.Errorf("tz = %q", got.TZ)
	}
}
