package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter counts events per key inside a sliding time window. All methods
// are safe to call on a nil *limiter (they do nothing), so tests and tools
// that build an App by hand never need to set one up.
type limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, max: max, window: window}
}

// recent returns the events for key that are still inside the window,
// dropping the rest. Caller holds l.mu.
func (l *limiter) recent(key string, now time.Time) []time.Time {
	kept := make([]time.Time, 0, len(l.hits[key]))
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = kept
	}
	return kept
}

func (l *limiter) blocked(key string) bool {
	if l == nil || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, time.Now())) >= l.max
}

func (l *limiter) record(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Keep memory bounded: keys can be attacker-chosen (any email string).
	if len(l.hits) > 20000 {
		for k := range l.hits {
			l.recent(k, now)
		}
	}
	l.hits[key] = append(l.hits[key], now)
}

func (l *limiter) reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// limits groups the limiters the app uses. Per-IP limits are deliberately
// loose: many people on Nigerian mobile networks share one public IP.
type limits struct {
	loginEmail, loginIP, resetEmail, resetIP *limiter
}

func newLimits() *limits {
	return &limits{
		loginEmail: newLimiter(5, 15*time.Minute),
		loginIP:    newLimiter(50, 15*time.Minute),
		resetEmail: newLimiter(5, time.Hour),
		resetIP:    newLimiter(30, time.Hour),
	}
}

func (a *App) loginBlocked(email, ip string) bool {
	return a.limits != nil && (a.limits.loginEmail.blocked(email) || a.limits.loginIP.blocked(ip))
}

func (a *App) loginFailed(email, ip string) {
	if a.limits != nil {
		a.limits.loginEmail.record(email)
		a.limits.loginIP.record(ip)
	}
}

func (a *App) clearLoginFailures(email string) {
	if a.limits != nil {
		a.limits.loginEmail.reset(email)
	}
}

func (a *App) resetLimited(email, ip string) bool {
	return a.limits != nil && (a.limits.resetEmail.blocked(email) || a.limits.resetIP.blocked(ip))
}

func (a *App) recordReset(email, ip string) {
	if a.limits != nil {
		a.limits.resetEmail.record(email)
		a.limits.resetIP.record(ip)
	}
}

// clientIP is the caller's address as best we can tell. Behind a proxy
// (Render), the LAST X-Forwarded-For entry is the one the platform's own
// proxy appended, so it can't be spoofed by the client the way the first
// entry can. It is still only a best-effort signal, which is why the
// per-email limit is the one that really protects an account.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
