package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	defaultLocalCredentialQueueTimeout = 30 * time.Second
	maxLocalCredentialInFlight         = 10_000
	maxLocalCredentialQueueCapacity    = 100_000
	maxLocalCredentialQueueTimeout     = time.Hour
)

var errLocalCredentialQueueFull = errors.New("local credential admission queue is full")

type localCredentialAdmissionSettings struct {
	maxInFlight   int
	queueCapacity int
	queueTimeout  time.Duration
}

type localCredentialAdmission struct {
	mu sync.Mutex

	settings localCredentialAdmissionSettings
	inFlight int
	waiters  int
	notify   chan struct{}
}

// localCredentialAdmissionWaitState preserves one admission deadline across
// scheduler wakeups and retry rounds for one public execution call.
type localCredentialAdmissionWaitState struct {
	deadline time.Time
}

func (s *localCredentialAdmissionWaitState) remaining(now time.Time, timeout time.Duration) time.Duration {
	if s == nil {
		return timeout
	}
	if s.deadline.IsZero() {
		s.deadline = now.Add(timeout)
	}
	return s.deadline.Sub(now)
}

func localCredentialAdmissionWaitStateForCall(states []*localCredentialAdmissionWaitState) *localCredentialAdmissionWaitState {
	if len(states) > 0 && states[0] != nil {
		return states[0]
	}
	return &localCredentialAdmissionWaitState{}
}

type localCredentialBusyError struct {
	retryAfter time.Duration
}

func (e *localCredentialBusyError) Error() string {
	return "local Claude credential concurrency limit exceeded"
}

func (e *localCredentialBusyError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *localCredentialBusyError) IsRequestScoped() bool {
	return true
}

func (e *localCredentialBusyError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	retryAfter := e.retryAfter
	return &retryAfter
}

func (e *localCredentialBusyError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return safeRetryAfterHeader(e.retryAfter)
}

func newLocalCredentialBusyError(retryAfter time.Duration) error {
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	return &localCredentialBusyError{retryAfter: retryAfter}
}

func wrapLocalCredentialAdmissionStream(ctx context.Context, result *cliproxyexecutor.StreamResult, release func()) *cliproxyexecutor.StreamResult {
	if release == nil {
		return result
	}
	if result == nil || result.Chunks == nil {
		release()
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer release()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-result.Chunks:
				if !ok {
					return
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

func (l *localCredentialAdmission) configure(settings localCredentialAdmissionSettings) {
	l.mu.Lock()
	if l.notify == nil {
		l.notify = make(chan struct{})
	}
	if l.settings != settings {
		l.settings = settings
		l.signalLocked()
	}
	l.mu.Unlock()
}

func (l *localCredentialAdmission) tryAcquire() (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	l.mu.Lock()
	if l.settings.maxInFlight <= 0 {
		l.mu.Unlock()
		return func() {}, true
	}
	if l.inFlight >= l.settings.maxInFlight {
		l.mu.Unlock()
		return nil, false
	}
	l.inFlight++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.inFlight > 0 {
				l.inFlight--
			}
			l.signalLocked()
			l.mu.Unlock()
		})
	}, true
}

func (l *localCredentialAdmission) registerWaiter() (<-chan struct{}, time.Duration, func(), error) {
	if l == nil {
		return nil, 0, nil, errLocalCredentialQueueFull
	}
	l.mu.Lock()
	if l.notify == nil {
		l.notify = make(chan struct{})
	}
	if l.settings.maxInFlight <= 0 || l.inFlight < l.settings.maxInFlight {
		l.mu.Unlock()
		return nil, 0, func() {}, nil
	}
	if l.settings.queueCapacity <= 0 || l.waiters >= l.settings.queueCapacity {
		l.mu.Unlock()
		return nil, 0, nil, errLocalCredentialQueueFull
	}
	l.waiters++
	notify := l.notify
	timeout := l.settings.queueTimeout
	l.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			l.mu.Lock()
			if l.waiters > 0 {
				l.waiters--
			}
			l.mu.Unlock()
		})
	}
	return notify, timeout, cancel, nil
}

func (l *localCredentialAdmission) availabilitySignal() (<-chan struct{}, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.notify == nil {
		l.notify = make(chan struct{})
	}
	if l.settings.maxInFlight <= 0 || l.inFlight < l.settings.maxInFlight {
		return nil, true
	}
	return l.notify, false
}

