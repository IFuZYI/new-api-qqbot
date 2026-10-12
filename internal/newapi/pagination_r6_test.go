package newapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func r6Page(items []User, total, page, size int) string {
	return okEnvelope(map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func TestR6PaginationBeyond2000(t *testing.T) {
	for _, search := range []bool{false, true} {
		t.Run(fmt.Sprint(search), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				page, _ := strconv.Atoi(r.URL.Query().Get("p"))
				items := []User{}
				for id := (page-1)*100 + 1; id <= page*100 && id <= 2101; id++ {
					u := User{ID: id}
					if id == 2101 {
						u.Email = "target@qq.com"
					}
					items = append(items, u)
				}
				fmt.Fprint(w, r6Page(items, 2101, page, 100))
			}))
			defer srv.Close()
			c := New(srv.URL)
			if search {
				u, err := c.FindUserByEmail("target@qq.com")
				if err != nil || u == nil || u.ID != 2101 {
					t.Fatalf("last match lost: %+v %v", u, err)
				}
			} else {
				us, err := c.ListUsers()
				if err != nil || len(us) != 2101 {
					t.Fatalf("partial list: %d %v", len(us), err)
				}
			}
			if calls != 22 {
				t.Fatalf("calls=%d want 22", calls)
			}
		})
	}
}

func TestR6PaginationRejectsPartial(t *testing.T) {
	cases := []struct {
		name                 string
		first, second, third string
	}{
		{"short", r6Page([]User{{ID: 1}}, 3, 1, 2), r6Page([]User{{ID: 2}, {ID: 3}}, 3, 2, 2), ""},
		{"empty", r6Page([]User{}, 3, 1, 2), r6Page([]User{{ID: 2}, {ID: 3}}, 3, 2, 2), r6Page([]User{{ID: 1}}, 3, 3, 2)},
		{"duplicate-in-page", r6Page([]User{{ID: 1}, {ID: 1}}, 2, 1, 2), "", ""},
		{"duplicate-next-page", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), r6Page([]User{{ID: 2}}, 3, 2, 2), ""},
		{"total-changed", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), r6Page([]User{{ID: 3}}, 4, 2, 2), ""},
		{"size-changed", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), r6Page([]User{{ID: 3}}, 3, 2, 3), ""},
		{"page-repeated", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), r6Page([]User{{ID: 3}}, 3, 1, 2), ""},
		{"over-total", r6Page([]User{{ID: 1}, {ID: 2}}, 1, 1, 2), "", ""},
		{"invalid-id", r6Page([]User{{ID: 0}}, 1, 1, 2), "", ""},
		{"zero-id-between", r6Page([]User{{ID: 1}, {ID: 0}}, 3, 1, 2), r6Page([]User{{ID: 3}}, 3, 2, 2), ""},
		{"late-rejection", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), `{"success":false,"message":"boom"}`, ""},
		// A repeated id keeps len(seen) short, so a naive scan fetches page 3 and
		// returns the row twice; only the duplicate guard stops it.
		{"duplicate-masked-by-extra-page", r6Page([]User{{ID: 1}, {ID: 2}}, 3, 1, 2), r6Page([]User{{ID: 2}}, 3, 2, 2), r6Page([]User{{ID: 3}}, 3, 3, 2)},
	}
	for _, tc := range cases {
		for _, search := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/search=%v", tc.name, search), func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					switch {
					case calls == 1:
						fmt.Fprint(w, tc.first)
					case calls == 2 || tc.third == "":
						fmt.Fprint(w, tc.second)
					default:
						fmt.Fprint(w, tc.third)
					}
				}))
				defer srv.Close()
				c := New(srv.URL)
				if search {
					u, err := c.FindUserByEmail("x@qq.com")
					if err == nil || u != nil {
						t.Fatalf("partial search accepted: %+v %v", u, err)
					}
				} else {
					us, err := c.ListUsers()
					if err == nil || us != nil {
						t.Fatalf("partial list accepted: %+v %v", us, err)
					}
				}
			})
		}
	}
}

func TestR6PaginationUsesAnnouncedPageSize(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		items := []User{{ID: 1}, {ID: 2}}
		if page == 2 {
			items = []User{{ID: 3}}
		}
		fmt.Fprint(w, r6Page(items, 3, page, 2))
	}))
	defer srv.Close()
	users, err := New(srv.URL).ListUsers()
	if err != nil || len(users) != 3 || calls != 2 {
		t.Fatalf("users=%v calls=%d err=%v", users, calls, err)
	}
}
