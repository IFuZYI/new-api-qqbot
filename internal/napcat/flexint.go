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

// flexFloat accepts a JSON number or a numeric string and keeps the fraction.
//
// NapCat computes a sign-in rank as (signInRank-1)/2+1 in JavaScript, so an even
// signInRank yields a fractional rank (2 -> 1.5). A plain int field would fail
// the whole array's decode on the first fractional rank, losing the entire
// board, so the rank must tolerate a fraction.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
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
		n, err := strconv.ParseFloat(str, 64)
		if err != nil {
			*f = 0
			return nil
		}
		*f = flexFloat(n)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}
