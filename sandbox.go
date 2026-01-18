package toolfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// SandboxConfig configures sandbox behavior for plugin execution
type SandboxConfig struct {
	CPUTimeout    time.Duration // Maximum CPU time allowed
	MemoryLimit   int64         // Maximum memory in bytes (0 = no limit)
	AllowHostFS   bool          // Allow direct host filesystem access (should be false)
	CaptureStdout bool          // Capture stdout output
	CaptureStderr bool          // Capture stderr output
	AuditLog      AuditLogger   // Optional audit logger for plugin executions
}

// DefaultSandboxConfig returns a safe default sandbox configuration
func DefaultSandboxConfig() *SandboxConfig {
	return &SandboxConfig{
		CPUTimeout:    30 * time.Second,
		MemoryLimit:   64 * 1024 * 1024, // 64 MB default
		AllowHostFS:   false,             // Block host filesystem access
		CaptureStdout: true,
		CaptureStderr: true,
	}
}

// PluginExecutionResult contains the result of a sandboxed plugin execution
type PluginExecutionResult struct {
	Output      []byte            `json:"output"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
	CPUTime     time.Duration     `json:"cpu_time"`
	MemoryUsed  int64             `json:"memory_used"`
	Success     bool              `json:"success"`
	Error       string            `json:"error,omitempty"`
	Violations  []string          `json:"violations,omitempty"` // Security violations detected
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// WASMSandbox defines the interface for WASM-based plugin sandboxing
type WASMSandbox interface {
	// Execute runs a plugin in the sandbox with resource limits
	Execute(plugin ToolFSPlugin, input []byte, config *SandboxConfig, context *PluginContext) (*PluginExecutionResult, error)
	
	// LoadWASMModule loads a WASM module for execution
	LoadWASMModule(wasmBytes []byte) error
	
	// Close releases sandbox resources
	Close() error
}

// InMemorySandbox is a mock/in-memory sandbox implementation for testing
// In production, this would use an actual WASM runtime (e.g., wazero, wasmer)
type InMemorySandbox struct {
	mu           sync.Mutex
	loadedModule []byte
	violations   []string
}

// NewInMemorySandbox creates a new in-memory sandbox
func NewInMemorySandbox() *InMemorySandbox {
	return &InMemorySandbox{
		violations: make([]string, 0),
	}
}

// LoadWASMModule loads a WASM module (mock implementation)
func (s *InMemorySandbox) LoadWASMModule(wasmBytes []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadedModule = wasmBytes
	return nil
}

// Close releases sandbox resources
func (s *InMemorySandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadedModule = nil
	return nil
}

// Execute runs a plugin with sandboxing
func (s *InMemorySandbox) Execute(plugin ToolFSPlugin, input []byte, config *SandboxConfig, ctx *PluginContext) (*PluginExecutionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// Reset violations for this execution
	s.violations = make([]string, 0)
	
	// Create context with timeout
	ctxTimeout := context.Background()
	if config.CPUTimeout > 0 {
		var cancel context.CancelFunc
		ctxTimeout, cancel = context.WithTimeout(context.Background(), config.CPUTimeout)
		defer cancel()
	}
	
	// Capture stdout/stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	var originalStdout, originalStderr *os.File
	var stdoutDone, stderrDone chan struct{}
	var stdoutWriter, stderrWriter *os.File // Save write end for later closing
	
	if config.CaptureStdout {
		originalStdout = os.Stdout
		r, w, _ := os.Pipe()
		stdoutWriter = w
		os.Stdout = w
		stdoutDone = make(chan struct{})
		
		go func() {
			io.Copy(&stdoutBuf, r)
			r.Close()
			close(stdoutDone)
		}()
		defer func() {
			w.Close()
			// Don't wait here, wait after goroutine completes
		}()
	}
	
	if config.CaptureStderr {
		originalStderr = os.Stderr
		r, w, _ := os.Pipe()
		stderrWriter = w
		os.Stderr = w
		stderrDone = make(chan struct{})
		
		go func() {
			io.Copy(&stderrBuf, r)
			r.Close()
			close(stderrDone)
		}()
		defer func() {
			w.Close()
			// Don't wait here, wait after goroutine completes
		}()
	}
	
	// Track execution start time and memory
	startTime := time.Now()
	var memoryUsed int64
	
	// Create a wrapped plugin that enforces filesystem restrictions
	restrictedPlugin := &RestrictedPlugin{
		plugin:     plugin,
		config:     config,
		context:    ctx,
		sandbox:    s,
	}
	
	// Execute plugin with timeout and resource monitoring
	resultChan := make(chan *PluginExecutionResult, 1)
	
	go func() {
		output, err := restrictedPlugin.Execute(input)
		
		cpuTime := time.Since(startTime)
		
		// Restore original stdout/stderr
		if originalStdout != nil {
			os.Stdout = originalStdout
		}
		if originalStderr != nil {
			os.Stderr = originalStderr
		}
		
		// Close write end to trigger copy goroutine completion
		// Note: Write end needs to be closed after use, but since it's in defer, we need to explicitly close it here
		// The problem is that write end variable w is in defer scope, cannot access here
		// So we need to redesign: don't wait in goroutine, wait in main function instead
		
		result := &PluginExecutionResult{
			Output:     output,
			Stdout:     "", // Set empty first, fill later
			Stderr:     "", // Set empty first, fill later
			CPUTime:    cpuTime,
			MemoryUsed: memoryUsed,
			Success:    err == nil,
			Violations: make([]string, len(s.violations)),
			Metadata: map[string]interface{}{
				"plugin_name": plugin.Name(),
				"plugin_version": plugin.Version(),
			},
		}
		
		copy(result.Violations, s.violations)
		
		if err != nil {
			result.Error = err.Error()
		}
		
		// Check memory limit
		if config.MemoryLimit > 0 && memoryUsed > config.MemoryLimit {
			result.Success = false
			result.Error = fmt.Sprintf("memory limit exceeded: %d > %d bytes", memoryUsed, config.MemoryLimit)
			result.Violations = append(result.Violations, "memory_limit_exceeded")
		}
		
		resultChan <- result
	}()
	
	// Wait for execution or timeout
	select {
	case result := <-resultChan:
		// Close write end to trigger copy completion
		if stdoutWriter != nil {
			stdoutWriter.Close()
		}
		if stderrWriter != nil {
			stderrWriter.Close()
		}
		
		// Wait for stdout/stderr copy completion
		if stdoutDone != nil {
			<-stdoutDone
		}
		if stderrDone != nil {
			<-stderrDone
		}
		
		// Now safe to read buffers
		result.Stdout = stdoutBuf.String()
		result.Stderr = stderrBuf.String()
		
		// Check timeout
		if config.CPUTimeout > 0 && result.CPUTime > config.CPUTimeout {
			result.Success = false
			result.Error = fmt.Sprintf("CPU timeout exceeded: %v", result.CPUTime)
			result.Violations = append(result.Violations, "cpu_timeout_exceeded")
		}
		
		// Log audit entry if configured
		if config.AuditLog != nil {
			entry := AuditLogEntry{
				Timestamp:   time.Now(),
				SessionID:   getSessionID(ctx),
				Operation:   "PluginExecute",
				Path:        fmt.Sprintf("plugin:%s", plugin.Name()),
				Success:     result.Success,
				Error:       result.Error,
				BytesRead:   int64(len(input)),
				BytesWritten: int64(len(result.Output)),
				AccessDenied: len(result.Violations) > 0,
			}
			config.AuditLog.Log(entry)
		}
		
		return result, nil
	case <-ctxTimeout.Done():
		return &PluginExecutionResult{
			Success:    false,
			Error:      fmt.Sprintf("execution timeout after %v", config.CPUTimeout),
			Violations: []string{"cpu_timeout"},
		}, errors.New("execution timeout")
	}
}

func getSessionID(ctx *PluginContext) string {
	if ctx != nil && ctx.session != nil {
		return ctx.session.ID
	}
	return ""
}

// RestrictedPlugin wraps a plugin to enforce filesystem restrictions
type RestrictedPlugin struct {
	plugin  ToolFSPlugin
	config  *SandboxConfig
	context *PluginContext
	sandbox *InMemorySandbox
}

// Execute runs the plugin with filesystem access restrictions
func (rp *RestrictedPlugin) Execute(input []byte) ([]byte, error) {
	// Parse request to check for illegal filesystem access
	var request PluginRequest
	if err := json.Unmarshal(input, &request); err == nil {
		// Check for host filesystem access attempts
		if !rp.config.AllowHostFS {
			// Block access to host paths (outside ToolFS)
			if request.Path != "" {
				// Normalize path
				path := normalizeVirtualPath(request.Path)
				
				// Block absolute paths that don't start with /toolfs
				if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "/toolfs") {
					rp.sandbox.violations = append(rp.sandbox.violations, fmt.Sprintf("blocked_host_fs_access: %s", path))
					return nil, fmt.Errorf("access to host filesystem blocked: %s (only ToolFS paths allowed)", path)
				}
				
				// Block attempts to escape with ../
				if strings.Contains(path, "..") {
					rp.sandbox.violations = append(rp.sandbox.violations, fmt.Sprintf("path_traversal_attempt: %s", path))
					return nil, fmt.Errorf("path traversal blocked: %s", path)
				}
				
				// Block attempts to access system directories
				// Check both normalized and original path
				blockedPrefixes := []string{
					"/etc", "/sys", "/proc", "/dev",
					"C:/Windows", "C:/System32", "C:\\Windows", "C:\\System32",
				}
				originalPath := request.Path // Use original path for Windows checks
				for _, prefix := range blockedPrefixes {
					if strings.HasPrefix(path, prefix) || strings.HasPrefix(originalPath, prefix) {
						rp.sandbox.violations = append(rp.sandbox.violations, fmt.Sprintf("blocked_system_path: %s", path))
						return nil, fmt.Errorf("access to system path blocked: %s", path)
					}
				}
			}
		}
	}
	
	// Execute the actual plugin
	return rp.plugin.Execute(input)
}

// SandboxedPluginManager extends PluginManager with sandboxing support
type SandboxedPluginManager struct {
	*PluginManager
	sandbox WASMSandbox
	configs map[string]*SandboxConfig // Per-plugin sandbox configs
}

// NewSandboxedPluginManager creates a plugin manager with sandboxing
func NewSandboxedPluginManager(sandbox WASMSandbox) *SandboxedPluginManager {
	return &SandboxedPluginManager{
		PluginManager: NewPluginManager(),
		sandbox:       sandbox,
		configs:       make(map[string]*SandboxConfig),
	}
}

// SetSandboxConfig sets sandbox configuration for a specific plugin
func (spm *SandboxedPluginManager) SetSandboxConfig(pluginName string, config *SandboxConfig) {
	if config == nil {
		config = DefaultSandboxConfig()
	}
	spm.configs[pluginName] = config
}

// GetSandboxConfig gets sandbox configuration for a plugin
func (spm *SandboxedPluginManager) GetSandboxConfig(pluginName string) *SandboxConfig {
	if config, exists := spm.configs[pluginName]; exists {
		return config
	}
	return DefaultSandboxConfig()
}

// ExecutePluginSandboxed executes a plugin with sandboxing
func (spm *SandboxedPluginManager) ExecutePluginSandboxed(name string, input []byte, ctx *PluginContext) (*PluginExecutionResult, error) {
	plugin, err := spm.registry.Get(name)
	if err != nil {
		return nil, err
	}
	
	// Get sandbox config for this plugin
	config := spm.GetSandboxConfig(name)
	
	// Execute in sandbox
	return spm.sandbox.Execute(plugin, input, config, ctx)
}

// ExecutePluginWithSandbox wraps ExecutePlugin with sandboxing
func (spm *SandboxedPluginManager) ExecutePluginWithSandbox(name string, request *PluginRequest) (*PluginResponse, error) {
	// Get plugin context
	ctx, err := spm.registry.GetContext(name)
	if err != nil {
		// Try to get default context or create one
		ctx = nil
	}
	
	// Marshal request
	input, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	
	// Execute in sandbox
	result, err := spm.ExecutePluginSandboxed(name, input, ctx)
	if err != nil {
		return &PluginResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}
	
	if !result.Success {
		return &PluginResponse{
			Success: false,
			Error:   result.Error,
		}, errors.New(result.Error)
	}
	
	// Parse result output
	var response PluginResponse
	if err := json.Unmarshal(result.Output, &response); err != nil {
		// If output is not JSON, wrap it
		response = PluginResponse{
			Success: true,
			Result:  string(result.Output),
			Metadata: map[string]interface{}{
				"stdout": result.Stdout,
				"stderr": result.Stderr,
				"cpu_time_ms": result.CPUTime.Milliseconds(),
				"memory_used": result.MemoryUsed,
			},
		}
	}
	
	return &response, nil
}

