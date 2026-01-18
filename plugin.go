package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolFSPlugin defines the interface that all ToolFS plugins must implement.
// Plugins can be compiled to WASM modules for sandboxed execution.
//
// Example usage:
//
//	type MyPlugin struct {}
//
//	func (p *MyPlugin) Name() string { return "my-plugin" }
//	func (p *MyPlugin) Version() string { return "1.0.0" }
//	func (p *MyPlugin) Init(config map[string]interface{}) error {
//		// Initialize plugin with configuration
//		return nil
//	}
//	func (p *MyPlugin) Execute(input []byte) ([]byte, error) {
//		// Process input and return result
//		return []byte("result"), nil
//	}
//
// To compile as WASM:
//
//	GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm plugin.go
type ToolFSPlugin interface {
	// Name returns the unique name of the plugin.
	// This should be a short identifier (e.g., "file-processor", "data-validator").
	Name() string

	// Version returns the semantic version of the plugin (e.g., "1.2.3").
	Version() string

	// Init initializes the plugin with the provided configuration.
	// The config map can contain plugin-specific settings like:
	//   - "allowed_paths": []string - List of ToolFS paths the plugin can access
	//   - "timeout": int - Execution timeout in seconds
	//   - "memory_limit": int - Memory limit in bytes
	//   - Custom plugin-specific configuration
	//
	// Returns an error if initialization fails.
	Init(config map[string]interface{}) error

	// Execute runs the plugin's core functionality.
	// The input is typically JSON-encoded data containing:
	//   - "operation": string - The operation to perform (e.g., "process", "validate")
	//   - "path": string - ToolFS virtual path (e.g., "/toolfs/data/file.txt")
	//   - "data": interface{} - Additional operation-specific data
	//
	// The output should be JSON-encoded result data containing:
	//   - "success": bool - Whether the operation succeeded
	//   - "result": interface{} - Operation result data
	//   - "error": string - Error message if operation failed
	//
	// Example input:
	//   {
	//     "operation": "read_file",
	//     "path": "/toolfs/data/document.txt",
	//     "options": {"encoding": "utf8"}
	//   }
	//
	// Example output:
	//   {
	//     "success": true,
	//     "result": {"content": "file contents...", "size": 1234}
	//   }
	Execute(input []byte) ([]byte, error)
}

// SkillDocumentProvider is an optional interface that plugins can implement
// to provide SKILL.md documentation. This allows plugins to expose their
// capabilities and usage instructions.
type SkillDocumentProvider interface {
	// GetSkillDocument returns the SKILL.md content for this plugin.
	// Returns the markdown content as a string, or empty string if not available.
	GetSkillDocument() string
}

// PluginContext provides access to ToolFS functionality within plugins.
// This allows plugins to interact with the filesystem, memory, and RAG stores
// while respecting session-based access control.
type PluginContext struct {
	fs      *ToolFS
	session *Session
}

// NewPluginContext creates a new plugin context with ToolFS and session access.
func NewPluginContext(fs *ToolFS, session *Session) *PluginContext {
	return &PluginContext{
		fs:      fs,
		session: session,
	}
}

// ReadFile reads a file from ToolFS using the plugin's session.
func (ctx *PluginContext) ReadFile(path string) ([]byte, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.ReadFileWithSession(path, ctx.session)
}

// WriteFile writes data to a file in ToolFS using the plugin's session.
func (ctx *PluginContext) WriteFile(path string, data []byte) error {
	if ctx.fs == nil {
		return errors.New("ToolFS instance not available")
	}
	return ctx.fs.WriteFileWithSession(path, data, ctx.session)
}

// ListDir lists directory contents from ToolFS using the plugin's session.
func (ctx *PluginContext) ListDir(path string) ([]string, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.ListDirWithSession(path, ctx.session)
}

// Stat gets file metadata from ToolFS using the plugin's session.
func (ctx *PluginContext) Stat(path string) (*FileInfo, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.StatWithSession(path, ctx.session)
}

// PluginRequest represents a request to a plugin.
type PluginRequest struct {
	Operation string                 `json:"operation"`
	Path      string                 `json:"path,omitempty"`    // ToolFS virtual path
	Data      map[string]interface{} `json:"data,omitempty"`    // Additional data
	Options   map[string]interface{} `json:"options,omitempty"` // Operation options
}

