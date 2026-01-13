package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/athalabs/sleepyservice/internal/proxy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Config holds the application configuration
type Config struct {
	Namespace         string
	SleepyServiceName string // Name of the SleepyService resource
	DeploymentName    string
	CNPGClusterName   string // Optional - if empty, no CNPG handling
	BackendURL        string
	HealthPath        string
	ListenAddr        string
	DesiredReplicas   int32
	WakeTimeout       time.Duration
	IdleTimeout       time.Duration // 0 = disabled
}

// State represents the current state of the backend
type State int

const (
	StateSleeping State = iota
	StateWaking
	StateAwake
)

func (s State) String() string {
	switch s {
	case StateSleeping:
		return "sleeping"
	case StateWaking:
		return "waking"
	case StateAwake:
		return "awake"
	default:
		return "unknown"
	}
}

//nolint:unparam
func (s State) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// WakeStatus holds detailed status information
type WakeStatus struct {
	State          State     `json:"state"`
	Message        string    `json:"message"`
	Progress       int       `json:"progress"` // 0-100
	StartedAt      time.Time `json:"started_at,omitempty"`
	EstimatedReady time.Time `json:"estimated_ready,omitempty"`
}

// Proxy is the main application struct
type Proxy struct {
	config       Config
	k8s          *proxy.K8sClient
	reverseProxy *httputil.ReverseProxy
	templates    *template.Template

	mu           sync.RWMutex
	state        State
	wakeStatus   WakeStatus
	lastActivity time.Time
	wakeCancel   context.CancelFunc

	// SSE subscribers
	sseClients map[chan WakeStatus]struct{}
	sseMu      sync.RWMutex
}

func main() {
	config := loadConfig()

	// Parse templates from embedded filesystem
	tmpl, err := template.ParseFS(proxy.TemplateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	// Create Kubernetes client
	k8s, err := proxy.NewK8sClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create reverse proxy
	backendURL, err := url.Parse(config.BackendURL)
	if err != nil {
		log.Fatalf("Invalid backend URL: %v", err)
	}

	p := &Proxy{
		config:       config,
		k8s:          k8s,
		reverseProxy: httputil.NewSingleHostReverseProxy(backendURL),
		templates:    tmpl,
		state:        StateSleeping,
		lastActivity: time.Now(),
		sseClients:   make(map[chan WakeStatus]struct{}),
	}

	// Configure reverse proxy error handler
	p.reverseProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error: %v", err)
		// If backend fails, we might have gone to sleep
		p.checkAndUpdateState()
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
	}

	// Check initial state
	p.checkAndUpdateState()

	// Start idle monitor if enabled
	if config.IdleTimeout > 0 {
		go p.idleMonitor()
	}

	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handleRequest)
	mux.HandleFunc("/_wake/status", p.handleStatus)
	mux.HandleFunc("/_wake/events", p.handleSSE)
	mux.HandleFunc("/_wake/trigger", p.handleTriggerWake)
	mux.HandleFunc("/_wake/hibernate", p.handleHibernate)
	mux.HandleFunc("/_wake/health", p.handleHealth)

	server := &http.Server{
		Addr:         config.ListenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	log.Printf("Wake proxy starting on %s", config.ListenAddr)
	log.Printf("Backend: %s", config.BackendURL)
	log.Printf("Deployment: %s/%s", config.Namespace, config.DeploymentName)
	if config.CNPGClusterName != "" {
		log.Printf("CNPG Cluster: %s/%s", config.Namespace, config.CNPGClusterName)
	}
	if config.IdleTimeout > 0 {
		log.Printf("Idle timeout: %s", config.IdleTimeout)
	}

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func loadConfig() Config {
	config := Config{
		Namespace:         getEnv("NAMESPACE", "default"),
		SleepyServiceName: getEnv("HIBERNATING_SERVICE_NAME", ""),
		DeploymentName:    getEnv("DEPLOYMENT_NAME", ""),
		CNPGClusterName:   getEnv("CNPG_CLUSTER_NAME", ""),
		BackendURL:        getEnv("BACKEND_URL", "http://localhost:8055"),
		HealthPath:        getEnv("HEALTH_PATH", "/server/health"),
		ListenAddr:        getEnv("LISTEN_ADDR", ":8080"),
		DesiredReplicas:   int32(getEnvInt("DESIRED_REPLICAS", 1)),
		WakeTimeout:       getEnvDuration("WAKE_TIMEOUT", 5*time.Minute),
		IdleTimeout:       getEnvDuration("IDLE_TIMEOUT", 0),
	}

	if config.SleepyServiceName == "" {
		log.Fatal("HIBERNATING_SERVICE_NAME environment variable is required")
	}

	return config
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

// handleRequest is the main request handler
func (p *Proxy) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Skip wake endpoints
	if len(r.URL.Path) >= 6 && r.URL.Path[:6] == "/_wake" {
		return
	}

	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()

	switch state {
	case StateAwake:
		// Update last activity and proxy
		p.mu.Lock()
		p.lastActivity = time.Now()
		p.mu.Unlock()
		p.reverseProxy.ServeHTTP(w, r)

	case StateWaking:
		// Check if this is an API request (non-browser)
		if p.isAPIRequest(r) {
			// Hold the request and wait for wake-up
			p.waitAndProxy(w, r)
		} else {
			// Show waiting page for browser
			p.serveWaitingPage(w, r)
		}

	case StateSleeping:
		// Trigger wake
		go p.triggerWake()

		// Check if this is an API request (non-browser)
		if p.isAPIRequest(r) {
			// Hold the request and wait for wake-up
			p.waitAndProxy(w, r)
		} else {
			// Show waiting page for browser
			p.serveWaitingPage(w, r)
		}
	}
}

//nolint:unparam
func (p *Proxy) serveWaitingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	p.mu.RLock()
	status := p.wakeStatus
	p.mu.RUnlock()

	data := struct {
		Status      WakeStatus
		ServiceName string
	}{
		Status:      status,
		ServiceName: p.config.DeploymentName,
	}

	if err := p.templates.ExecuteTemplate(w, "waiting.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (p *Proxy) handleStatus(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	status := p.wakeStatus
	p.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (p *Proxy) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create channel for this client
	ch := make(chan WakeStatus, 10)
	p.sseMu.Lock()
	p.sseClients[ch] = struct{}{}
	p.sseMu.Unlock()

	defer func() {
		p.sseMu.Lock()
		delete(p.sseClients, ch)
		p.sseMu.Unlock()
		close(ch)
	}()

	// Send initial status
	p.mu.RLock()
	status := p.wakeStatus
	p.mu.RUnlock()

	data, _ := json.Marshal(status)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Stream updates
	for {
		select {
		case status := <-ch:
			data, _ := json.Marshal(status)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (p *Proxy) handleTriggerWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	go p.triggerWake()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "wake triggered"})
}

func (p *Proxy) handleHibernate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	go p.hibernate()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "hibernate triggered"})
}