func (l *localCredentialAdmission) signalLocked() {
	if l.notify != nil {
		close(l.notify)
	}
	l.notify = make(chan struct{})
}

func (m *Manager) tryAcquireLocalCredential(auth *Auth) (func(), *localCredentialAdmission, bool) {
	settings, enabled := m.localCredentialAdmissionSettings(auth)
	if !enabled {
		return func() {}, nil, true
	}
	key := strings.TrimSpace(auth.ID)
	if key == "" {
		key = strings.TrimSpace(auth.EnsureIndex())
	}
	if key == "" {
		return func() {}, nil, true
	}
	value, _ := m.localCredentialAdmissions.LoadOrStore(key, &localCredentialAdmission{})
	limiter, ok := value.(*localCredentialAdmission)
	if !ok || limiter == nil {
		return func() {}, nil, true
	}
	limiter.configure(settings)
	release, acquired := limiter.tryAcquire()
	return release, limiter, acquired
}

func recordBusyLocalCredential(busy map[string]*localCredentialAdmission, order *[]string, authID string, limiter *localCredentialAdmission) {
	if busy == nil || limiter == nil {
		return
	}
	if _, exists := busy[authID]; !exists && order != nil {
		*order = append(*order, authID)
	}
	busy[authID] = limiter
}

func orderedBusyLocalCredentialKeys(busy map[string]*localCredentialAdmission, preferred ...[]string) []string {
	keys := make([]string, 0, len(busy))
	seen := make(map[string]struct{}, len(busy))
	if len(preferred) > 0 {
		for _, key := range preferred[0] {
			if _, exists := busy[key]; !exists {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	remaining := make([]string, 0, len(busy)-len(keys))
	for key := range busy {
		if _, exists := seen[key]; !exists {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

func (m *Manager) waitForLocalCredentialCapacity(ctx context.Context, busy map[string]*localCredentialAdmission, waitState *localCredentialAdmissionWaitState, preferredKeys ...[]string) error {
	if len(busy) == 0 {
		return newLocalCredentialBusyError(time.Second)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	keys := orderedBusyLocalCredentialKeys(busy, preferredKeys...)

	var (
		timeout      time.Duration
		cancelWaiter func()
	)
	for _, key := range keys {
		limiter := busy[key]
		notify, registeredTimeout, cancel, errRegister := limiter.registerWaiter()
		if errors.Is(errRegister, errLocalCredentialQueueFull) {
			continue
		}
		if errRegister != nil {
			return errRegister
		}
		if cancel == nil {
			cancel = func() {}
		}
		if notify == nil {
			cancel()
			return nil
		}
		timeout = registeredTimeout
		cancelWaiter = cancel
		break
	}
	if cancelWaiter == nil {
		return newLocalCredentialBusyError(time.Second)
	}
	defer cancelWaiter()
	if timeout <= 0 {
		timeout = defaultLocalCredentialQueueTimeout
	}
	signalCases := make([]reflect.SelectCase, 0, len(keys))
	for _, key := range keys {
		signal, available := busy[key].availabilitySignal()
		if available {
			return nil
		}
		if signal == nil {
			continue
		}
		// The registered credential's signal is included through the same path;
		// every request consumes only one queue slot while any eligible release
		// can wake the scheduler for a fresh selection pass.
		signalCases = append(signalCases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(signal)})
	}
	if len(signalCases) == 0 {
		return newLocalCredentialBusyError(time.Second)
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	timeout = waitState.remaining(time.Now(), timeout)
	if timeout <= 0 {
		return newLocalCredentialBusyError(time.Second)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	cases := make([]reflect.SelectCase, 0, len(signalCases)+2)
	cases = append(cases,
		reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())},
		reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timer.C)},
	)
	cases = append(cases, signalCases...)

	selected, _, _ := reflect.Select(cases)
	switch selected {
	case 0:
		return ctx.Err()
	case 1:
		for _, key := range keys {
			if _, available := busy[key].availabilitySignal(); available {
				return nil
			}
		}
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		return newLocalCredentialBusyError(time.Second)
	default:
		return nil
	}
}

func (m *Manager) notifyLocalCredentialAdmissionConfigChanged() {
	if m == nil {
		return
	}
	m.localCredentialAdmissions.Range(func(_, value any) bool {
		limiter, ok := value.(*localCredentialAdmission)
		if !ok || limiter == nil {
			return true
		}
		limiter.mu.Lock()
		limiter.signalLocked()
		limiter.mu.Unlock()
		return true
	})
}

func (m *Manager) refreshLocalCredentialAdmission(auth *Auth) {
	if m == nil || auth == nil {
		return
	}
	key := strings.TrimSpace(auth.ID)
	if key == "" {
		return
	}
	value, ok := m.localCredentialAdmissions.Load(key)
	if !ok {
		return
	}
	limiter, ok := value.(*localCredentialAdmission)
	if !ok || limiter == nil {
		m.localCredentialAdmissions.Delete(key)
		return
	}
	settings, enabled := m.localCredentialAdmissionSettings(auth)
	if !enabled {
		limiter.configure(localCredentialAdmissionSettings{})
		m.localCredentialAdmissions.Delete(key)
		return
	}
	limiter.configure(settings)
}

func (m *Manager) removeLocalCredentialAdmission(authID string) {
	if m == nil {
		return
	}
	value, ok := m.localCredentialAdmissions.LoadAndDelete(strings.TrimSpace(authID))
	if !ok {
		return
	}
	limiter, ok := value.(*localCredentialAdmission)
	if ok && limiter != nil {
		limiter.configure(localCredentialAdmissionSettings{})
	}
}

func (m *Manager) localCredentialAdmissionSettings(auth *Auth) (localCredentialAdmissionSettings, bool) {
	if m == nil || auth == nil || m.HomeEnabled() || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return localCredentialAdmissionSettings{}, false
	}
	cfg := m.runtimeConfigSnapshot()
	settings := localCredentialAdmissionSettings{}
	if cfg != nil {
		settings.maxInFlight = cfg.ClaudeCode.MaxInFlight
		settings.queueCapacity = cfg.ClaudeCode.QueueCapacity
		settings.queueTimeout = time.Duration(cfg.ClaudeCode.QueueTimeoutSeconds) * time.Second
	}
	settings.maxInFlight = credentialAdmissionIntOverride(auth, settings.maxInFlight, "max_in_flight", "max-in-flight")
	settings.queueCapacity = credentialAdmissionIntOverride(auth, settings.queueCapacity, "queue_capacity", "queue-capacity")
	queueTimeoutSeconds := int(settings.queueTimeout / time.Second)
	queueTimeoutSeconds = credentialAdmissionIntOverride(auth, queueTimeoutSeconds, "queue_timeout_seconds", "queue-timeout-seconds")
	settings.queueTimeout = time.Duration(queueTimeoutSeconds) * time.Second
	settings = normalizeLocalCredentialAdmissionSettings(settings)
	return settings, settings.maxInFlight > 0
}

func credentialAdmissionIntOverride(auth *Auth, fallback int, keys ...string) int {
	if auth == nil {
		return fallback
	}
	for _, key := range keys {
		if auth.Metadata != nil {
			if value, ok := auth.Metadata[key]; ok {
				if parsed, okParsed := parseIntAny(value); okParsed {
					return parsed
				}
			}
		}
		if auth.Attributes != nil {
			if value, ok := auth.Attributes[key]; ok {
				if parsed, errParse := strconv.Atoi(strings.TrimSpace(value)); errParse == nil {
					return parsed
				}
			}
		}
	}
	return fallback
}

func normalizeLocalCredentialAdmissionSettings(settings localCredentialAdmissionSettings) localCredentialAdmissionSettings {
	settings.maxInFlight = min(max(settings.maxInFlight, 0), maxLocalCredentialInFlight)
	settings.queueCapacity = min(max(settings.queueCapacity, 0), maxLocalCredentialQueueCapacity)
	if settings.queueCapacity == 0 {
		settings.queueTimeout = 0
		return settings
	}
	if settings.queueTimeout <= 0 {
		settings.queueTimeout = defaultLocalCredentialQueueTimeout
	}
	if settings.queueTimeout > maxLocalCredentialQueueTimeout {
		settings.queueTimeout = maxLocalCredentialQueueTimeout
	}
	return settings
}
