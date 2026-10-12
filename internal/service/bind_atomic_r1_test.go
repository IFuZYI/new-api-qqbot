package service

import (
	"qqbot/internal/newapi"
	"qqbot/internal/store"
	"testing"
)

type r1LookupGateway struct {
	Gateway
	entered, resume chan struct{}
	reads           int
}

func (g *r1LookupGateway) FindUserByEmail(addr string) (*newapi.User, error) {
	close(g.entered)
	<-g.resume
	return &newapi.User{ID: 7, Email: addr}, nil
}
func (g *r1LookupGateway) GetUser(id int) (*newapi.User, error) {
	g.reads++
	return g.Gateway.GetUser(id)
}
func TestR1ManualBindingWinsLookup(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	g := &r1LookupGateway{Gateway: svc.gw, entered: make(chan struct{}), resume: make(chan struct{})}
	svc.gw = g
	if _, err := st.CreateRule(store.PromotionRule{Enabled: true, OnBind: true, SourceGroups: `["default"]`, TargetGroup: "vip", Conditions: `[]`}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *store.Binding, 1)
	errs := make(chan error, 1)
	go func() { b, err := svc.BindByCommand(1, 54321); done <- b; errs <- err }()
	<-g.entered
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 99, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	close(g.resume)
	b := <-done
	bindErr := <-errs
	if bindErr != nil {
		t.Errorf("losing auto bind must skip rules: %v", bindErr)
	}
	saved, err := st.GetBinding(54321)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.NewAPIUserID != 99 || saved.NewAPIUserID != 99 || saved.Source != "manual" {
		t.Fatalf("manual binding lost: returned=%+v stored=%+v", b, saved)
	}
	if g.reads != 0 {
		t.Fatalf("losing auto bind triggered rules: %d reads", g.reads)
	}
	// Manual rebinding must remain an overwrite.
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 100, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	saved, err = st.GetBinding(54321)
	if err != nil || saved.NewAPIUserID != 100 {
		t.Fatalf("manual overwrite: %+v %v", saved, err)
	}
}
