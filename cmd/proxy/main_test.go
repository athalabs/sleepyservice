package main

import (
	"net/http"
	"net/http/httptest"
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
