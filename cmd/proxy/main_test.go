package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// Helper function to simulate state transition after a delay
func simulateStateTransition(p *Proxy, targetState State, delay time.Duration) {
	go func() {
		time.Sleep(delay)
		p.mu.Lock()
		p.state = targetState
		p.mu.Unlock()
	}()
}

// Helper function to create a test proxy with backend
func createTestProxy(
	wakeTimeout time.Duration,
	initialState State,
	backendHandler http.HandlerFunc,
) (*Proxy, *httptest.Server) {
	backend := httptest.NewServer(backendHandler)
	backendURL, _ := url.Parse(backend.URL)

	p := &Proxy{
		config: Config{
			WakeTimeout: wakeTimeout,
		},
		reverseProxy: httputil.NewSingleHostReverseProxy(backendURL),
		state:        initialState,
	}

	return p, backend
}

func TestIsAPIRequest(t *testing.T) {
	p := &Proxy{}

	tests := []struct {
		name     string
		accept   string
		expected bool
	}{
		{
			name:     "browser request with text/html",
			accept:   "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			expected: false,
		},
		{
			name:     "api request with application/json",
			accept:   "application/json",
			expected: true,
		},
		{
			name:     "api request with application/xml",
			accept:   "application/xml",
			expected: true,
		},
		{
			name:     "api request with wildcard",
			accept:   "*/*",
			expected: true,
		},
		{
			name:     "empty accept header",
			accept:   "",
			expected: false,
		},
		{
			name:     "mixed with html first",
			accept:   "text/html, application/json",
			expected: false,
		},
		{
			name:     "mixed with json first",
			accept:   "application/json, text/html",
			expected: false,
		},
		{
			name:     "plain text",
			accept:   "text/plain",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			result := p.isAPIRequest(req)
			if result != tt.expected {
				t.Errorf("isAPIRequest() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestWaitAndProxy_Success(t *testing.T) {
	backendCalled := atomic.Bool{}
	p, backend := createTestProxy(
		5*time.Second,
		StateWaking,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendCalled.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("backend response"))
		}),
	)
	defer backend.Close()

	// Simulate transition to Awake state after 100ms
	simulateStateTransition(p, StateAwake, 100*time.Millisecond)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	// Call waitAndProxy
	p.waitAndProxy(rec, req)

	// Check that backend was called
	if !backendCalled.Load() {
		t.Error("Backend was not called")
	}

	// Check response
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body != "backend response" {
		t.Errorf("Expected 'backend response', got '%s'", body)
	}
}

func TestWaitAndProxy_Timeout(t *testing.T) {
	// Create a mock backend server (should not be called)
	backendCalled := atomic.Bool{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)

	p := &Proxy{
		config: Config{
			WakeTimeout: 200 * time.Millisecond, // Short timeout for test
		},
		reverseProxy: httputil.NewSingleHostReverseProxy(backendURL),
		state:        StateWaking,
	}

	// Don't transition to Awake - let it timeout

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	// Call waitAndProxy
	p.waitAndProxy(rec, req)

	// Check that backend was NOT called
	if backendCalled.Load() {
		t.Error("Backend should not have been called on timeout")
	}

	// Check response is 504 Gateway Timeout
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("Expected status 504, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body != "Service wake-up timeout\n" {
		t.Errorf("Expected timeout message, got '%s'", body)
	}
}

func TestHandleRequest_AwakeState_ProxiesImmediately(t *testing.T) {
	// Create a mock backend server
	backendCalled := atomic.Bool{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)

	p := &Proxy{
		config: Config{
			WakeTimeout: 5 * time.Second,
		},
		reverseProxy: httputil.NewSingleHostReverseProxy(backendURL),
		state:        StateAwake,
		lastActivity: time.Now(),
	}

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	// Call handleRequest
	p.handleRequest(rec, req)

	// Check that backend was called
	if !backendCalled.Load() {
		t.Error("Backend was not called when awake")
	}

	// Check that lastActivity was updated
	p.mu.RLock()
	timeSinceActivity := time.Since(p.lastActivity)
	p.mu.RUnlock()

	if timeSinceActivity > 100*time.Millisecond {
		t.Error("lastActivity was not updated")
	}
}

// TestHandleRequest_SleepingState_APIRequest is skipped because it requires k8s client
// The sleeping state triggers a wake goroutine that needs k8s interaction
// This functionality is better tested in integration tests

// TestHandleRequest_SleepingState_BrowserRequest is skipped because it triggers wake goroutine
// The handleRequest for sleeping state triggers triggerWake() which needs k8s client
// This functionality is better tested in integration tests

func TestHandleRequest_WakingState_APIRequest(t *testing.T) {
	backendCalled := atomic.Bool{}
	p, backend := createTestProxy(
		1*time.Second,
		StateWaking,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		}),
	)
	defer backend.Close()

	// Simulate wake-up completion
	simulateStateTransition(p, StateAwake, 100*time.Millisecond)

	// Create API request
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	// Call handleRequest
	p.handleRequest(rec, req)

	// Check that backend was called after wake-up
	if !backendCalled.Load() {
		t.Error("Backend was not called after wake-up")
	}
}

