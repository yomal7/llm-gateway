package limiter

import (
	"math"
	"sync"
	"time"
)

type Model struct {
	Name string
	RPM  int
	TPM  int
	RPD  int
}

type Decision struct {
	Admitted bool
	Reason   string
	Wait     time.Duration
}

type Limiter struct {
	mu   sync.Mutex
	name string

	rpmCapacity float64
	rpmTokens   float64
	rpmRate     float64
	rpmUpdated  time.Time

	tpmCapacity float64
	tpmTokens   float64
	tpmRate     float64
	tpmUpdated  time.Time

	rpdLimit   int
	rpdCount   int
	rpdResetAt time.Time

	now func() time.Time
}

func New(m Model) *Limiter {
	l := &Limiter{
		name:        m.Name,
		rpmCapacity: float64(m.RPM),
		rpmTokens:   float64(m.RPM),
		rpmRate:     float64(m.RPM) / 60.0,
		tpmCapacity: float64(m.TPM),
		tpmTokens:   float64(m.TPM),
		tpmRate:     float64(m.TPM) / 60.0,
		rpdLimit:    m.RPD,
		now:         time.Now,
	}
	now := l.now()
	l.rpmUpdated = now
	l.tpmUpdated = now
	l.rpdResetAt = nextPacificMidnight(now)
	return l
}

func (l *Limiter) Name() string { return l.name }

func (l *Limiter) refillRPM(now time.Time) {
	elapsed := now.Sub(l.rpmUpdated).Seconds()
	if elapsed <= 0 {
		return
	}
	l.rpmTokens = math.Min(l.rpmCapacity, l.rpmTokens+elapsed*l.rpmRate)
	l.rpmUpdated = now
}

func (l *Limiter) refillTPM(now time.Time) {
	elapsed := now.Sub(l.tpmUpdated).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tpmTokens = math.Min(l.tpmCapacity, l.tpmTokens+elapsed*l.tpmRate)
	l.tpmUpdated = now
}

func (l *Limiter) maybeResetRPD(now time.Time) {
	if !now.Before(l.rpdResetAt) {
		l.rpdCount = 0
		l.rpdResetAt = nextPacificMidnight(now)
	}
}

func (l *Limiter) TryAdmit(estimatedTokens int) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.maybeResetRPD(now)
	l.refillRPM(now)
	l.refillTPM(now)

	if l.rpdCount >= l.rpdLimit {
		return Decision{Reason: "rpd", Wait: l.rpdResetAt.Sub(now)}
	}
	if l.rpmTokens < 1 {
		return Decision{Reason: "rpm", Wait: durationFromRate(1-l.rpmTokens, l.rpmRate)}
	}
	cost := float64(estimatedTokens)
	if cost > l.tpmTokens {
		return Decision{Reason: "tpm", Wait: durationFromRate(cost-l.tpmTokens, l.tpmRate)}
	}

	l.rpdCount++
	l.rpmTokens -= 1
	l.tpmTokens -= cost
	return Decision{Admitted: true}
}

func (l *Limiter) Release(estimatedTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.maybeResetRPD(now)
	l.refillRPM(now)
	l.refillTPM(now)

	l.rpmTokens = math.Min(l.rpmCapacity, l.rpmTokens+1)
	l.tpmTokens = math.Min(l.tpmCapacity, l.tpmTokens+float64(estimatedTokens))
	if l.rpdCount > 0 {
		l.rpdCount--
	}
}

func (l *Limiter) ReportActualUsage(estimatedTokens, actualTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.refillTPM(now)
	diff := float64(actualTokens - estimatedTokens)
	l.tpmTokens = math.Min(l.tpmCapacity, l.tpmTokens-diff)
}

type Snapshot struct {
	Name         string
	RPMAvailable float64
	RPMCapacity  float64
	TPMAvailable float64
	TPMCapacity  float64
	RPDUsed      int
	RPDLimit     int
	RPDResetAt   time.Time
}

func (l *Limiter) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.maybeResetRPD(now)
	l.refillRPM(now)
	l.refillTPM(now)

	return Snapshot{
		Name:         l.name,
		RPMAvailable: l.rpmTokens,
		RPMCapacity:  l.rpmCapacity,
		TPMAvailable: l.tpmTokens,
		TPMCapacity:  l.tpmCapacity,
		RPDUsed:      l.rpdCount,
		RPDLimit:     l.rpdLimit,
		RPDResetAt:   l.rpdResetAt,
	}
}

func durationFromRate(deficit, ratePerSecond float64) time.Duration {
	if ratePerSecond <= 0 {
		return 0
	}
	return time.Duration(deficit / ratePerSecond * float64(time.Second))
}