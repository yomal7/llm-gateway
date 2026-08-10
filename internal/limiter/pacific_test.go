package limiter

import (
	"testing"
	"time"
)

func TestNextPacificMidnight_Basic(t *testing.T) {
	from := time.Date(2026, 8, 10, 15, 30, 0, 0, time.UTC)
	got := nextPacificMidnight(from)
	want := time.Date(2026, 8, 11, 0, 0, 0, 0, pacific)
	if !got.Equal(want) {
		t.Errorf("nextPacificMidnight(%v) = %v, want %v", from, got, want)
	}
}

func TestNextPacificMidnight_ExactlyAtMidnightRollsToNextDay(t *testing.T) {
	midnight := time.Date(2026, 8, 10, 0, 0, 0, 0, pacific)
	got := nextPacificMidnight(midnight)
	want := time.Date(2026, 8, 11, 0, 0, 0, 0, pacific)
	if !got.Equal(want) {
		t.Errorf("at exact midnight, next boundary = %v, want %v (a boundary instant counts as already reset)", got, want)
	}
}

func TestNextPacificMidnight_JustBeforeMidnight(t *testing.T) {
	almostMidnight := time.Date(2026, 8, 10, 23, 59, 59, 0, pacific)
	got := nextPacificMidnight(almostMidnight)
	want := time.Date(2026, 8, 11, 0, 0, 0, 0, pacific)
	if !got.Equal(want) {
		t.Errorf("nextPacificMidnight(%v) = %v, want %v", almostMidnight, got, want)
	}
}