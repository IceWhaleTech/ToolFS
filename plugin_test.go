package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ExamplePlugin is a simple example plugin implementation.
type ExamplePlugin struct {
	name    string
	version string
	config  map[string]interface{}
}

func (p *ExamplePlugin) Name() string {
	return p.name
}

func (p *ExamplePlugin) Version() string {
	return p.version
}

func (p *ExamplePlugin) Init(config map[string]interface{}) error {
	p.config = config
	// Validate required configuration
	if timeout, ok := config["timeout"].(float64); ok {
		if timeout <= 0 {
			return errors.New("timeout must be positive")
		}
	}
	return nil
}

func (p *ExamplePlugin) Execute(input []byte) ([]byte, error) {
	var request PluginRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, err
	}

	response := PluginResponse{
		Success: true,
		Result: map[string]interface{}{
			"plugin":    p.name,
			"version":   p.version,
			"operation": request.Operation,
			"path":      request.Path,
		},
	}

	return json.Marshal(response)
}

// FileProcessorPlugin is an example plugin that processes files from ToolFS.
type FileProcessorPlugin struct {
	context *PluginContext
	config  map[string]interface{}
}

func (p *FileProcessorPlugin) Name() string {
	return "file-processor"
}

func (p *FileProcessorPlugin) Version() string {
	return "1.0.0"
}

func (p *FileProcessorPlugin) Init(config map[string]interface{}) error {
	p.config = config

	// Validate allowed paths if provided
	if paths, ok := config["allowed_paths"].([]interface{}); ok {
		for _, path := range paths {
			if _, ok := path.(string); !ok {
				return errors.New("allowed_paths must contain strings")
			}
		}
	}

	return nil
}

func (p *FileProcessorPlugin) Execute(input []byte) ([]byte, error) {
	var request PluginRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, err
	}

	switch request.Operation {
	case "read_and_process":
		if request.Path == "" {
			return nil, errors.New("path is required for read_and_process")
		}

		// Check allowed paths if configured
		if allowedPaths, ok := p.config["allowed_paths"].([]interface{}); ok {
			allowed := false
			for _, ap := range allowedPaths {
				if apStr, ok := ap.(string); ok && apStr == request.Path {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, errors.New("path not in allowed_paths")
			}
		}

		// Read file using plugin context
		if p.context == nil {
			return nil, errors.New("plugin context not available")
		}

		data, err := p.context.ReadFile(request.Path)
		if err != nil {
			return nil, err
		}

		// Process the data (simple example: convert to uppercase)
		processed := string(data)
		if len(processed) > 100 {
			processed = processed[:100] + "... (truncated)"
		}

		response := PluginResponse{
			Success: true,
			Result: map[string]interface{}{
				"original_size": len(data),
				"processed":     processed,
				"path":          request.Path,
			},
		}

		return json.Marshal(response)

	case "list_files":
		if request.Path == "" {
			return nil, errors.New("path is required for list_files")
		}

		if p.context == nil {
			return nil, errors.New("plugin context not available")
		}

		entries, err := p.context.ListDir(request.Path)
		if err != nil {
			return nil, err
		}

		response := PluginResponse{
			Success: true,
			Result: map[string]interface{}{
				"path":    request.Path,
				"entries": entries,
				"count":   len(entries),
			},
		}

		return json.Marshal(response)

	default:
		return nil, fmt.Errorf("unknown operation: %s", request.Operation)
	}
}

// ErrorPlugin is an example plugin that returns errors for testing.
type ErrorPlugin struct {
	initError    error
	executeError error
}

func (p *ErrorPlugin) Name() string {
	return "error-plugin"
}

func (p *ErrorPlugin) Version() string {
	return "1.0.0"
}

func (p *ErrorPlugin) Init(config map[string]interface{}) error {
	return p.initError
}

func (p *ErrorPlugin) Execute(input []byte) ([]byte, error) {
	if p.executeError != nil {
		return nil, p.executeError
	}

	response := PluginResponse{
		Success: false,
		Error:   "plugin execution error",
	}

	return json.Marshal(response)
}