func (p *Proxy) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"healthy": true,
		"state":   p.state.String(),
	})
}

// broadcastStatus sends status updates to all SSE clients
func (p *Proxy) broadcastStatus(status WakeStatus) {
	p.sseMu.RLock()
	defer p.sseMu.RUnlock()

	for ch := range p.sseClients {
		select {
		case ch <- status:
		default:
			// Channel full, skip
		}
	}
}

func (p *Proxy) updateStatus(state State, message string, progress int) {
	p.mu.Lock()
	p.state = state
	p.wakeStatus = WakeStatus{
		State:    state,
		Message:  message,
		Progress: progress,
	}
	if state == StateWaking && p.wakeStatus.StartedAt.IsZero() {
		p.wakeStatus.StartedAt = time.Now()
	}
	status := p.wakeStatus
	p.mu.Unlock()

	log.Printf("Status: %s - %s (%d%%)", state, message, progress)
	p.broadcastStatus(status)
}

func (p *Proxy) checkAndUpdateState() {
	ctx := context.Background()

	// Check SleepyService state
	state, err := p.k8s.GetSleepyServiceState(ctx, p.config.Namespace, p.config.SleepyServiceName)
	if err != nil {
		log.Printf("Error checking SleepyService state: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	switch state {
	case "Awake":
		p.state = StateAwake
		p.wakeStatus = WakeStatus{
			State:    StateAwake,
			Message:  "Service is running",
			Progress: 100,
		}
	case "Waking":
		if p.state != StateWaking {
			p.state = StateWaking
			p.wakeStatus = WakeStatus{
				State:    StateWaking,
				Message:  "Service is waking up",
				Progress: 50,
			}
		}
	case "Sleeping", "Hibernating":
		if p.state != StateWaking {
			p.state = StateSleeping
			p.wakeStatus = WakeStatus{
				State:    StateSleeping,
				Message:  "Service is hibernated",
				Progress: 0,
			}
		}
	}
}

func (p *Proxy) triggerWake() {
	p.mu.Lock()
	if p.state == StateWaking || p.state == StateAwake {
		p.mu.Unlock()
		return
	}
	p.state = StateWaking
	ctx, cancel := context.WithTimeout(context.Background(), p.config.WakeTimeout)
	p.wakeCancel = cancel
	p.mu.Unlock()

	defer cancel()

	p.updateStatus(StateWaking, "Starting wake-up sequence...", 5)

	// Update SleepyService status to request wake-up
	now := metav1.Now()
	if err := p.k8s.UpdateSleepyServiceStatus(
		ctx,
		p.config.Namespace,
		p.config.SleepyServiceName,
		"Awake",
		&now,
	); err != nil {
		log.Printf("Error updating SleepyService status: %v", err)
		p.updateStatus(StateSleeping, fmt.Sprintf("Failed to request wake-up: %v", err), 0)
		return
	}

	p.updateStatus(StateWaking, "Wake-up requested, waiting for controller...", 10)

	// Wait for SleepyService to become Awake
	if err := p.k8s.WaitForSleepyServiceAwake(
		ctx,
		p.config.Namespace,
		p.config.SleepyServiceName,
		p.onWakeProgress,
	); err != nil {
		log.Printf("Error waiting for wake-up: %v", err)
		p.updateStatus(StateSleeping, fmt.Sprintf("Wake-up timeout: %v", err), 0)
		return
	}

	// Step 3: Health check
	p.updateStatus(StateWaking, "Verifying application health...", 90)

	backendURL := p.config.BackendURL + p.config.HealthPath
	if err := p.waitForHealthy(ctx, backendURL); err != nil {
		log.Printf("Health check failed: %v", err)
		p.updateStatus(StateSleeping, fmt.Sprintf("Health check failed: %v", err), 0)
		return
	}

	p.mu.Lock()
	p.state = StateAwake
	p.lastActivity = time.Now()
	p.mu.Unlock()

	p.updateStatus(StateAwake, "Service is ready!", 100)
}

func (p *Proxy) onWakeProgress(message string, progress int) {
	// Map wake progress (0-100) to our range (10-85)
	mappedProgress := 10 + (progress * 75 / 100)
	p.updateStatus(StateWaking, message, mappedProgress)
}

func (p *Proxy) waitForHealthy(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 5 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}

		time.Sleep(2 * time.Second)
	}
}

