package newapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestR6DeletedUsersExcludedAfterCountingAllRows(t *testing.T) {
	for _, search := range []bool{false, true} {
		t.Run(fmt.Sprint(search), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("p") == "1" {
					fmt.Fprint(w, `{"success":true,"data":{"items":[{"id":1,"email":"target@qq.com","DeletedAt":"2026-01-02T03:04:05Z"}],"total":2,"page":1,"page_size":1}}`)
				} else {
					fmt.Fprint(w, `{"success":true,"data":{"items":[{"id":2,"email":"TARGET@qq.com","DeletedAt":null}],"total":2,"page":2,"page_size":1}}`)
				}
			}))
			defer srv.Close()
			c := New(srv.URL)
			if search {
				u, err := c.FindUserByEmail("target@qq.com")
				if err != nil || u == nil || u.ID != 2 {
					t.Fatalf("deleted match included: %+v %v", u, err)
				}
			} else {
				us, err := c.ListUsers()
				if err != nil || len(us) != 1 || us[0].ID != 2 {
					t.Fatalf("deleted user included: %+v %v", us, err)
				}
			}
			if calls != 2 {
				t.Fatalf("total includes deleted rows: calls=%d", calls)
			}
		})
	}
}

func TestR6OnlyDeletedMatchIsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{"items":[{"id":1,"email":"target@qq.com","DeletedAt":"2026-01-02T03:04:05Z"}],"total":1,"page":1,"page_size":100}}`)
	}))
	defer srv.Close()
	u, err := New(srv.URL).FindUserByEmail("target@qq.com")
	if err != nil || u != nil {
		t.Fatalf("deleted match returned: %+v %v", u, err)
	}
}
