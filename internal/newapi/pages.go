package newapi

import (
	"errors"
	"fmt"
	"net/http"
)

// PageInfo mirrors reference/new-api/common/page_info.go. Presence matters:
// absent metadata must never turn a failed directory read into an empty cache.
type userPage struct {
	Items    []User `json:"items"`
	Total    *int   `json:"total"`
	Page     *int   `json:"page"`
	PageSize *int   `json:"page_size"`
}

func (p userPage) validate(page int) error {
	if p.Items == nil || p.Total == nil || p.Page == nil || p.PageSize == nil || *p.Page != page || *p.PageSize <= 0 || *p.PageSize > searchPageSize {
		return errors.New("invalid user PageInfo")
	}
	return nil
}

// readUsers fails closed: a cache replacement or uniqueness decision is safe
// only after every row in a stable PageInfo snapshot has been read.
func (c *Client) readUsers(prefix string) ([]User, error) {
	var out []User
	seen := make(map[int]struct{})
	total, size := -1, searchPageSize
	for page := 1; ; page++ {
		var resp userPage
		path := fmt.Sprintf("%sp=%d&page_size=%d", prefix, page, size)
		if err := c.do(http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		if err := resp.validate(page); err != nil {
			return nil, err
		}
		if total == -1 {
			total, size = *resp.Total, *resp.PageSize
		}
		if *resp.Total != total || *resp.PageSize != size {
			return nil, errors.New("user pagination metadata changed")
		}
		remaining := total - len(seen)
		expected := min(size, remaining)
		if len(resp.Items) != expected {
			return nil, errors.New("incomplete or inconsistent user page")
		}
		for _, u := range resp.Items {
			if u.ID <= 0 {
				return nil, errors.New("invalid user id in page")
			}
			if _, duplicate := seen[u.ID]; duplicate {
				return nil, errors.New("duplicate user id in pages")
			}
			seen[u.ID] = struct{}{}
			if u.DeletedAt == nil {
				out = append(out, u)
			}
		}
		if len(seen) == total {
			return out, nil
		}
	}
}
