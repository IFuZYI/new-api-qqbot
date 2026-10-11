package napcat

import (
	"encoding/json"
	"testing"
)

// NapCat's sign-in board reports rank as (signInRank-1)/2+1, which is fractional
// for every even signInRank. A plain int field failed the whole array's decode,
// so a board with two or more signers recorded nothing at all.
func TestSignedBoardToleratesFractionalRank(t *testing.T) {
	raw := []byte(`{"status":"ok","retcode":0,"message":"","data":[
		{"user_id":1,"nick":"a","time":10,"rank":1},
		{"user_id":2,"nick":"b","time":20,"rank":1.5},
		{"user_id":3,"nick":"c","time":30,"rank":2}
	]}`)

	var out []SignedMember
	if err := decodeAction("get_group_signed_list", raw, &out); err != nil {
		t.Fatalf("a fractional rank must not fail the whole board: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 members, got %d", len(out))
	}
	if out[1].Rank != 1.5 {
		t.Errorf("fractional rank not preserved: %v", out[1].Rank)
	}
}

// A string rank is also tolerated, in case NapCat changes the encoding.
func TestSignedBoardToleratesStringRank(t *testing.T) {
	raw := []byte(`{"status":"ok","retcode":0,"data":[{"user_id":1,"rank":"2.5"}]}`)
	var out []SignedMember
	if err := decodeAction("get_group_signed_list", raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out[0].Rank != 2.5 {
		t.Errorf("rank = %v, want 2.5", out[0].Rank)
	}
}

// A garbage rank must not lose the member either.
func TestSignedBoardToleratesGarbageRank(t *testing.T) {
	raw := []byte(`{"status":"ok","retcode":0,"data":[{"user_id":1,"rank":"nonsense"}]}`)
	var out []SignedMember
	if err := decodeAction("get_group_signed_list", raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].QQ != 1 || out[0].Rank != 0 {
		t.Errorf("unexpected: %+v", out)
	}
}

// flexFloat's own contract.
func TestFlexFloat(t *testing.T) {
	cases := []struct {
		in   string
		want flexFloat
	}{
		{`1`, 1}, {`1.5`, 1.5}, {`"2.5"`, 2.5}, {`""`, 0}, {`null`, 0},
		{`"x"`, 0}, {`0`, 0}, {`-1.25`, -1.25},
	}
	for _, c := range cases {
		var f flexFloat
		if err := json.Unmarshal([]byte(c.in), &f); err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if f != c.want {
			t.Errorf("%s = %v, want %v", c.in, f, c.want)
		}
	}
}