func TestHandleRequest_SkipsWakeEndpoints(t *testing.T) {
	p := &Proxy{
		state: StateSleeping,
	}

	wakeEndpoints := []string{
		"/_wake/status",
		"/_wake/events",
		"/_wake/trigger",
		"/_wake/hibernate",
		"/_wake/health",
	}

	for _, endpoint := range wakeEndpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			rec := httptest.NewRecorder()

			// handleRequest should return early for _wake endpoints
			// We can't easily verify it returns early, but we can verify
			// it doesn't panic or cause issues
			p.handleRequest(rec, req)

			// Status should still be 200 (default from recorder)
			// The actual endpoint handlers are tested separately
		})
	}
}

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		name           string
		initialState   State
		finalState     State
		transitionTime time.Duration
	}{
		{
			name:           "sleeping to waking",
			initialState:   StateSleeping,
			finalState:     StateWaking,
			transitionTime: 10 * time.Millisecond,
		},
		{
			name:           "waking to awake",
			initialState:   StateWaking,
			finalState:     StateAwake,
			transitionTime: 10 * time.Millisecond,
		},
		{
			name:           "awake to sleeping",
			initialState:   StateAwake,
			finalState:     StateSleeping,
			transitionTime: 10 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Proxy{
				state: tt.initialState,
			}

			// Simulate state transition
			simulateStateTransition(p, tt.finalState, tt.transitionTime)

			// Wait for transition
			time.Sleep(tt.transitionTime + 50*time.Millisecond)

			// Verify state changed
			p.mu.RLock()
			currentState := p.state
			p.mu.RUnlock()

			if currentState != tt.finalState {
				t.Errorf("Expected state %v, got %v", tt.finalState, currentState)
			}
		})
	}
}

func TestWaitAndProxy_ConcurrentRequests(t *testing.T) {
	backendCallCount := atomic.Int32{}
	p, backend := createTestProxy(
		2*time.Second,
		StateWaking,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendCallCount.Add(1)
			w.WriteHeader(http.StatusOK)
		}),
	)
	defer backend.Close()

	// Simulate wake-up after delay
	simulateStateTransition(p, StateAwake, 200*time.Millisecond)

	// Send 5 concurrent requests
	numRequests := 5
	done := make(chan bool, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			rec := httptest.NewRecorder()
			p.waitAndProxy(rec, req)

			if rec.Code == http.StatusOK {
				done <- true
			} else {
				done <- false
			}
		}()
	}

	// Wait for all requests to complete
	successCount := 0
	for i := 0; i < numRequests; i++ {
		if <-done {
			successCount++
		}
	}

	// All requests should have succeeded
	if successCount != numRequests {
		t.Errorf("Expected %d successful requests, got %d", numRequests, successCount)
	}

	// Backend should have been called for all requests
	if backendCallCount.Load() != int32(numRequests) {
		t.Errorf("Expected %d backend calls, got %d", numRequests, backendCallCount.Load())
	}
}

func TestWaitAndProxy_LastActivityUpdated(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)

	p := &Proxy{
		config: Config{
			WakeTimeout: 1 * time.Second,
		},
		reverseProxy: httputil.NewSingleHostReverseProxy(backendURL),
		state:        StateWaking,
		lastActivity: time.Now().Add(-10 * time.Minute), // Old activity
	}

	// Transition to awake immediately
	go func() {
		time.Sleep(50 * time.Millisecond)
		p.mu.Lock()
		p.state = StateAwake
		p.mu.Unlock()
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	p.waitAndProxy(rec, req)

	// Check that lastActivity was updated
	p.mu.RLock()
	timeSinceActivity := time.Since(p.lastActivity)
	p.mu.RUnlock()

	if timeSinceActivity > 200*time.Millisecond {
		t.Error("lastActivity was not updated after successful proxy")
	}
}
