package main

import (
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	d := func(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
	sorted := []time.Duration{d(1), d(2), d(3), d(4), d(5), d(6), d(7), d(8), d(9), d(10)}

	cases := []struct {
		p    float64
		want time.Duration
	}{
		{0, d(1)},
		{50, d(6)},
		{99, d(10)},
		{100, d(10)},
	}
	for _, tc := range cases {
		if got := percentile(sorted, tc.p); got != tc.want {
			t.Errorf("percentile(p=%v) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestPercentile_Empty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("expected 0 for empty slice, got %v", got)
	}
}

func TestSortDurations(t *testing.T) {
	d := []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond}
	sortDurations(d)
	want := []time.Duration{1 * time.Millisecond, 3 * time.Millisecond, 5 * time.Millisecond}
	for i := range want {
		if d[i] != want[i] {
			t.Fatalf("sortDurations() = %v, want %v", d, want)
		}
	}
}