func TestToolFSPluginInterface(t *testing.T) {
	// Test that ExamplePlugin implements ToolFSPlugin interface
	var _ ToolFSPlugin = (*ExamplePlugin)(nil)

	plugin := &ExamplePlugin{
		name:    "test-plugin",
		version: "1.0.0",
	}

	// Test Name()
	if plugin.Name() != "test-plugin" {
		t.Errorf("Expected name 'test-plugin', got '%s'", plugin.Name())
	}

	// Test Version()
	if plugin.Version() != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", plugin.Version())
	}

	// Test Init()
	err := plugin.Init(map[string]interface{}{
		"timeout": 30.0,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test Init() with invalid config
	err = plugin.Init(map[string]interface{}{
		"timeout": -1.0,
	})
	if err == nil {
		t.Error("Expected error for negative timeout")
	}

	// Test Execute()
	request := PluginRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := plugin.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Error("Expected successful response")
	}

	result, ok := response.Result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if result["operation"] != "test" {
		t.Errorf("Expected operation 'test', got '%v'", result["operation"])
	}
}

func TestPluginRegistry(t *testing.T) {
	registry := NewPluginRegistry()

	// Test Register()
	plugin1 := &ExamplePlugin{name: "plugin1", version: "1.0.0"}
	err := registry.Register(plugin1, nil)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Test duplicate registration
	err = registry.Register(plugin1, nil)
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}

	// Test Get()
	retrieved, err := registry.Get("plugin1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.Name() != "plugin1" {
		t.Errorf("Expected plugin name 'plugin1', got '%s'", retrieved.Name())
	}

	// Test Get() non-existent
	_, err = registry.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}

	// Test List()
	plugin2 := &ExamplePlugin{name: "plugin2", version: "1.0.0"}
	registry.Register(plugin2, nil)

	list := registry.List()
	if len(list) != 2 {
		t.Errorf("Expected 2 plugins, got %d", len(list))
	}

	// Test Unregister()
	registry.Unregister("plugin1")
	list = registry.List()
	if len(list) != 1 {
		t.Errorf("Expected 1 plugin after unregister, got %d", len(list))
	}

	if list[0] != "plugin2" {
		t.Errorf("Expected remaining plugin 'plugin2', got '%s'", list[0])
	}
}

