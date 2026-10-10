package napcat

import (
	"encoding/json"
	"strconv"
)

// flexInt accepts a JSON number or a numeric string. NapCat is inconsistent
// here: `level` arrives as "0" (string) while `qq_level` arrives as 0 (number),
// and a strict type would fail to decode the whole member list.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" || s == `""` {
		*f = 0
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		if str == "" {
			*f = 0
			return nil
		}
		n, err := strconv.Atoi(str)
		if err != nil {
			// A non-numeric level is treated as absent rather than fatal.
			*f = 0
			return nil
		}
		*f = flexInt(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}
