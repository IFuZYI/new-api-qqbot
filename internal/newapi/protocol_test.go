package newapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestR6RequiredData(t *testing.T) {
	for _, body := range []string{`{"success":true}`, `{"success":true,"data":null}`, `{"success":true,"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			c := New(srv.URL)
			if u, err := c.GetUser(9); err == nil {
				t.Errorf("invalid user accepted: %+v", u)
			}
			if us, err := c.ListUsers(); err == nil {
				t.Errorf("invalid page accepted: %+v", us)
			}
		})
	}
	for _, id := range []int{-1, 0, 8, 9} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, id) }))
			defer srv.Close()
			_, err := New(srv.URL).GetUser(9)
			if (err == nil) != (id == 9) {
				t.Fatalf("id=%d err=%v", id, err)
			}
		})
	}
}

func TestR6PageInfoRequired(t *testing.T) {
	for _, data := range []string{
		`{"items":[],"total":0}`, `{"items":[],"total":0,"page":1}`, `{"items":[],"total":null,"page":1,"page_size":100}`,
		`{"items":null,"total":0,"page":1,"page_size":100}`, `{"total":0,"page":1,"page_size":100}`,
		`{"items":[],"total":0,"page":0,"page_size":100}`, `{"items":[],"total":0,"page":1,"page_size":0}`,
		`{"items":[],"total":-1,"page":1,"page_size":100}`, `{"items":[],"total":0,"page":1,"page_size":101}`,
		`{"items":[],"total":0,"page":1,"page_size":100}`,
	} {
		t.Run(data, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%s}`, data) }))
			defer srv.Close()
			_, err := New(srv.URL).ListUsers()
			valid := data == `{"items":[],"total":0,"page":1,"page_size":100}`
			if (err == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
		})
	}
}