// PluginResponse represents a response from a plugin.
type PluginResponse struct {
	Success  bool                   `json:"success"`
	Result   interface{}            `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// PluginRegistry manages registered plugins.
type PluginRegistry struct {
	plugins  map[string]ToolFSPlugin
	contexts map[string]*PluginContext // Plugin name -> context
}

// NewPluginRegistry creates a new plugin registry.
func NewPluginRegistry() *PluginRegistry {
	return &PluginRegistry{
		plugins:  make(map[string]ToolFSPlugin),
		contexts: make(map[string]*PluginContext),
	}
}

// Register registers a plugin with the registry.
// Returns an error if a plugin with the same name is already registered.
func (r *PluginRegistry) Register(plugin ToolFSPlugin, context *PluginContext) error {
	name := plugin.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin '%s' is already registered", name)
	}

	r.plugins[name] = plugin
	if context != nil {
		r.contexts[name] = context
	}

	return nil
}

// Get retrieves a plugin by name.
func (r *PluginRegistry) Get(name string) (ToolFSPlugin, error) {
	plugin, exists := r.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin '%s' not found", name)
	}
	return plugin, nil
}

// GetContext retrieves the context for a plugin.
func (r *PluginRegistry) GetContext(name string) (*PluginContext, error) {
	context, exists := r.contexts[name]
	if !exists {
		return nil, fmt.Errorf("context for plugin '%s' not found", name)
	}
	return context, nil
}

// List returns all registered plugin names.
func (r *PluginRegistry) List() []string {
	names := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		names = append(names, name)
	}
	return names
}

// Unregister removes a plugin from the registry.
func (r *PluginRegistry) Unregister(name string) {
	delete(r.plugins, name)
	delete(r.contexts, name)
}

// ExecutePlugin executes a plugin with the given input.
// This is a convenience method that handles request/response encoding.
func (r *PluginRegistry) ExecutePlugin(name string, request *PluginRequest) (*PluginResponse, error) {
	plugin, err := r.Get(name)
	if err != nil {
		return &PluginResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	// Encode request to JSON
	input, err := json.Marshal(request)
	if err != nil {
		return &PluginResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to encode request: %v", err),
		}, err
	}

	// Execute plugin
	output, err := plugin.Execute(input)
	if err != nil {
		return &PluginResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	// Decode response
	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return &PluginResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, err
	}

	return &response, nil
}

// InitPlugin initializes a plugin with the given configuration.
func (r *PluginRegistry) InitPlugin(name string, config map[string]interface{}) error {
	plugin, err := r.Get(name)
	if err != nil {
		return err
	}

	return plugin.Init(config)
}

// AddPluginRegistry adds plugin registry support to ToolFS.
// This allows ToolFS to directly access plugins from the registry
// without requiring a PluginManager. If a PluginManager exists,
// its registry will be used instead.
func (fs *ToolFS) AddPluginRegistry(registry *PluginRegistry) {
	if registry == nil {
		return
	}
	
	// If PluginManager exists, update its registry reference
	if fs.pluginManager != nil {
		fs.pluginManager.registry = registry
	}
	
	// Store registry for direct access
	fs.pluginRegistry = registry
}

// GetPluginRegistry returns the plugin registry associated with this ToolFS instance.
// If a PluginManager exists, returns its registry. Otherwise returns the direct registry.
func (fs *ToolFS) GetPluginRegistry() *PluginRegistry {
	if fs.pluginManager != nil && fs.pluginManager.registry != nil {
		return fs.pluginManager.registry
	}
	return fs.pluginRegistry
}

// WASMPluginRunner is an interface for running WASM-compiled plugins.
// This allows different WASM runtime implementations (e.g., wasmtime, wasmer).
type WASMPluginRunner interface {
	// Load loads a WASM module from bytes.
	Load(wasmBytes []byte) error

	// Call executes a WASM function with the given input.
	Call(function string, input []byte) ([]byte, error)

	// Close releases resources.
	Close() error
}

// Note: Actual WASM plugin loading would require a WASM runtime library.
// Example implementations could use:
//   - github.com/tetratelabs/wazero
//   - github.com/wasmerio/wasmer-go
//
// A plugin compiled to WASM would need to export functions that match
// the ToolFSPlugin interface, and the WASM runner would need to translate
// between Go types and WASM types.

// PluginManager manages plugin lifecycle, loading, and execution with sandboxing and timeouts.
type PluginManager struct {
	registry   *PluginRegistry
	plugins    map[string]*ManagedPlugin
	wasmLoader WASMPluginLoader // Optional WASM loader for WASM plugins
	timeout    time.Duration    // Default execution timeout
}

// ManagedPlugin wraps a plugin with management metadata.
type ManagedPlugin struct {
	Plugin     ToolFSPlugin
	Context    *PluginContext
	Source     string // Source: "injected" or file path
	LoadedAt   time.Time
	Config     map[string]interface{}
	Timeout    time.Duration // Per-plugin timeout override
	Sandboxed  bool          // Whether plugin runs in sandbox
	WASMModule []byte        // WASM module bytes if loaded from WASM
}

// WASMPluginLoader defines interface for loading WASM plugins.
// This allows different WASM runtime implementations.
type WASMPluginLoader interface {
	// LoadWASM loads a WASM module from file path and returns the bytes.
	LoadWASM(path string) ([]byte, error)

	// Instantiate creates a ToolFSPlugin instance from WASM bytes.
	Instantiate(wasmBytes []byte, context *PluginContext) (ToolFSPlugin, error)
}

// NewPluginManager creates a new PluginManager with default settings.
func NewPluginManager() *PluginManager {
	return &PluginManager{
		registry: NewPluginRegistry(),
		plugins:  make(map[string]*ManagedPlugin),
		timeout:  30 * time.Second, // Default 30 second timeout
	}
}

// SetTimeout sets the default execution timeout for plugins.
func (pm *PluginManager) SetTimeout(timeout time.Duration) {
	pm.timeout = timeout
}

// SetWASMLoader sets the WASM loader for loading WASM plugins.
func (pm *PluginManager) SetWASMLoader(loader WASMPluginLoader) {
	pm.wasmLoader = loader
}

// LoadPlugin loads a plugin from a file path.
// Supports both native Go plugins (.so files) and WASM plugins (.wasm files).
// For WASM plugins, the WASM loader must be configured.
func (pm *PluginManager) LoadPlugin(path string, context *PluginContext, config map[string]interface{}) error {
	if path == "" {
		return errors.New("plugin path cannot be empty")
	}

	// Check if plugin is already loaded
	if _, exists := pm.plugins[path]; exists {
		return fmt.Errorf("plugin already loaded from path: %s", path)
	}

	var plugin ToolFSPlugin
	var wasmBytes []byte

	// Determine plugin type based on file extension
	if strings.HasSuffix(strings.ToLower(path), ".wasm") {
		// WASM plugin
		if pm.wasmLoader == nil {
			return errors.New("WASM loader not configured, cannot load WASM plugin")
		}

		var err error
		wasmBytes, err = pm.wasmLoader.LoadWASM(path)
		if err != nil {
			return fmt.Errorf("failed to load WASM plugin: %w", err)
		}

		plugin, err = pm.wasmLoader.Instantiate(wasmBytes, context)
		if err != nil {
			return fmt.Errorf("failed to instantiate WASM plugin: %w", err)
		}
	} else {
		// For now, native Go plugins are not supported via file loading
		// In a real implementation, you would use plugin.Open() for .so files
		return errors.New("native Go plugin loading not yet implemented, use InjectPlugin instead")
	}

	// Initialize plugin with config
	if config == nil {
		config = make(map[string]interface{})
	}

	if err := plugin.Init(config); err != nil {
		return fmt.Errorf("plugin initialization failed: %w", err)
	}

	// Create managed plugin
	managed := &ManagedPlugin{
		Plugin:     plugin,
		Context:    context,
		Source:     path,
		LoadedAt:   time.Now(),
		Config:     config,
		Timeout:    pm.timeout, // Use default timeout
		Sandboxed:  true,       // WASM plugins are sandboxed by default
		WASMModule: wasmBytes,
	}

	pm.plugins[plugin.Name()] = managed

	// Register in registry
	if err := pm.registry.Register(plugin, context); err != nil {
		delete(pm.plugins, plugin.Name())
		return fmt.Errorf("failed to register plugin: %w", err)
	}

	return nil
}

// InjectPlugin injects a plugin directly into the ToolFS runtime.
// This is useful for native Go plugins or plugins created programmatically.
func (pm *PluginManager) InjectPlugin(plugin ToolFSPlugin, context *PluginContext, config map[string]interface{}) error {
	if plugin == nil {
		return errors.New("plugin cannot be nil")
	}

	name := plugin.Name()
	if name == "" {
		return errors.New("plugin name cannot be empty")
	}

	// Check if plugin is already loaded
	if _, exists := pm.plugins[name]; exists {
		return fmt.Errorf("plugin '%s' is already loaded", name)
	}

	// Initialize plugin with config
	if config == nil {
		config = make(map[string]interface{})
	}

	if err := plugin.Init(config); err != nil {
		return fmt.Errorf("plugin initialization failed: %w", err)
	}

	// Create managed plugin
	managed := &ManagedPlugin{
		Plugin:    plugin,
		Context:   context,
		Source:    "injected",
		LoadedAt:  time.Now(),
		Config:    config,
		Timeout:   pm.timeout, // Use default timeout
		Sandboxed: false,      // Injected plugins are not sandboxed by default
	}

	pm.plugins[name] = managed

	// Register in registry
	if err := pm.registry.Register(plugin, context); err != nil {
		delete(pm.plugins, name)
		return fmt.Errorf("failed to register plugin: %w", err)
	}

	return nil
}

// ListPlugins returns a list of all loaded plugin names.
func (pm *PluginManager) ListPlugins() []string {
	names := make([]string, 0, len(pm.plugins))
	for name := range pm.plugins {
		names = append(names, name)
	}
	return names
}

// GetPluginInfo returns information about a loaded plugin.
func (pm *PluginManager) GetPluginInfo(name string) (*ManagedPlugin, error) {
	managed, exists := pm.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin '%s' not found", name)
	}
	return managed, nil
}

// ExecutePlugin executes a plugin with the given input, respecting timeout and sandboxing.
func (pm *PluginManager) ExecutePlugin(name string, input []byte) ([]byte, error) {
	managed, exists := pm.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin '%s' not found", name)
	}

	// Get timeout for this plugin
	timeout := managed.Timeout
	if timeout == 0 {
		timeout = pm.timeout
	}

	// Execute with timeout
	resultChan := make(chan executeResult, 1)

	go func() {
		output, err := managed.Plugin.Execute(input)
		resultChan <- executeResult{output: output, err: err}
	}()

	select {
	case result := <-resultChan:
		if result.err != nil {
			return nil, result.err
		}
		return result.output, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("plugin execution timeout after %v", timeout)
	}
}

type executeResult struct {
	output []byte
	err    error
}

// UnloadPlugin removes a plugin from the manager.
func (pm *PluginManager) UnloadPlugin(name string) error {
	managed, exists := pm.plugins[name]
	if !exists {
		return fmt.Errorf("plugin '%s' not found", name)
	}

	// Unregister from registry
	pm.registry.Unregister(name)

	// Remove from manager
	delete(pm.plugins, name)

	// If it was a WASM plugin, close the module if needed
	_ = managed // Can be used for cleanup if needed

	return nil
}

// SetPluginTimeout sets a custom timeout for a specific plugin.
func (pm *PluginManager) SetPluginTimeout(name string, timeout time.Duration) error {
	managed, exists := pm.plugins[name]
	if !exists {
		return fmt.Errorf("plugin '%s' not found", name)
	}

	if timeout <= 0 {
		return errors.New("timeout must be positive")
	}

	managed.Timeout = timeout
	return nil
}

// SetPluginSandboxed sets whether a plugin runs in sandbox mode.
func (pm *PluginManager) SetPluginSandboxed(name string, sandboxed bool) error {
	managed, exists := pm.plugins[name]
	if !exists {
		return fmt.Errorf("plugin '%s' not found", name)
	}

	managed.Sandboxed = sandboxed
	return nil
}
