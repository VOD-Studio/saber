package ai

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen 表示熔断器处于打开状态，请求被拒绝。
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitState 表示熔断器状态。
type CircuitState int

const (
	// CircuitStateClosed 关闭状态，允许请求。
	CircuitStateClosed CircuitState = iota
	// CircuitStateOpen 打开状态，拒绝请求。
	CircuitStateOpen
	// CircuitStateHalfOpen 半开状态，允许探测请求。
	CircuitStateHalfOpen
)

// String 返回状态的可读表示。
func (s CircuitState) String() string {
	switch s {
	case CircuitStateClosed:
		return "closed"
	case CircuitStateOpen:
		return "open"
	case CircuitStateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker 实现三状态熔断器。
//
// 状态转换：
//   - Closed: 正常状态，允许所有请求。失败计数达到阈值后转为 Open。
//   - Open: 拒绝所有请求。经过 resetTimeout 后转为 HalfOpen。
//   - HalfOpen: 允许一个探测请求。成功则转为 Closed，失败则转为 Open。
type CircuitBreaker struct {
	state            CircuitState
	failureCount     int
	failureThreshold int
	resetTimeout     time.Duration
	lastFailureTime  time.Time
	mu               sync.RWMutex
}

// NewCircuitBreaker 创建新的熔断器。
//
// 参数：
//   - failureThreshold: 触发熔断的失败次数阈值（默认 5）
//   - resetTimeout: 熔断后等待恢复的超时时间（默认 30 秒）
func NewCircuitBreaker(failureThreshold int, resetTimeout time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 5
	}
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}

	return &CircuitBreaker{
		state:            CircuitStateClosed,
		failureThreshold: failureThreshold,
		resetTimeout:     resetTimeout,
	}
}

// State 返回当前熔断器状态。
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	// 检查是否应该从 Open 转为 HalfOpen
	if cb.state == CircuitStateOpen {
		if time.Since(cb.lastFailureTime) >= cb.resetTimeout {
			cb.mu.RUnlock()
			cb.mu.Lock()
			// 双重检查，避免竞态
			if cb.state == CircuitStateOpen && time.Since(cb.lastFailureTime) >= cb.resetTimeout {
				cb.state = CircuitStateHalfOpen
			}
			cb.mu.Unlock()
			cb.mu.RLock()
		}
	}

	return cb.state
}

// Allow 检查是否允许请求。
//
// 返回：
//   - true: 允许请求
//   - false: 拒绝请求（熔断器处于 Open 状态）
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// 检查是否应该从 Open 转为 HalfOpen
	if cb.state == CircuitStateOpen {
		if time.Since(cb.lastFailureTime) >= cb.resetTimeout {
			cb.state = CircuitStateHalfOpen
			return true
		}
		return false
	}

	// HalfOpen 状态只允许一个请求
	if cb.state == CircuitStateHalfOpen {
		return false
	}

	// Closed 状态允许所有请求
	return true
}

// RecordSuccess 记录成功请求。
//
// 成功会重置失败计数。在 HalfOpen 状态下，成功会使熔断器转为 Closed。
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0

	if cb.state == CircuitStateHalfOpen {
		cb.state = CircuitStateClosed
	}
}

// RecordFailure 记录失败请求。
//
// 失败会增加失败计数。达到阈值后熔断器打开。
// 在 HalfOpen 状态下，失败会立即使熔断器转为 Open。
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.state == CircuitStateHalfOpen {
		// 探测失败，立即打开
		cb.state = CircuitStateOpen
		return
	}

	if cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitStateOpen
	}
}

// Execute 在熔断器保护下执行操作。
//
// 如果熔断器处于 Open 状态，返回 ErrCircuitOpen。
// 操作成功会调用 RecordSuccess，失败会调用 RecordFailure。
func (cb *CircuitBreaker) Execute(operation func() (any, error)) (any, error) {
	if !cb.Allow() {
		return nil, ErrCircuitOpen
	}

	result, err := operation()
	if err != nil {
		cb.RecordFailure()
		return nil, err
	}

	cb.RecordSuccess()
	return result, nil
}
