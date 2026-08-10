package limiter

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newWithClock builds a Limiter and rewires its internal clock to a
// fake one, re-anchoring the refill/reset timestamps so the swap
// doesn't produce a bogus elapsed-time jump on the first call.
func newWithClock(m Model, now func() time.Time) *Limiter {
	l := New(m)
	l.now = now
	t0 := now()
	l.rpmUpdated = t0
	l.tpmUpdated = t0
	l.rpdResetAt = nextPacificMidnight(t0)
	return l
}

func TestTryAdmit_RPMExhaustsThenRefills(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	l := newWithClock(Model{Name: "m", RPM: 2, TPM: 100000, RPD: 100}, clock.Now)

	for i := 0; i < 2; i++ {
		if d := l.TryAdmit(10); !d.Admitted {
			t.Fatalf("request %d: expected admission, got reason=%q", i, d.Reason)
		}
	}

	d := l.TryAdmit(10)
	if d.Admitted || d.Reason != "rpm" {
		t.Fatalf("expected an rpm rejection, got %+v", d)
	}
	if d.Wait <= 0 {
		t.Errorf("expected a positive wait hint, got %v", d.Wait)
	}

	clock.Advance(d.Wait + time.Second)
	if d = l.TryAdmit(10); !d.Admitted {
		t.Fatalf("expected admission after waiting %v, got reason=%q", d.Wait, d.Reason)
	}
}

func TestTryAdmit_TPMRejectionDoesNotConsumeRPMOrRPD(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	l := newWithClock(Model{Name: "m", RPM: 5, TPM: 100, RPD: 5}, clock.Now)

	before := l.Snapshot()
	d := l.TryAdmit(500) // exceeds TPM capacity entirely
	if d.Admitted || d.Reason != "tpm" {
		t.Fatalf("expected a tpm rejection, got %+v", d)
	}
	after := l.Snapshot()

	if after.RPMAvailable != before.RPMAvailable {
		t.Errorf("RPM should be untouched on a TPM rejection: before=%v after=%v", before.RPMAvailable, after.RPMAvailable)
	}
	if after.RPDUsed != before.RPDUsed {
		t.Errorf("RPD should be untouched on a TPM rejection: before=%d after=%d", before.RPDUsed, after.RPDUsed)
	}
}

func TestTryAdmit_RPDExhaustsAndResetsAtPacificMidnight(t *testing.T) {
	start := time.Date(2026, 8, 10, 23, 0, 0, 0, pacific) // 11pm Pacific
	clock := &fakeClock{t: start}
	l := newWithClock(Model{Name: "m", RPM: 100, TPM: 1000000, RPD: 2}, clock.Now)

	for i := 0; i < 2; i++ {
		if d := l.TryAdmit(1); !d.Admitted {
			t.Fatalf("request %d: expected admission, got %+v", i, d)
		}
	}
	d := l.TryAdmit(1)
	if d.Admitted || d.Reason != "rpd" {
		t.Fatalf("expected an rpd rejection, got %+v", d)
	}

	clock.Advance(2 * time.Hour) // crosses midnight Pacific
	if d = l.TryAdmit(1); !d.Admitted {
		t.Fatalf("expected admission after crossing midnight Pacific, got %+v", d)
	}
}

func TestRelease_RefundsReservation(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	l := newWithClock(Model{Name: "m", RPM: 1, TPM: 1000, RPD: 1}, clock.Now)

	if d := l.TryAdmit(100); !d.Admitted {
		t.Fatalf("expected admission, got %+v", d)
	}
	if d := l.TryAdmit(1); d.Admitted {
		t.Fatal("expected rejection before release — capacity should be exhausted")
	}

	l.Release(100)

	if d := l.TryAdmit(1); !d.Admitted {
		t.Fatalf("expected admission after release, got %+v", d)
	}
}

func TestReportActualUsage_CorrectsTPMBucket(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	l := newWithClock(Model{Name: "m", RPM: 100, TPM: 1000, RPD: 100}, clock.Now)

	if d := l.TryAdmit(50); !d.Admitted { // estimate: 50 tokens
		t.Fatalf("expected admission, got %+v", d)
	}
	before := l.Snapshot().TPMAvailable // 950

	l.ReportActualUsage(50, 200) // actual usage came in much higher than the estimate
	after := l.Snapshot().TPMAvailable

	wantDrop := float64(200 - 50)
	if got := before - after; got != wantDrop {
		t.Errorf("TPM dropped by %v, want %v (before=%v after=%v)", got, wantDrop, before, after)
	}
}

func TestTryAdmit_ConcurrentNeverExceedsCapacity(t *testing.T) {
	l := New(Model{Name: "m", RPM: 10, TPM: 1_000_000, RPD: 1000})

	var wg sync.WaitGroup
	var admitted int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d := l.TryAdmit(1); d.Admitted {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()

	if admitted > 10 {
		t.Errorf("admitted %d requests concurrently, capacity was 10 — the bucket allowed over-admission under a race", admitted)
	}
}