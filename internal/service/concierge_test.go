package service

import "testing"

func TestParseBudget(t *testing.T) {
	cases := map[string]int64{"di bawah 300 ribu": 300000, "budget 1,5 juta": 1500000, "maksimal 250rb": 250000, "kurang dari 500k": 500000, "murah": 0, "di bawah 300": 300000, "tiket jazz": 0}
	for in, want := range cases {
		if got := parseBudget(in); got != want {
			t.Errorf("%q → %d, mau %d", in, got, want)
		}
	}
}
