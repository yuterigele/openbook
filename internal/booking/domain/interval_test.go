package domain

import (
	"testing"
	"time"
)

func TestServiceIntervalDurations(t *testing.T) {
	start := time.Date(2026, 8, 25, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for _, tc := range []struct {
		name     string
		duration time.Duration
		want     time.Duration
	}{
		{name: "30 minutes", duration: 30 * time.Minute, want: 30 * time.Minute},
		{name: "60 minutes", duration: 60 * time.Minute, want: 60 * time.Minute},
		{name: "90 minutes", duration: 90 * time.Minute, want: 90 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			interval, err := NewServiceInterval(start, tc.duration, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := interval.Duration(); got != tc.want {
				t.Fatalf("duration = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIntervalConflictTable(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	base := time.Date(2026, 8, 25, 10, 0, 0, 0, zone)
	makeInterval := func(offset, duration time.Duration) Interval {
		interval, err := NewInterval(base.Add(offset), base.Add(offset+duration))
		if err != nil {
			t.Fatalf("make interval: %v", err)
		}
		return interval
	}

	cases := []struct {
		name      string
		candidate Interval
		occupied  []Interval
		want      bool
	}{
		{name: "before", candidate: makeInterval(-60*time.Minute, 30*time.Minute), occupied: []Interval{makeInterval(0, 30*time.Minute)}, want: false},
		{name: "adjacent", candidate: makeInterval(30*time.Minute, 30*time.Minute), occupied: []Interval{makeInterval(0, 30*time.Minute)}, want: false},
		{name: "same start", candidate: makeInterval(0, 30*time.Minute), occupied: []Interval{makeInterval(0, 30*time.Minute)}, want: true},
		{name: "inside", candidate: makeInterval(5*time.Minute, 10*time.Minute), occupied: []Interval{makeInterval(0, 30*time.Minute)}, want: true},
		{name: "after", candidate: makeInterval(31*time.Minute, 30*time.Minute), occupied: []Interval{makeInterval(0, 30*time.Minute)}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasConflict(tc.candidate, tc.occupied); got != tc.want {
				t.Fatalf("HasConflict = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIntervalBufferAndValidation(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	start := time.Date(2026, 8, 25, 10, 0, 0, 0, zone)
	base, err := NewServiceInterval(start, 30*time.Minute, 10*time.Minute, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := base.StartAt, start.Add(-10*time.Minute); !got.Equal(want) {
		t.Fatalf("buffered start = %s, want %s", got, want)
	}
	if got, want := base.EndAt, start.Add(45*time.Minute); !got.Equal(want) {
		t.Fatalf("buffered end = %s, want %s", got, want)
	}
	if HasConflict(base, []Interval{{StartAt: start.Add(45 * time.Minute), EndAt: start.Add(60 * time.Minute)}}) {
		t.Fatal("an interval starting at buffered end must be adjacent, not conflicting")
	}
	if _, err := base.Buffered(-time.Minute, 0); err != ErrInvalidBuffer {
		t.Fatalf("negative buffer error = %v, want %v", err, ErrInvalidBuffer)
	}
	if _, err := NewInterval(start, start); err != ErrInvalidInterval {
		t.Fatalf("zero-length interval error = %v, want %v", err, ErrInvalidInterval)
	}
}
