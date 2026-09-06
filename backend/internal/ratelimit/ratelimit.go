// Package ratelimit is the in-process limiter built from the contract's
// rate-limit classes (ADR 0016). Every bucketed class has a per-key bucket,
// which is a fair-share partition, and a global bucket, which is the
// guarantee; classes may also cap concurrency where the cost is memory or a
// scarce backend rather than a rate.
package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
)

// Transition is the moment a (class, key) pair first became limited, reported
// at most once per ten minutes so a burst never becomes a burst of writes.
type Transition struct {
	Class     string
	KeyKind   string
	Key       string
	SourceIP  string
	SessionID string
}

// Sink receives transitions. It runs on its own goroutine and must not block
// the request path.
type Sink func(ctx context.Context, t Transition)

// Limiter holds every bucket and concurrency count.
type Limiter struct {
	classes map[string]contract.Class
	sink    Sink
	now     func() time.Time

	mu       sync.Mutex
	global   map[string]*rate.Limiter
	perKey   map[string]*keyState
	inFlight map[string]int
	notified map[string]time.Time
	calls    int
}

type keyState struct {
	limiter  *rate.Limiter
	inFlight int
	lastSeen time.Time
}

const (
	transitionInterval = 10 * time.Minute
	idleEviction       = time.Hour
	sweepEvery         = 1000
	slotPoll           = 25 * time.Millisecond
)

// New builds a limiter for the given classes.
func New(classes map[string]contract.Class, sink Sink) *Limiter {
	l := &Limiter{
		classes:  classes,
		sink:     sink,
		now:      time.Now,
		global:   map[string]*rate.Limiter{},
		perKey:   map[string]*keyState{},
		inFlight: map[string]int{},
		notified: map[string]time.Time{},
	}
	for name, class := range classes {
		if class.HasBucket() {
			l.global[name] = rate.NewLimiter(refill(class.GlobalRefillSeconds), class.GlobalBurst)
		}
	}
	return l
}

// Class looks up a class by name.
func (l *Limiter) Class(name string) (contract.Class, bool) {
	c, ok := l.classes[name]
	return c, ok
}

// Check consumes one token for the request. It returns whether the request is
// admitted and, when it is not, how long until a token frees.
func (l *Limiter) Check(ctx context.Context, class contract.Class, kind, key, ip, session string) (retryAfter time.Duration, admitted bool) {
	if !class.HasBucket() {
		return 0, true
	}
	now := l.now()
	l.mu.Lock()
	state := l.stateLocked(class, key, now)
	perKey := state.limiter.ReserveN(now, 1)
	if !perKey.OK() {
		l.mu.Unlock()
		return time.Hour, false
	}
	if delay := perKey.DelayFrom(now); delay > 0 {
		perKey.CancelAt(now)
		l.mu.Unlock()
		l.transition(ctx, class, kind, key, ip, session, now)
		return delay, false
	}
	global := l.global[class.Name].ReserveN(now, 1)
	if !global.OK() {
		perKey.CancelAt(now)
		l.mu.Unlock()
		return time.Hour, false
	}
	if delay := global.DelayFrom(now); delay > 0 {
		global.CancelAt(now)
		perKey.CancelAt(now)
		l.mu.Unlock()
		l.transition(ctx, class, kind, key, ip, session, now)
		return delay, false
	}
	l.mu.Unlock()
	return 0, true
}

// Acquire takes a concurrency slot for the request, waiting up to wait for one
// to free. The release function must be called when the request finishes.
func (l *Limiter) Acquire(ctx context.Context, class contract.Class, kind, key, ip, session string, wait time.Duration) (release func(), admitted bool) {
	if class.Concurrency == 0 && class.GlobalConcurrency == 0 {
		return func() {}, true
	}
	deadline := l.now().Add(wait)
	for {
		now := l.now()
		l.mu.Lock()
		state := l.stateLocked(class, key, now)
		globalFree := class.GlobalConcurrency == 0 || l.inFlight[class.Name] < class.GlobalConcurrency
		keyFree := class.Concurrency == 0 || state.inFlight < class.Concurrency
		if globalFree && keyFree {
			l.inFlight[class.Name]++
			state.inFlight++
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					l.inFlight[class.Name]--
					if s, ok := l.perKey[bucketKey(class.Name, key)]; ok {
						s.inFlight--
					}
					l.mu.Unlock()
				})
			}, true
		}
		l.mu.Unlock()
		if now.After(deadline) || ctx.Err() != nil {
			l.transition(ctx, class, kind, key, ip, session, now)
			return func() {}, false
		}
		select {
		case <-ctx.Done():
			return func() {}, false
		case <-time.After(slotPoll):
		}
	}
}

// RetryAfterSeconds rounds a delay up to the whole seconds Retry-After needs.
func RetryAfterSeconds(d time.Duration) int {
	seconds := int(math.Ceil(d.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (l *Limiter) stateLocked(class contract.Class, key string, now time.Time) *keyState {
	l.calls++
	if l.calls%sweepEvery == 0 {
		for k, s := range l.perKey {
			if s.inFlight == 0 && now.Sub(s.lastSeen) > idleEviction {
				delete(l.perKey, k)
			}
		}
		for k, t := range l.notified {
			if now.Sub(t) > idleEviction {
				delete(l.notified, k)
			}
		}
	}
	k := bucketKey(class.Name, key)
	state, ok := l.perKey[k]
	if !ok {
		state = &keyState{}
		if class.HasBucket() {
			state.limiter = rate.NewLimiter(refill(class.RefillSeconds), class.Burst)
		} else {
			state.limiter = rate.NewLimiter(rate.Inf, 1)
		}
		l.perKey[k] = state
	}
	state.lastSeen = now
	return state
}

func (l *Limiter) transition(ctx context.Context, class contract.Class, kind, key, ip, session string, now time.Time) {
	if l.sink == nil {
		return
	}
	k := bucketKey(class.Name, key)
	l.mu.Lock()
	last, seen := l.notified[k]
	if seen && now.Sub(last) < transitionInterval {
		l.mu.Unlock()
		return
	}
	l.notified[k] = now
	l.mu.Unlock()
	t := Transition{Class: class.Name, KeyKind: kind, Key: key, SourceIP: ip, SessionID: session}
	go l.sink(context.WithoutCancel(ctx), t)
}

func bucketKey(class, key string) string {
	return class + "\x00" + key
}

func refill(seconds float64) rate.Limit {
	if seconds <= 0 {
		return rate.Inf
	}
	return rate.Every(time.Duration(seconds * float64(time.Second)))
}
