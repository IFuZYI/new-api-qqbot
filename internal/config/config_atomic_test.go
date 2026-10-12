package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAtomicFileAdoptionRetriesSameContents(t *testing.T) {
	m, st := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	before := m.lastHash
	raw := strings.Replace(string(mustRead(t, m.Path())), "username: napcat", "username: adopted", 1)
	if err := os.WriteFile(m.Path(), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec(`CREATE TRIGGER reject_adopt BEFORE UPDATE ON settings WHEN NEW.key = 'config.napcat' BEGIN SELECT RAISE(ABORT, 'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err == nil {
		t.Fatal("expected adoption failure")
	}
	if m.lastHash != before {
		t.Error("failed adoption advanced hash")
	}
	if err := st.DB().Exec(`DROP TRIGGER reject_adopt`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.NewAPI.Username != "adopted" {
		t.Fatalf("unchanged file was not retried: %+v", got)
	}
}

type atomicCAS interface {
	SaveSectionCAS(string, Config, string) (Config, error)
	SaveCAS(Config, string) (Config, error)
}

func TestAtomicCASConcurrentWriters(t *testing.T) {
	for _, section := range []string{"", SectionNewAPI, SectionNapCat} {
		t.Run(section, func(t *testing.T) {
			m, _ := newTestManager(t)
			if err := m.Save(sample()); err != nil {
				t.Fatal(err)
			}
			cas, ok := any(m).(atomicCAS)
			if !ok {
				t.Fatal("Manager lacks revision-aware CAS")
			}
			rev := m.Revision()
			if section != "" {
				rev = m.SectionRevision(section)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, name := range []string{"first", "second"} {
				go func(name string) {
					<-start
					c := sample()
					c.NewAPI.Username = name
					c.NapCat.WebUIToken = name
					var err error
					if section == "" {
						_, err = cas.SaveCAS(c, rev)
					} else {
						_, err = cas.SaveSectionCAS(section, c, rev)
					}
					results <- err
				}(name)
			}
			close(start)
			success, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					success++
				} else if strings.Contains(err.Error(), "配置已被修改") {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if success != 1 || conflicts != 1 {
				t.Fatalf("success=%d conflicts=%d", success, conflicts)
			}
		})
	}
}

func TestAtomicIndependentSectionsPreserved(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, section := range []string{SectionNewAPI, SectionNapCat} {
		wg.Add(1)
		go func(section string) {
			defer wg.Done()
			<-start
			c := sample()
			c.NewAPI.Username = "gateway"
			c.NapCat.WebUIToken = "qq"
			if err := m.SaveSection(section, c); err != nil {
				t.Error(err)
			}
		}(section)
	}
	close(start)
	wg.Wait()
	got := m.MustLoad()
	if got.NewAPI.Username != "gateway" || got.NapCat.WebUIToken != "qq" {
		t.Fatalf("lost independent change: %+v", got)
	}
}

func TestAtomicManagerSerializesOperations(t *testing.T) {
	for _, op := range []string{"load", "save", "section", "revision", "ensure"} {
		t.Run(op, func(t *testing.T) {
			m, _ := newTestManager(t)
			if err := m.Save(sample()); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			done := make(chan error, 1)
			go func() {
				switch op {
				case "load":
					_, err := m.Load()
					done <- err
				case "save":
					done <- m.Save(sample())
				case "section":
					done <- m.SaveSection(SectionNewAPI, sample())
				case "revision":
					m.Revision()
					done <- nil
				case "ensure":
					done <- m.EnsureFile()
				}
			}()
			var early error
			select {
			case <-done:
				early = errors.New("operation bypassed Manager lock")
			case <-time.After(30 * time.Millisecond):
			}
			m.mu.Unlock()
			if early != nil {
				t.Fatal(early)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("operation deadlocked")
			}
		})
	}
}

func TestAtomicCommitFailureRestoresFile(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			m, st := newTestManager(t)
			old := sample()
			if err := m.Save(old); err != nil {
				t.Fatal(err)
			}
			before := mustRead(t, m.Path())
			beforeHash := m.lastHash
			if !exists {
				if err := os.Remove(m.Path()); err != nil {
					t.Fatal(err)
				}
			}
			// The statements succeed; the deferred FK fails only at COMMIT, after rename.
			for _, sql := range []string{
				`PRAGMA foreign_keys = ON`,
				`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY)`,
				`CREATE TABLE commit_child (id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
				`CREATE TRIGGER reject_commit AFTER UPDATE ON settings BEGIN INSERT INTO commit_child VALUES (99); END`,
			} {
				if err := st.DB().Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			changed := old
			changed.NewAPI.Username = "commit-failed"
			err := m.Save(changed)
			if err == nil {
				t.Fatal("expected commit failure")
			}
			if !strings.Contains(err.Error(), "commit") {
				t.Errorf("commit failure not identified: %v", err)
			}
			if exists {
				if got := mustRead(t, m.Path()); string(got) != string(before) {
					t.Error("commit failure left renamed file in place")
				}
			} else if _, err := os.Stat(m.Path()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("new file not removed: %v", err)
			}
			if m.lastHash != beforeHash {
				t.Error("commit failure advanced hash")
			}
			if got := m.fromStore(); got != old {
				t.Errorf("commit failure changed database: %+v", got)
			}
			if err := st.DB().Exec(`DROP TRIGGER reject_commit`).Error; err != nil {
				t.Fatal(err)
			}
			if err := m.Save(changed); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestAtomicSaveRollsBackBothSections(t *testing.T) {
	m, st := newTestManager(t)
	old := sample()
	if err := m.Save(old); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, m.Path()))
	if err := st.DB().Exec(`CREATE TRIGGER reject_napcat BEFORE UPDATE ON settings WHEN NEW.key = 'config.napcat' BEGIN SELECT RAISE(ABORT, 'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	changed := old
	changed.NewAPI.Username = "changed"
	changed.NapCat.WebUIToken = "changed"
	if err := m.Save(changed); err == nil {
		t.Fatal("expected injected failure")
	}
	var got NewAPI
	if err := st.GetSetting(newAPIKey, &got); err != nil {
		t.Fatal(err)
	}
	if got != old.NewAPI {
		t.Fatalf("first section partially committed: %+v", got)
	}
	if after := string(mustRead(t, m.Path())); after != before {
		t.Fatal("SQL failure changed file")
	}
}

func TestAtomicSaveFileFailureLeavesDatabaseUntouched(t *testing.T) {
	for _, mode := range []string{"rename", "prepare"} {
		t.Run(mode, func(t *testing.T) {
			m, st := newTestManager(t)
			old := sample()
			if err := m.Save(old); err != nil {
				t.Fatal(err)
			}
			oldHash := m.lastHash
			if mode == "rename" {
				if err := os.Remove(m.Path()); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(m.Path(), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				m.path = m.Path() + "/impossible/config.yaml"
			}
			changed := old
			changed.NewAPI.Username = "changed"
			if err := m.Save(changed); err == nil {
				t.Fatal("expected file failure")
			}
			var got NewAPI
			if err := st.GetSetting(newAPIKey, &got); err != nil {
				t.Fatal(err)
			}
			if got != old.NewAPI {
				t.Fatalf("file failure committed database: %+v", got)
			}
			if m.lastHash != oldHash {
				t.Fatal("failed write advanced lastHash")
			}
		})
	}
}
