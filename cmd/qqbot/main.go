// Command qqbot is a QQ group monitor that links group members to new-api
// accounts: it mirrors rosters and sign-in boards, binds members by their QQ
// email, promotes accounts into an authorised group, and pays a daily
// check-in reward.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qqbot/internal/api"
	"qqbot/internal/config"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

func main() {
	var (
		addr    = flag.String("addr", ":4096", "HTTP listen address for the WebUI")
		dataDir = flag.String("data", "data", "directory holding the SQLite database")
	)
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "qqbot.db"))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer st.Close()

	// The gateway and NapCat clients are rebuilt per call from the stored
	// configuration, so editing either in the WebUI takes effect immediately
	// without restarting the service.
	gw := &lazyGateway{st: st}
	qq := &lazyQQ{st: st}

	svc := service.New(st, gw, qq)
	sched := scheduler.New(svc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sched.Run(ctx)

	router := api.NewRouter(st, svc)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("qqbot listening on %s (data: %s)", *addr, *dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// lazyGateway builds a new-api client from stored configuration on every call.
type lazyGateway struct{ st *store.Store }

func (l *lazyGateway) client() (*newapi.Client, error) {
	cfg, err := config.Load(l.st)
	if err != nil {
		return nil, err
	}
	if cfg.NewAPI.URL == "" {
		return nil, errNotConfigured
	}
	c := newapi.New(cfg.NewAPI.URL)
	if err := c.Login(cfg.NewAPI.Username, cfg.NewAPI.Password); err != nil {
		return nil, err
	}
	return c, nil
}

func (l *lazyGateway) FindUserByEmail(addr string) (*newapi.User, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	return c.FindUserByEmail(addr)
}

func (l *lazyGateway) SetGroup(u *newapi.User, group string) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.SetGroup(u, group)
}

func (l *lazyGateway) AddQuota(userID, value int) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	return c.AddQuota(userID, value)
}

// lazyQQ prefers the OneBot HTTP endpoint and falls back to the WebUI bridge,
// which needs no network adapter to be enabled.
type lazyQQ struct{ st *store.Store }

func (l *lazyQQ) onebot() (*napcat.Client, error) {
	cfg, err := config.Load(l.st)
	if err != nil {
		return nil, err
	}
	if cfg.NapCat.OneBotURL == "" {
		return nil, errNotConfigured
	}
	return napcat.New(cfg.NapCat.OneBotURL, cfg.NapCat.OneBotToken), nil
}

func (l *lazyQQ) webui() (*napcat.WebUI, error) {
	cfg, err := config.Load(l.st)
	if err != nil {
		return nil, err
	}
	if cfg.NapCat.WebUIURL == "" {
		return nil, errNotConfigured
	}
	w := napcat.NewWebUI(cfg.NapCat.WebUIURL, cfg.NapCat.WebUIToken)
	if err := w.Login(); err != nil {
		return nil, err
	}
	return w, nil
}

func (l *lazyQQ) GetGroupList() ([]napcat.Group, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupList()
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupList()
}

func (l *lazyQQ) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupMemberList(groupID)
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupMemberList(groupID)
}

func (l *lazyQQ) GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupSignedList(groupID)
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupSignedList(groupID)
}

var errNotConfigured = &notConfiguredError{}

type notConfiguredError struct{}

func (*notConfiguredError) Error() string {
	return "not configured yet: set the new-api and NapCat connection details in the WebUI"
}
