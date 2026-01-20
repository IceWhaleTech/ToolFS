package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ExampleSkill is a simple example skill implementation.
type ExampleSkill struct {
	name    string
	version string
	config  map[string]interface{}
}

func (p *ExampleSkill) Name() string {
	return p.name
}

func (p *ExampleSkill) Version() string {
	return p.version
}

func (p *ExampleSkill) Init(config map[string]interface{}) error {
	p.config = config
	// Validate required configuration
	if timeout, ok := config["timeout"].(float64); ok {
		if timeout <= 0 {
			return errors.New("timeout must be positive")
		}
	}
	return nil
}

func (p *ExampleSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, err
	}

	response := SkillResponse{
		Success: true,
		Result: map[string]interface{}{
			"code":    p.name,
			"version":   p.version,
			"operation": request.Operation,
			"path":      request.Path,
		},
	}

	return json.Marshal(response)
}

// FileProcessorSkill is an example skill that processes files from ToolFS.
type FileProcessorSkill struct {
	context *SkillContext
	config  map[string]interface{}
}

func (p *FileProcessorSkill) Name() string {
	return "file-processor"
}

func (p *FileProcessorSkill) Version() string {
	return "1.0.0"
}

func (p *FileProcessorSkill) Init(config map[string]interface{}) error {
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

func (p *FileProcessorSkill) Execute(input []byte) ([]byte, error) {
	var request SkillRequest
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

		// Read file using skill context
		if p.context == nil {
			return nil, errors.New("skill context not available")
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

		response := SkillResponse{
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
			return nil, errors.New("skill context not available")
		}

		entries, err := p.context.ListDir(request.Path)
		if err != nil {
			return nil, err
		}

		response := SkillResponse{
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

// ErrorSkill is an example skill that returns errors for testing.
type ErrorSkill struct {
	initError    error
	executeError error
}

func (p *ErrorSkill) Name() string {
	return "error-skill"
}

func (p *ErrorSkill) Version() string {
	return "1.0.0"
}

func (p *ErrorSkill) Init(config map[string]interface{}) error {
	return p.initError
}

func (p *ErrorSkill) Execute(input []byte) ([]byte, error) {
	if p.executeError != nil {
		return nil, p.executeError
	}

	response := SkillResponse{
		Success: false,
		Error:   "skill execution error",
	}

	return json.Marshal(response)
}

func TestSkillExecutorInterface(t *testing.T) {
	// Test that ExampleSkill implements SkillExecutor interface
	var _ SkillExecutor = (*ExampleSkill)(nil)

	skill := &ExampleSkill{
		name:    "test-skill",
		version: "1.0.0",
	}

	// Test Name()
	if skill.Name() != "test-skill" {
		t.Errorf("Expected name 'test-skill', got '%s'", skill.Name())
	}

	// Test Version()
	if skill.Version() != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", skill.Version())
	}

	// Test Init()
	err := skill.Init(map[string]interface{}{
		"timeout": 30.0,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test Init() with invalid config
	err = skill.Init(map[string]interface{}{
		"timeout": -1.0,
	})
	if err == nil {
		t.Error("Expected error for negative timeout")
	}

	// Test Execute()
	request := SkillRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response SkillResponse
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

func TestSkillExecutorRegistry(t *testing.T) {
	registry := NewSkillExecutorRegistry()

	// Test Register()
	skill1 := &ExampleSkill{name: "skill1", version: "1.0.0"}
	err := registry.Register(skill1, nil)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Test duplicate registration
	err = registry.Register(skill1, nil)
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}

	// Test Get()
	retrieved, err := registry.Get("skill1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.Name() != "skill1" {
		t.Errorf("Expected skill name 'skill1', got '%s'", retrieved.Name())
	}

	// Test Get() non-existent
	_, err = registry.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}

	// Test List()
	skill2 := &ExampleSkill{name: "skill2", version: "1.0.0"}
	registry.Register(skill2, nil)

	list := registry.List()
	if len(list) != 2 {
		t.Errorf("Expected 2 skills, got %d", len(list))
	}

	// Test Unregister()
	registry.Unregister("skill1")
	list = registry.List()
	if len(list) != 1 {
		t.Errorf("Expected 1 skill after unregister, got %d", len(list))
	}

	if list[0] != "skill2" {
		t.Errorf("Expected remaining skill 'skill2', got '%s'", list[0])
	}
}

func TestSkillContext(t *testing.T) {
	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	err := fs.MountLocal("/data", tmpDir, false)
	if err != nil {
		t.Fatalf("MountLocal failed: %v", err)
	}

	session, err := fs.NewSession("skill-session", []string{"/toolfs/data"})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	ctx := NewSkillContext(fs, session)

	// Test ReadFile()
	data, err := ctx.ReadFile("/toolfs/data/test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "Hello, ToolFS!" {
		t.Errorf("Expected 'Hello, ToolFS!', got '%s'", string(data))
	}

	// Test WriteFile()
	err = ctx.WriteFile("/toolfs/data/skill-test.txt", []byte("Skill test"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify write
	data, err = ctx.ReadFile("/toolfs/data/skill-test.txt")
	if err != nil {
		t.Fatalf("ReadFile after write failed: %v", err)
	}

	if string(data) != "Skill test" {
		t.Errorf("Expected 'Skill test', got '%s'", string(data))
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

func TestFileProcessorSkill(t *testing.T) {
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

	ctx := NewSkillContext(fs, session)
	skill := &FileProcessorSkill{context: ctx}

	// Test Init()
	err = skill.Init(map[string]interface{}{
		"allowed_paths": []interface{}{"/toolfs/data"},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test Execute - read_and_process (without allowed_paths restriction)
	skillNoRestriction := &FileProcessorSkill{context: ctx}
	skillNoRestriction.Init(nil) // No allowed_paths restriction

	request := SkillRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := skillNoRestriction.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response SkillResponse
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
	request = SkillRequest{
		Operation: "list_files",
		Path:      "/toolfs/data",
	}
	input, _ = json.Marshal(request)

	output, err = skill.Execute(input)
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
	request = SkillRequest{
		Operation: "invalid_operation",
	}
	input, _ = json.Marshal(request)

	_, err = skill.Execute(input)
	if err == nil {
		t.Error("Expected error for invalid operation")
	}

	// Test Execute - path not in allowed_paths
	// Create a new skill instance for this test to avoid state issues
	skill2 := &FileProcessorSkill{context: ctx}
	err = skill2.Init(map[string]interface{}{
		"allowed_paths": []interface{}{"/toolfs/data/subdir"},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	request = SkillRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ = json.Marshal(request)

	_, err = skill2.Execute(input)
	if err == nil {
		t.Error("Expected error for path not in allowed_paths")
	}
	if err != nil && !strings.Contains(err.Error(), "allowed_paths") {
		t.Errorf("Expected error about allowed_paths, got: %v", err)
	}
}

func TestSkillExecutorRegistryExecuteSkill(t *testing.T) {
	registry := NewSkillExecutorRegistry()

	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)
	session, _ := fs.NewSession("test-session", []string{"/toolfs/data"})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "test-skill", version: "1.0.0"}
	skill.Init(nil)

	registry.Register(skill, ctx)

	// Test ExecuteSkill()
	request := &SkillRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}

	response, err := registry.ExecuteSkill("test-skill", request)
	if err != nil {
		t.Fatalf("ExecuteSkill failed: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	result := response.Result.(map[string]interface{})
	if result["code"] != "test-skill" {
		t.Errorf("Expected skill 'test-skill', got '%v'", result["code"])
	}

	// Test ExecuteSkill - non-existent skill
	_, err = registry.ExecuteSkill("nonexistent", request)
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestSkillExecutorRegistryInitSkill(t *testing.T) {
	registry := NewSkillExecutorRegistry()

	skill := &ExampleSkill{name: "test-skill", version: "1.0.0"}
	registry.Register(skill, nil)

	// Test InitSkill()
	err := registry.InitSkill("test-skill", map[string]interface{}{
		"timeout": 30.0,
	})
	if err != nil {
		t.Fatalf("InitSkill failed: %v", err)
	}

	// Test InitSkill - non-existent skill
	err = registry.InitSkill("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}

	// Test InitSkill - invalid config
	err = registry.InitSkill("test-skill", map[string]interface{}{
		"timeout": -1.0,
	})
	if err == nil {
		t.Error("Expected error for invalid config")
	}
}

func TestSkillContextWithNilToolFS(t *testing.T) {
	ctx := &SkillContext{fs: nil, session: nil}

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

func TestSkillRequestResponseJSON(t *testing.T) {
	// Test SkillRequest JSON encoding/decoding
	request := SkillRequest{
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

	var decoded SkillRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if decoded.Operation != "read_file" {
		t.Errorf("Operation mismatch")
	}
	if decoded.Path != "/toolfs/data/test.txt" {
		t.Errorf("Path mismatch")
	}

	// Test SkillResponse JSON encoding/decoding
	response := SkillResponse{
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

	var decodedResp SkillResponse
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

func TestFileProcessorSkillWithoutContext(t *testing.T) {
	skill := &FileProcessorSkill{context: nil}

	skill.Init(nil)

	request := SkillRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	_, err := skill.Execute(input)
	if err == nil {
		t.Error("Expected error when skill context is nil")
	}
}

func TestErrorSkill(t *testing.T) {
	// Test Init error
	skill := &ErrorSkill{
		initError: errors.New("initialization failed"),
	}

	err := skill.Init(nil)
	if err == nil {
		t.Error("Expected init error")
	}

	// Test Execute error
	skill = &ErrorSkill{
		executeError: errors.New("execution failed"),
	}
	skill.Init(nil)

	input := []byte(`{"operation":"test"}`)
	_, err = skill.Execute(input)
	if err == nil {
		t.Error("Expected execute error")
	}

	// Test Execute with no error (returns error response)
	skill = &ErrorSkill{}
	skill.Init(nil)

	output, err := skill.Execute(input)
	if err != nil {
		t.Fatalf("Execute should not return error: %v", err)
	}

	var response SkillResponse
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
	instantiateFunc func([]byte, *SkillContext) (SkillExecutor, error)
}

func (m *MockWASMLoader) LoadWASM(path string) ([]byte, error) {
	if m.loadFunc != nil {
		return m.loadFunc(path)
	}
	return []byte("mock wasm bytes"), nil
}

func (m *MockWASMLoader) Instantiate(wasmBytes []byte, context *SkillContext) (SkillExecutor, error) {
	if m.instantiateFunc != nil {
		return m.instantiateFunc(wasmBytes, context)
	}
	// Return a mock skill
	return &ExampleSkill{name: "wasm-skill", version: "1.0.0"}, nil
}

func TestSkillExecutorManagerInjectSkill(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "test-skill", version: "1.0.0"}

	// Test InjectSkill
	err := pm.InjectSkill(skill, ctx, nil)
	if err != nil {
		t.Fatalf("InjectSkill failed: %v", err)
	}

	// Verify skill is loaded
	skills := pm.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}

	if skills[0] != "test-skill" {
		t.Errorf("Expected skill 'test-skill', got '%s'", skills[0])
	}

	// Test duplicate injection
	err = pm.InjectSkill(skill, ctx, nil)
	if err == nil {
		t.Error("Expected error for duplicate skill injection")
	}

	// Test nil skill
	err = pm.InjectSkill(nil, ctx, nil)
	if err == nil {
		t.Error("Expected error for nil skill")
	}

	// Test skill with empty name
	emptySkill := &ExampleSkill{name: "", version: "1.0.0"}
	err = pm.InjectSkill(emptySkill, ctx, nil)
	if err == nil {
		t.Error("Expected error for skill with empty name")
	}
}

func TestSkillExecutorManagerListExecutors(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	// Initially empty
	skills := pm.ListSkills()
	if len(skills) != 0 {
		t.Errorf("Expected 0 skills initially, got %d", len(skills))
	}

	// Inject multiple skills
	pm.InjectSkill(&ExampleSkill{name: "skill1", version: "1.0.0"}, ctx, nil)
	pm.InjectSkill(&ExampleSkill{name: "skill2", version: "1.0.0"}, ctx, nil)
	pm.InjectSkill(&ExampleSkill{name: "skill3", version: "1.0.0"}, ctx, nil)

	skills = pm.ListSkills()
	if len(skills) != 3 {
		t.Errorf("Expected 3 skills, got %d", len(skills))
	}

	// Verify all skills are listed
	skillMap := make(map[string]bool)
	for _, name := range skills {
		skillMap[name] = true
	}

	if !skillMap["skill1"] || !skillMap["skill2"] || !skillMap["skill3"] {
		t.Error("Expected all skills to be listed")
	}
}

func TestSkillExecutorManagerExecuteSkill(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "test-skill", version: "1.0.0"}
	pm.InjectSkill(skill, ctx, nil)

	// Test ExecuteSkill
	request := SkillRequest{
		Operation: "test",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := pm.ExecuteSkill("test-skill", input)
	if err != nil {
		t.Fatalf("ExecuteSkill failed: %v", err)
	}

	var response SkillResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	// Test ExecuteSkill - non-existent skill
	_, err = pm.ExecuteSkill("nonexistent", input)
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestSkillExecutorManagerExecuteSkillTimeout(t *testing.T) {
	pm := NewSkillExecutorManager()
	pm.SetTimeout(100 * time.Millisecond) // Short timeout

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	// Create a slow skill
	slowSkill := &SlowSkill{delay: 200 * time.Millisecond}
	pm.InjectSkill(slowSkill, ctx, nil)

	request := SkillRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	// Should timeout
	_, err := pm.ExecuteSkill("slow-skill", input)
	if err == nil {
		t.Error("Expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// SlowSkill is a skill that delays execution for testing timeouts.
type SlowSkill struct {
	delay time.Duration
}

func (p *SlowSkill) Name() string    { return "slow-skill" }
func (p *SlowSkill) Version() string { return "1.0.0" }
func (p *SlowSkill) Init(config map[string]interface{}) error {
	if delay, ok := config["delay"].(float64); ok {
		p.delay = time.Duration(delay) * time.Millisecond
	}
	return nil
}

func (p *SlowSkill) Execute(input []byte) ([]byte, error) {
	time.Sleep(p.delay)
	response := SkillResponse{Success: true, Result: "delayed result"}
	return json.Marshal(response)
}

func TestSkillExecutorManagerLoadSkill(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	// Test loading without WASM loader (should fail for .wasm files)
	err := pm.LoadSkill("test.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when loading WASM without loader")
	}
	if !strings.Contains(err.Error(), "WASM loader not configured") {
		t.Errorf("Expected WASM loader error, got: %v", err)
	}

	// Test loading with mock WASM loader
	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	err = pm.LoadSkill("test.wasm", ctx, nil)
	if err != nil {
		t.Fatalf("LoadSkill with WASM loader failed: %v", err)
	}

	// Verify skill is loaded
	skills := pm.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill after loading, got %d", len(skills))
	}

	// Test loading duplicate skill
	err = pm.LoadSkill("test.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error for duplicate skill loading")
	}

	// Test loading native skill (not yet implemented)
	err = pm.LoadSkill("test.so", ctx, nil)
	if err == nil {
		t.Error("Expected error for native skill (not implemented)")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("Expected 'not yet implemented' error, got: %v", err)
	}
}

func TestSkillExecutorManagerLoadSkillWithConfig(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	config := map[string]interface{}{
		"timeout":      60.0,
		"memory_limit": 1024 * 1024,
	}

	err := pm.LoadSkill("config-test.wasm", ctx, config)
	if err != nil {
		t.Fatalf("LoadSkill with config failed: %v", err)
	}

	// Verify config was passed
	info, err := pm.GetSkillInfo("wasm-skill")
	if err != nil {
		t.Fatalf("GetSkillInfo failed: %v", err)
	}

	if info.Config == nil {
		t.Error("Expected config to be set")
	}
}

func TestSkillExecutorManagerGetSkillInfo(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "info-skill", version: "1.0.0"}
	pm.InjectSkill(skill, ctx, map[string]interface{}{"test": "value"})

	// Test GetSkillInfo
	info, err := pm.GetSkillInfo("info-skill")
	if err != nil {
		t.Fatalf("GetSkillInfo failed: %v", err)
	}

	if info.Executor.Name() != "info-skill" {
		t.Errorf("Expected skill name 'info-skill', got '%s'", info.Executor.Name())
	}

	if info.Source != "injected" {
		t.Errorf("Expected source 'injected', got '%s'", info.Source)
	}

	if info.Context == nil {
		t.Error("Expected context to be set")
	}

	// Test GetSkillInfo - non-existent skill
	_, err = pm.GetSkillInfo("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestSkillExecutorManagerUnloadSkill(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "unload-skill", version: "1.0.0"}
	pm.InjectSkill(skill, ctx, nil)

	// Verify skill is loaded
	if len(pm.ListSkills()) != 1 {
		t.Error("Expected skill to be loaded")
	}

	// Test UnloadSkill
	err := pm.UnloadSkill("unload-skill")
	if err != nil {
		t.Fatalf("UnloadSkill failed: %v", err)
	}

	// Verify skill is unloaded
	if len(pm.ListSkills()) != 0 {
		t.Error("Expected skill to be unloaded")
	}

	// Test UnloadSkill - non-existent skill
	err = pm.UnloadSkill("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestSkillExecutorManagerSetSkillTimeout(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "timeout-skill", version: "1.0.0"}
	pm.InjectSkill(skill, ctx, nil)

	// Test SetSkillTimeout
	customTimeout := 60 * time.Second
	err := pm.SetSkillTimeout("timeout-skill", customTimeout)
	if err != nil {
		t.Fatalf("SetSkillTimeout failed: %v", err)
	}

	info, _ := pm.GetSkillInfo("timeout-skill")
	if info.Timeout != customTimeout {
		t.Errorf("Expected timeout %v, got %v", customTimeout, info.Timeout)
	}

	// Test SetSkillTimeout - non-existent skill
	err = pm.SetSkillTimeout("nonexistent", customTimeout)
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}

	// Test SetSkillTimeout - invalid timeout
	err = pm.SetSkillTimeout("timeout-skill", -1)
	if err == nil {
		t.Error("Expected error for invalid timeout")
	}
}

func TestSkillExecutorManagerSetSkillSandboxed(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	skill := &ExampleSkill{name: "sandbox-skill", version: "1.0.0"}
	pm.InjectSkill(skill, ctx, nil)

	// Test SetSkillSandboxed
	err := pm.SetSkillSandboxed("sandbox-skill", true)
	if err != nil {
		t.Fatalf("SetSkillSandboxed failed: %v", err)
	}

	info, _ := pm.GetSkillInfo("sandbox-skill")
	if !info.Sandboxed {
		t.Error("Expected skill to be sandboxed")
	}

	err = pm.SetSkillSandboxed("sandbox-skill", false)
	if err != nil {
		t.Fatalf("SetSkillSandboxed failed: %v", err)
	}

	info, _ = pm.GetSkillInfo("sandbox-skill")
	if info.Sandboxed {
		t.Error("Expected skill to not be sandboxed")
	}

	// Test SetSkillSandboxed - non-existent skill
	err = pm.SetSkillSandboxed("nonexistent", true)
	if err == nil {
		t.Error("Expected error for non-existent skill")
	}
}

func TestSkillExecutorManagerSetTimeout(t *testing.T) {
	pm := NewSkillExecutorManager()

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

func TestSkillExecutorManagerWASMLoader(t *testing.T) {
	pm := NewSkillExecutorManager()

	// Test SetWASMLoader
	mockLoader := &MockWASMLoader{}
	pm.SetWASMLoader(mockLoader)

	if pm.wasmLoader != mockLoader {
		t.Error("WASM loader not set correctly")
	}
}

func TestSkillExecutorManagerLoadSkillErrorHandling(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	// Test empty path
	err := pm.LoadSkill("", ctx, nil)
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

	err = pm.LoadSkill("nonexistent.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when WASM loader fails")
	}

	// Test WASM loader that fails to instantiate
	failingInstantiateLoader := &MockWASMLoader{
		instantiateFunc: func(wasmBytes []byte, context *SkillContext) (SkillExecutor, error) {
			return nil, errors.New("instantiation failed")
		},
	}
	pm.SetWASMLoader(failingInstantiateLoader)

	err = pm.LoadSkill("instantiate-fail.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when instantiation fails")
	}

	// Test skill that fails initialization
	initFailLoader := &MockWASMLoader{
		instantiateFunc: func(wasmBytes []byte, context *SkillContext) (SkillExecutor, error) {
			return &ErrorSkill{initError: errors.New("init failed")}, nil
		},
	}
	pm.SetWASMLoader(initFailLoader)

	err = pm.LoadSkill("init-fail.wasm", ctx, nil)
	if err == nil {
		t.Error("Expected error when skill initialization fails")
	}
}

func TestSkillExecutorManagerExecuteSkillWithCustomTimeout(t *testing.T) {
	pm := NewSkillExecutorManager()

	fs := NewToolFS("/toolfs")
	session, _ := fs.NewSession("test-session", []string{})
	ctx := NewSkillContext(fs, session)

	// Create skill with custom timeout
	slowSkill := &SlowSkill{delay: 200 * time.Millisecond}
	pm.InjectSkill(slowSkill, ctx, nil)

	// Set custom timeout for this skill
	pm.SetSkillTimeout("slow-skill", 300*time.Millisecond)

	request := SkillRequest{Operation: "test"}
	input, _ := json.Marshal(request)

	// Should not timeout with custom timeout
	output, err := pm.ExecuteSkill("slow-skill", input)
	if err != nil {
		t.Fatalf("ExecuteSkill should not timeout with custom timeout: %v", err)
	}

	var response SkillResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Error("Expected successful response")
	}
}

func TestSkillExecutorManagerIntegration(t *testing.T) {
	pm := NewSkillExecutorManager()
	pm.SetTimeout(5 * time.Second)

	fs := NewToolFS("/toolfs")
	tmpDir, cleanup := setupTestDir(t)
	defer cleanup()

	fs.MountLocal("/data", tmpDir, false)
	session, _ := fs.NewSession("integration-session", []string{"/toolfs/data"})
	ctx := NewSkillContext(fs, session)

	// Inject file processor skill (without path restriction for this test)
	fileProcessor := &FileProcessorSkill{context: ctx}
	pm.InjectSkill(fileProcessor, ctx, nil) // No path restriction

	// Execute skill
	request := &SkillRequest{
		Operation: "read_and_process",
		Path:      "/toolfs/data/test.txt",
	}
	input, _ := json.Marshal(request)

	output, err := pm.ExecuteSkill("file-processor", input)
	if err != nil {
		t.Fatalf("ExecuteSkill failed: %v", err)
	}

	var response SkillResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success, got error: %s", response.Error)
	}

	// List skills
	skills := pm.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}

	// Get skill info
	info, err := pm.GetSkillInfo("file-processor")
	if err != nil {
		t.Fatalf("GetSkillInfo failed: %v", err)
	}

	if info.Executor.Name() != "file-processor" {
		t.Errorf("Expected skill name 'file-processor', got '%s'", info.Executor.Name())
	}

	// Unload skill
	err = pm.UnloadSkill("file-processor")
	if err != nil {
		t.Fatalf("UnloadSkill failed: %v", err)
	}

	if len(pm.ListSkills()) != 0 {
		t.Error("Expected no skills after unload")
	}
}
