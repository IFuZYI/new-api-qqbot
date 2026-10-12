package service

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestR1DrawSupportsFullIntRange(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	for _, r := range [][2]int{{0, math.MaxInt}, {0, math.MaxInt - 1}, {1, math.MaxInt}, {math.MaxInt - 1, math.MaxInt}, {math.MaxInt, math.MaxInt}, {0, 0}, {0, 1}} {
		t.Run(fmt.Sprint(r), func(t *testing.T) {
			cfg := DefaultSettings()
			cfg.RewardMin = r[0]
			cfg.RewardMax = r[1]
			if err := svc.SaveSettings(cfg); err != nil {
				t.Fatalf("valid range rejected: %v", err)
			}
			for i := 0; i < 100; i++ {
				n := svc.draw(cfg)
				if n < r[0] || n > r[1] {
					t.Fatalf("draw %d outside %v", n, r)
				}
			}
		})
	}
	if MaxIntervalMinutes != int(int64(math.MaxInt64)/int64(time.Minute)) {
		t.Fatal("invalid shared duration bound")
	}
}

func TestR1SaveSettingsNumericValidation(t *testing.T) {
	maxMinutes := int(int64(math.MaxInt64) / int64(time.Minute))
	fields := []struct {
		name string
		set  func(*Settings, int)
	}{
		{"reward_min", func(c *Settings, v int) { c.RewardMin = v }},
		{"reward_max", func(c *Settings, v int) { c.RewardMax = v }},
		{"member", func(c *Settings, v int) { c.MemberSyncMinutes = v }},
		{"checkin", func(c *Settings, v int) { c.CheckinSyncMinutes = v }},
		{"bind", func(c *Settings, v int) { c.BindPatrolMinutes = v }},
	}
	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			svc, st, _, _ := newTestService(t)
			original := DefaultSettings()
			if err := svc.SaveSettings(original); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetSettingRaw(settingsKey)
			if err != nil {
				t.Fatal(err)
			}
			cfg := original
			f.set(&cfg, -1)
			if err := svc.SaveSettings(cfg); err == nil {
				t.Error("accepted negative value")
			}
			after, err := st.GetSettingRaw(settingsKey)
			if err != nil || after != before {
				t.Fatalf("rejected save changed data: %q %v", after, err)
			}
			if f.name == "reward_min" || f.name == "reward_max" {
				return
			}
			for _, v := range []int{0, maxMinutes - 1, maxMinutes, maxMinutes + 1} {
				cfg = original
				f.set(&cfg, v)
				err := svc.SaveSettings(cfg)
				if (v > maxMinutes) != (err != nil) {
					t.Errorf("interval %d: %v", v, err)
				}
			}
		})
	}
}