func (p *Proxy) hibernate() {
	p.mu.Lock()
	if p.state == StateSleeping {
		p.mu.Unlock()
		return
	}

	// Cancel any ongoing wake
	if p.wakeCancel != nil {
		p.wakeCancel()
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p.updateStatus(StateSleeping, "Hibernating service...", 50)

	// Update SleepyService status to request hibernation
	now := metav1.Now()
	if err := p.k8s.UpdateSleepyServiceStatus(
		ctx,
		p.config.Namespace,
		p.config.SleepyServiceName,
		"Sleeping",
		&now,
	); err != nil {
		log.Printf("Error updating SleepyService status: %v", err)
	}

	p.mu.Lock()
	p.state = StateSleeping
	p.mu.Unlock()

	p.updateStatus(StateSleeping, "Service is hibernated", 0)
}

// isAPIRequest determines if the request is from an API client (not a browser)
func (p *Proxy) isAPIRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	// If Accept header contains text/html, it's likely a browser
	// Otherwise, treat it as an API request
	return accept != "" && !strings.Contains(accept, "text/html")
}

// waitAndProxy holds the request open and waits for the service to wake up,
// then proxies the request to the backend
func (p *Proxy) waitAndProxy(w http.ResponseWriter, r *http.Request) {
	// Create a channel to wait for wake-up completion
	wakeCh := make(chan bool, 1)

	// Start a goroutine to poll the state
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.After(p.config.WakeTimeout)

		for {
			select {
			case <-timeout:
				wakeCh <- false
				return
			case <-ticker.C:
				p.mu.RLock()
				state := p.state
				p.mu.RUnlock()

				if state == StateAwake {
					wakeCh <- true
					return
				}
			}
		}
	}()

	// Wait for wake-up or timeout
	success := <-wakeCh

	if !success {
		// Timeout - return 504 Gateway Timeout
		http.Error(w, "Service wake-up timeout", http.StatusGatewayTimeout)
		return
	}

	// Service is awake, proxy the request
	p.mu.Lock()
	p.lastActivity = time.Now()
	p.mu.Unlock()
	p.reverseProxy.ServeHTTP(w, r)
}

func (p *Proxy) idleMonitor() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.RLock()
		state := p.state
		lastActivity := p.lastActivity
		p.mu.RUnlock()

		if state == StateAwake && time.Since(lastActivity) > p.config.IdleTimeout {
			log.Printf("Idle timeout reached, hibernating...")
			p.hibernate()
		}
	}
}