func TestPluginContext(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("plugin-session", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	ctx := NewPluginContext(fs, session)

	// Test ReadFile()
	data, err := ctx.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "Hello, ToolFS!" {
		t.Errorf("Expected 'Hello, ToolFS!', got '%s'", string(data))
	}

	// Test WriteFile()
	err = ctx.WriteFile("/toolfs/data/plugin-test.txt", []byte("Plugin test"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify write
	data, err = ctx.ReadFile("/toolfs/data/plugin-test.txt")
	if err != nil {
		t.Fatalf("ReadFile after write failed: %v", err)
	}

	if string(data) != "Plugin test" {
		t.Errorf("Expected 'Plugin test', got '%s'", string(data))
	}

	// Test ListDir()
	entries, err := ctx.ListDir("/toolfs/data")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected entries in directory")
	}

	// Test Stat()
	info, err := ctx.Stat("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.IsDir {
		t.Error("Expected file to not be a directory")
	}

	if info.Size == 0 {
		t.Error("Expected non-zero file size")
	}
}

func TestFileProcessorPlugin(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("processor-session", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	ctx := NewPluginContext(fs, session)
	plugin := &FileProcessorPlugin{context: ctx}

	// Test Init()
	err = plugin.Init(map[string]interface{}{
		"allowed_paths": []interface{}{"/toolfs/data"},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test Execute - read_and_process (without allowed_paths restriction)
	pluginNoRestriction := &FileProcessorPlugin{context: ctx}
	pluginNoRestriction.Init(nil) // No allowed_paths restriction

	request := PluginRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := pluginNoRestriction.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	result := response.Result.(map[string]interface{})
	if result["path"] != "/toolfs/data/test.txt" {
		t.Errorf("Expected path '/toolfs/data/test.txt', got '%v'", result["path"])
	}

	// Test Execute - list_files (with allowed_paths)
	request = PluginRequest{
		Operation: "list_files",
		Path:      "/toolfs/data",
	}
	input, _ = json.Marshal(request)

	output, err = plugin.Execute(input)
	if err != nil {
		t.Fatalf("Execute list_files failed: %v", err)
	}

	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	result = response.Result.(map[string]interface{})
	entries, ok := result["entries"].([]interface{})
	if !ok {
		t.Fatal("Expected entries to be an array")
	}

	if len(entries) == 0 {
		t.Error("Expected non-empty entries list")
	}

	// Test Execute - invalid operation
	request = PluginRequest{
		Operation: "invalid_operation",
	}
	input, _ = json.Marshal(request)

	_, err = plugin.Execute(input)
	if err == nil {
		t.Error("Expected error for invalid operation")
	}

	// Test Execute - path not in allowed_paths
	// Create a new plugin instance for this test to avoid state issues
	plugin2 := &FileProcessorPlugin{context: ctx}
	err = plugin2.Init(map[string]interface{}{
		"allowed_paths": []interface{}{"/toolfs/data/subdir"},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	request = PluginRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ = json.Marshal(request)

	_, err = plugin2.Execute(input)
	if err == nil {
		t.Error("Expected error for path not in allowed_paths")
	}
	if err != nil && !strings.Contains(err.Error(), "allowed_paths") {
		t.Errorf("Expected error about allowed_paths, got: %v", err)
	}
}

func TestPluginRegistryExecutePlugin(t *testing.T) {
	registry := NewPluginRegistry()

	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)
	session, _ := fs.NewSession("test-session", []string{"/toolfs/data"})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "test-plugin", version: "1.0.0"}
	plugin.Init(nil)

	registry.Register(plugin, ctx)

	// Test ExecutePlugin()
	request := &PluginRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}

	response, err := registry.ExecutePlugin("test-plugin", request)
	if err != nil {
		t.Fatalf("ExecutePlugin failed: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	result := response.Result.(map[string]interface{})
	if result["plugin"] != "test-plugin" {
		t.Errorf("Expected plugin 'test-plugin', got '%v'", result["plugin"])
	}

	// Test ExecutePlugin - non-existent plugin
	_, err = registry.ExecutePlugin("nonexistent", request)
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}
}

func TestPluginRegistryInitPlugin(t *testing.T) {
	registry := NewPluginRegistry()

	plugin := &ExamplePlugin{name: "test-plugin", version: "1.0.0"}
	registry.Register(plugin, nil)

	// Test InitPlugin()
	err := registry.InitPlugin("test-plugin", map[string]interface{}{
		"timeout": 30.0,
	})
	if err != nil {
		t.Fatalf("InitPlugin failed: %v", err)
	}

	// Test InitPlugin - non-existent plugin
	err = registry.InitPlugin("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}

	// Test InitPlugin - invalid config
	err = registry.InitPlugin("test-plugin", map[string]interface{}{
		"timeout": -1.0,
	})
	if err == nil {
		t.Error("Expected error for invalid config")
	}
}

func TestPluginContextWithNilToolFS(t *testing.T) {
	ctx := &PluginContext{fs: nil, session: nil}

	// Test ReadFile with nil ToolFS
	_, err := ctx.ReadFile("/test/path")
	if err == nil {
		t.Error("Expected error for nil ToolFS")
	}

	// Test WriteFile with nil ToolFS
	err = ctx.WriteFile("/test/path", []byte("test"))
	if err == nil {
		t.Error("Expected error for nil ToolFS")
	}

	// Test ListDir with nil ToolFS
	_, err = ctx.ListDir("/test/path")
	if err == nil {
		t.Error("Expected error for nil ToolFS")
	}

	// Test Stat with nil ToolFS
	_, err = ctx.Stat("/test/path")
	if err == nil {
		t.Error("Expected error for nil ToolFS")
	}
}

func TestPluginRequestResponseJSON(t *testing.T) {
	// Test PluginRequest JSON encoding/decoding
	request := PluginRequest{
		Operation: "read_file",
		Path:      "/toolfs/data/test.txt",
		Data: map[string]interface{}{
			"encoding": "utf8",
		},
		Options: map[string]interface{}{
			"timeout": 30,
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	var decoded PluginRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if decoded.Operation != "read_file" {
		t.Errorf("Operation mismatch")
	}
	if decoded.Path != "/toolfs/data/test.txt" {
		t.Errorf("Path mismatch")
	}

	// Test PluginResponse JSON encoding/decoding
	response := PluginResponse{
		Success: true,
		Result: map[string]interface{}{
			"content": "test content",
			"size":    12,
		},
		Metadata: map[string]interface{}{
			"timestamp": "2023-01-01T00:00:00Z",
		},
	}

	data, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	var decodedResp PluginResponse
	if err := json.Unmarshal(data, &decodedResp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !decodedResp.Success {
		t.Error("Success mismatch")
	}

	result := decodedResp.Result.(map[string]interface{})
	if result["size"].(float64) != 12 {
		t.Errorf("Result size mismatch")
	}
}

func TestFileProcessorPluginWithoutContext(t *testing.T) {
	plugin := &FileProcessorPlugin{context: nil}

	plugin.Init(nil)

	request := PluginRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	_, err := plugin.Execute(input)
	if err == nil {
		t.Error("Expected error when plugin context is nil")
	}
}

func TestErrorPlugin(t *testing.T) {
	// Test Init error
	plugin := &ErrorPlugin{
		initError: errors.New("initialization failed"),
	}

	err := plugin.Init(nil)
	if err == nil {
		t.Error("Expected init error")
	}

	// Test Execute error
	plugin = &ErrorPlugin{
		executeError: errors.New("execution failed"),
	}
	plugin.Init(nil)

	input := []byte(`{"operation":"test"}`)
	_, err = plugin.Execute(input)
	if err == nil {
		t.Error("Expected execute error")
	}

	// Test Execute with no error (returns error response)
	plugin = &ErrorPlugin{}
	plugin.Init(nil)

	output, err := plugin.Execute(input)
	if err != nil {
		t.Fatalf("Execute should not return error: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if response.Success {
		t.Error("Expected unsuccessful response")
	}
}

// MockWASMLoader is a mock implementation for testing WASM loading.
type MockWASMLoader struct {
	loadFunc        func(string) ([]byte, error)
	instantiateFunc func([]byte, *PluginContext) (ToolFSPlugin, error)
}

func (m *MockWASMLoader) LoadWASM(path string) ([]byte, error) {
	if m.loadFunc != nil {
		return m.loadFunc(path)
	}
	return []byte("mock wasm bytes"), nil
}

func (m *MockWASMLoader) Instantiate(wasmBytes []byte, context *PluginContext) (ToolFSPlugin, error) {
	if m.instantiateFunc != nil {
		return m.instantiateFunc(wasmBytes, context)
	}
	// Return a mock plugin
	return &ExamplePlugin{name: "wasm-plugin", version: "1.0.0"}, nil
}

func TestPluginManagerInjectPlugin(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "test-plugin", version: "1.0.0"}

	// Test InjectPlugin
	err := pm.InjectPlugin(plugin, ctx, nil)
	if err != nil {
		t.Fatalf("InjectPlugin failed: %v", err)
	}

	// Verify plugin is loaded
	plugins := pm.ListPlugins()
	if len(plugins) != 1 {
		t.Errorf("Expected 1 plugin, got %d", len(plugins))
	}

	if plugins[0] != "test-plugin" {
		t.Errorf("Expected plugin 'test-plugin', got '%s'", plugins[0])
	}

	// Test duplicate injection
	err = pm.InjectPlugin(plugin, ctx, nil)
	if err == nil {
		t.Error("Expected error for duplicate plugin injection")
	}

	// Test nil plugin
	err = pm.InjectPlugin(nil, ctx, nil)
	if err == nil {
		t.Error("Expected error for nil plugin")
	}

	// Test plugin with empty name
	emptyPlugin := &ExamplePlugin{name: "", version: "1.0.0"}
	err = pm.InjectPlugin(emptyPlugin, ctx, nil)
	if err == nil {
		t.Error("Expected error for plugin with empty name")
	}
}

func TestPluginManagerListPlugins(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	// Initially empty
	plugins := pm.ListPlugins()
	if len(plugins) != 0 {
		t.Errorf("Expected 0 plugins initially, got %d", len(plugins))
	}

	// Inject multiple plugins
	pm.InjectPlugin(&ExamplePlugin{name: "plugin1", version: "1.0.0"}, ctx, nil)
	pm.InjectPlugin(&ExamplePlugin{name: "plugin2", version: "1.0.0"}, ctx, nil)
	pm.InjectPlugin(&ExamplePlugin{name: "plugin3", version: "1.0.0"}, ctx, nil)

	plugins = pm.ListPlugins()
	if len(plugins) != 3 {
		t.Errorf("Expected 3 plugins, got %d", len(plugins))
	}

	// Verify all plugins are listed
	pluginMap := make(map[string]bool)
	for _, name := range plugins {
		pluginMap[name] = true
	}

	if !pluginMap["plugin1"] || !pluginMap["plugin2"] || !pluginMap["plugin3"] {
		t.Error("Expected all plugins to be listed")
	}
}

func TestPluginManagerExecutePlugin(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "test-plugin", version: "1.0.0"}
	pm.InjectPlugin(plugin, ctx, nil)

	// Test ExecutePlugin
	request := PluginRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := pm.ExecutePlugin("test-plugin", input)
	if err != nil {
		t.Fatalf("ExecutePlugin failed: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	// Test ExecutePlugin - non-existent plugin
	_, err = pm.ExecutePlugin("nonexistent", input)
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}
}

func TestPluginManagerExecutePluginTimeout(t *testing.T) {
	pm := NewPluginManager()
	pm.SetTimeout(100 * time.Millisecond) // Short timeout

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	// Create a slow plugin
	slowPlugin := &SlowPlugin{delay: 200 * time.Millisecond}
	pm.InjectPlugin(slowPlugin, ctx, nil)

	request := PluginRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	// Should timeout
	_, err := pm.ExecutePlugin("slow-plugin", input)
	if err == nil {
		t.Error("Expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// SlowPlugin is a plugin that delays execution for testing timeouts.
type SlowPlugin struct {
	delay time.Duration
}

func (p *SlowPlugin) Name() string    { return "slow-plugin" }
func (p *SlowPlugin) Version() string { return "1.0.0" }
func (p *SlowPlugin) Init(config map[string]interface{}) error {
	if delay, ok := config["delay"].(float64); ok {
		p.delay = time.Duration(delay) * time.Millisecond
	}
	return nil
}
func (p *SlowPlugin) Execute(input []byte) ([]byte, error) {
	time.Sleep(p.delay)
	response := PluginResponse{Success: true, Result: "delayed result"}
	return json.Marshal(response)
}

func TestPluginManagerLoadPlugin(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	// Test loading without WASM loader (should fail for .wasm files)
	err := pm.LoadPlugin("test.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when loading WASM without loader")
	}
	if !strings.Contains(err.Error(), "WASM loader not configured") {
		t.Errorf("Expected WASM loader error, got: %v", err)
	}

	// Test loading with mock WASM loader
	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	err = pm.LoadPlugin("test.wasm", ctx, nil)
	if err != nil {
		t.Fatalf("LoadPlugin with WASM loader failed: %v", err)
	}

	// Verify plugin is loaded
	plugins := pm.ListPlugins()
	if len(plugins) != 1 {
		t.Errorf("Expected 1 plugin after loading, got %d", len(plugins))
	}

	// Test loading duplicate plugin
	err = pm.LoadPlugin("test.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error for duplicate plugin loading")
	}

	// Test loading native plugin (not yet implemented)
	err = pm.LoadPlugin("test.so", ctx, nil)
	if err == nil {
		t.Error("Expected error for native plugin (not implemented)")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("Expected 'not yet implemented' error, got: %v", err)
	}
}

func TestPluginManagerLoadPluginWithConfig(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	config := map[string]interface{}{
		"timeout":      60.0,
		"memory_limit": 1024 * 1024,
	}

	err := pm.LoadPlugin("config-test.wasm", ctx, config)
	if err != nil {
		t.Fatalf("LoadPlugin with config failed: %v", err)
	}

	// Verify config was passed
	info, err := pm.GetPluginInfo("wasm-plugin")
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if info.Config == nil {
		t.Error("Expected config to be set")
	}
}

func TestPluginManagerGetPluginInfo(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "info-plugin", version: "1.0.0"}
	pm.InjectPlugin(plugin, ctx, map[string]interface{}{"test": "value"})

	// Test GetPluginInfo
	info, err := pm.GetPluginInfo("info-plugin")
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if info.Plugin.Name() != "info-plugin" {
		t.Errorf("Expected plugin name 'info-plugin', got '%s'", info.Plugin.Name())
	}

	if info.Source != "injected" {
		t.Errorf("Expected source 'injected', got '%s'", info.Source)
	}

	if info.Context == nil {
		t.Error("Expected context to be set")
	}

	// Test GetPluginInfo - non-existent plugin
	_, err = pm.GetPluginInfo("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}
}

func TestPluginManagerUnloadPlugin(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "unload-plugin", version: "1.0.0"}
	pm.InjectPlugin(plugin, ctx, nil)

	// Verify plugin is loaded
	if len(pm.ListPlugins()) != 1 {
		t.Error("Expected plugin to be loaded")
	}

	// Test UnloadPlugin
	err := pm.UnloadPlugin("unload-plugin")
	if err != nil {
		t.Fatalf("UnloadPlugin failed: %v", err)
	}

	// Verify plugin is unloaded
	if len(pm.ListPlugins()) != 0 {
		t.Error("Expected plugin to be unloaded")
	}

	// Test UnloadPlugin - non-existent plugin
	err = pm.UnloadPlugin("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}
}

func TestPluginManagerSetPluginTimeout(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "timeout-plugin", version: "1.0.0"}
	pm.InjectPlugin(plugin, ctx, nil)

	// Test SetPluginTimeout
	customTimeout := 60 * time.Second
	err := pm.SetPluginTimeout("timeout-plugin", customTimeout)
	if err != nil {
		t.Fatalf("SetPluginTimeout failed: %v", err)
	}

	info, _ := pm.GetPluginInfo("timeout-plugin")
	if info.Timeout != customTimeout {
		t.Errorf("Expected timeout %v, got %v", customTimeout, info.Timeout)
	}

	// Test SetPluginTimeout - non-existent plugin
	err = pm.SetPluginTimeout("nonexistent", customTimeout)
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}

	// Test SetPluginTimeout - invalid timeout
	err = pm.SetPluginTimeout("timeout-plugin", -1)
	if err == nil {
		t.Error("Expected error for invalid timeout")
	}
}

func TestPluginManagerSetPluginSandboxed(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	plugin := &ExamplePlugin{name: "sandbox-plugin", version: "1.0.0"}
	pm.InjectPlugin(plugin, ctx, nil)

	// Test SetPluginSandboxed
	err := pm.SetPluginSandboxed("sandbox-plugin", true)
	if err != nil {
		t.Fatalf("SetPluginSandboxed failed: %v", err)
	}

	info, _ := pm.GetPluginInfo("sandbox-plugin")
	if !info.Sandboxed {
		t.Error("Expected plugin to be sandboxed")
	}

	err = pm.SetPluginSandboxed("sandbox-plugin", false)
	if err != nil {
		t.Fatalf("SetPluginSandboxed failed: %v", err)
	}

	info, _ = pm.GetPluginInfo("sandbox-plugin")
	if info.Sandboxed {
		t.Error("Expected plugin to not be sandboxed")
	}

	// Test SetPluginSandboxed - non-existent plugin
	err = pm.SetPluginSandboxed("nonexistent", true)
	if err == nil {
		t.Error("Expected error for non-existent plugin")
	}
}

func TestPluginManagerSetTimeout(t *testing.T) {
	pm := NewPluginManager()

	// Test default timeout
	defaultTimeout := 30 * time.Second
	if pm.timeout != defaultTimeout {
		t.Errorf("Expected default timeout %v, got %v", defaultTimeout, pm.timeout)
	}

	// Test SetTimeout
	customTimeout := 60 * time.Second
	pm.SetTimeout(customTimeout)

	if pm.timeout != customTimeout {
		t.Errorf("Expected timeout %v, got %v", customTimeout, pm.timeout)
	}
}

func TestPluginManagerWASMLoader(t *testing.T) {
	pm := NewPluginManager()

	// Test SetWASMLoader
	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	if pm.wasmLoader != mockLoader {
		t.Error("WASM loader not set correctly")
	}
}

func TestPluginManagerLoadPluginErrorHandling(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	// Test empty path
	err := pm.LoadPlugin("", ctx, nil)
	if err == nil {
		t.Error("Expected error for empty path")
	}

	// Test WASM loader that fails to load
	failingLoader := &MockWASMLoader{
		loadFunc: func(path string) ([]byte, error) {
			return nil, errors.New("file not found")
		},
	}
	pm.SetWASMLoader(failingLoader)

	err = pm.LoadPlugin("nonexistent.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when WASM loader fails")
	}

	// Test WASM loader that fails to instantiate
	failingInstantiateLoader := &MockWASMLoader{
		instantiateFunc: func(wasmBytes []byte, context *PluginContext) (ToolFSPlugin, error) {
			return nil, errors.New("instantiation failed")
		},
	}
	pm.SetWASMLoader(failingInstantiateLoader)

	err = pm.LoadPlugin("instantiate-fail.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when instantiation fails")
	}

	// Test plugin that fails initialization
	initFailLoader := &MockWASMLoader{
		instantiateFunc: func(wasmBytes []byte, context *PluginContext) (ToolFSPlugin, error) {
			return &ErrorPlugin{initError: errors.New("init failed")}, nil
		},
	}
	pm.SetWASMLoader(initFailLoader)

	err = pm.LoadPlugin("init-fail.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when plugin initialization fails")
	}
}

func TestPluginManagerExecutePluginWithCustomTimeout(t *testing.T) {
	pm := NewPluginManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewPluginContext(fs, session)

	// Create plugin with custom timeout
	slowPlugin := &SlowPlugin{delay: 200 * time.Millisecond}
	pm.InjectPlugin(slowPlugin, ctx, nil)

	// Set custom timeout for this plugin
	pm.SetPluginTimeout("slow-plugin", 300*time.Millisecond)

	request := PluginRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	// Should not timeout with custom timeout
	output, err := pm.ExecutePlugin("slow-plugin", input)
	if err != nil {
		t.Fatalf("ExecutePlugin should not timeout with custom timeout: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Error("Expected successful response")
	}
}

func TestPluginManagerIntegration(t *testing.T) {
	pm := NewPluginManager()
	pm.SetTimeout(5 * time.Second)

	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)
	session, _ := fs.NewSession("integration-session", []string{"/toolfs/data"})
	ctx := NewPluginContext(fs, session)

	// Inject file processor plugin (without path restriction for this test)
	fileProcessor := &FileProcessorPlugin{context: ctx}
	pm.InjectPlugin(fileProcessor, ctx, nil) // No path restriction

	// Execute plugin
	request := &PluginRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := pm.ExecutePlugin("file-processor", input)
	if err != nil {
		t.Fatalf("ExecutePlugin failed: %v", err)
	}

	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	// List plugins
	plugins := pm.ListPlugins()
	if len(plugins) != 1 {
		t.Errorf("Expected 1 plugin, got %d", len(plugins))
	}

	// Get plugin info
	info, err := pm.GetPluginInfo("file-processor")
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if info.Plugin.Name() != "file-processor" {
		t.Errorf("Expected plugin name 'file-processor', got '%s'", info.Plugin.Name())
	}

	// Unload plugin
	err = pm.UnloadPlugin("file-processor")
	if err != nil {
		t.Fatalf("UnloadPlugin failed: %v", err)
	}

	if len(pm.ListPlugins()) != 0 {
		t.Error("Expected no plugins after unload")
	}
}
