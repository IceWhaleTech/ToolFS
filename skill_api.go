package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SkillExecutor defines the interface that all code-based skills must implement.
// This is the core interface for skills that can execute code (WASM or native).
//
// Example usage:
//
//	type MySkill struct {}
//
//	func (s *MySkill) Name() string { return "my-skill" }
//	func (s *MySkill) Version() string { return "1.0.0" }
//	func (s *MySkill) Init(config map[string]interface{}) error { return nil }
//	func (s *MySkill) Execute(input []byte) ([]byte, error) {
//		return []byte("result"), nil
//	}
type SkillExecutor interface {
	// Name returns the unique name of the skill.
	Name() string

	// Version returns the semantic version of the skill.
	Version() string

	// Init initializes the skill with the provided configuration.
	Init(config map[string]interface{}) error

	// Execute runs the skill's core functionality.
	Execute(input []byte) ([]byte, error)
}

// SkillDocumentProvider is an optional interface that skills can implement
// to provide SKILL.md documentation.
type SkillDocumentProvider interface {
	// GetSkillDocument returns the SKILL.md content for this skill.
	GetSkillDocument() string
}

// SkillContext provides access to ToolFS functionality within skills.
type SkillContext struct {
	fs      *ToolFS
	session *Session
}

// NewSkillContext creates a new skill context with ToolFS and session access.
func NewSkillContext(fs *ToolFS, session *Session) *SkillContext {
	return &SkillContext{
		fs:      fs,
		session: session,
	}
}

// ReadFile reads a file from ToolFS using the skill's session.
func (ctx *SkillContext) ReadFile(path string) ([]byte, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.ReadFileWithSession(path, ctx.session)
}

// WriteFile writes data to a file in ToolFS using the skill's session.
func (ctx *SkillContext) WriteFile(path string, data []byte) error {
	if ctx.fs == nil {
		return errors.New("ToolFS instance not available")
	}
	return ctx.fs.WriteFileWithSession(path, data, ctx.session)
}

// ListDir lists directory contents from ToolFS using the skill's session.
func (ctx *SkillContext) ListDir(path string) ([]string, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.ListDirWithSession(path, ctx.session)
}

// Stat gets file metadata from ToolFS using the skill's session.
func (ctx *SkillContext) Stat(path string) (*FileInfo, error) {
	if ctx.fs == nil {
		return nil, errors.New("ToolFS instance not available")
	}
	return ctx.fs.StatWithSession(path, ctx.session)
}

// SkillRequest represents a request to a skill.
type SkillRequest struct {
	Operation string                 `json:"operation"`
	Path      string                 `json:"path,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Options   map[string]interface{} `json:"options,omitempty"`
}

// SkillResponse represents a response from a skill.
type SkillResponse struct {
	Success  bool                   `json:"success"`
	Result   interface{}            `json:"result,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ManagedSkill wraps a skill with management metadata.
type ManagedSkill struct {
	Executor  SkillExecutor
	Context   *SkillContext
	Source    string // Source: "injected" or file path
	LoadedAt  time.Time
	Config    map[string]interface{}
	Timeout   time.Duration // Per-skill timeout
	Sandboxed bool          // Whether skill runs in sandbox
}

// SkillType represents the type of skill
type SkillType string

const (
	SkillTypeFilesystem SkillType = "filesystem" // Filesystem-based skill with SKILL.md
	SkillTypeCode     SkillType = "code"     // Skill-based skill (WASM or native)
	SkillTypeBuiltin    SkillType = "builtin"    // Built-in skill
)

// Skill represents a unified skill that can be filesystem-based, skill-based, or builtin
// This unifies the concept of "filesystem skills" and "code skills" into one structure
type Skill struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Type        SkillType              `json:"type"`
	Path        string                 `json:"path"`      // Virtual path in ToolFS (e.g., /toolfs/skills/my-skill)
	BasePath    string                 `json:"base_path"` // Physical base path for filesystem skills
	Metadata    map[string]interface{} `json:"metadata"`
	Document    *SkillDocument         `json:"document,omitempty"`

	// Filesystem-based skill paths (only for SkillTypeFilesystem)
	SkillMdPath    string `json:"skill_md_path,omitempty"`   // Path to SKILL.md
	ReferencesPath string `json:"references_path,omitempty"` // Path to references/
	ScriptsPath    string `json:"scripts_path,omitempty"`    // Path to scripts/

	// Code-based skill (only for SkillTypeCode)
	Executor SkillExecutor `json:"-"` // The skill executor instance (not serialized)
}

// SkillRegistry manages skill registrations and provides unified API access
type SkillRegistry struct {
	skills      map[string]*Skill     // skill name -> skill
	pathToSkill map[string]string     // virtual path -> skill name
	docManager  *SkillDocumentManager // Document manager
}

// NewSkillRegistry creates a new skill registry
func NewSkillRegistry(docManager *SkillDocumentManager) *SkillRegistry {
	if docManager == nil {
		docManager = NewSkillDocumentManager()
	}
	return &SkillRegistry{
		skills:      make(map[string]*Skill),
		pathToSkill: make(map[string]string),
		docManager:  docManager,
	}
}

// RegisterSkill registers a skill with the registry
func (sr *SkillRegistry) RegisterSkill(skill *Skill) error {
	if skill.Name == "" {
		return errors.New("skill name cannot be empty")
	}

	// Check if skill already exists
	if _, exists := sr.skills[skill.Name]; exists {
		return fmt.Errorf("skill '%s' is already registered", skill.Name)
	}

	// Register the skill
	sr.skills[skill.Name] = skill

	// Map path to skill name if path is provided
	if skill.Path != "" {
		sr.pathToSkill[normalizeVirtualPath(skill.Path)] = skill.Name
	}

	return nil
}

// RegisterFilesystemSkill registers a skill from a filesystem directory
// The directory should contain:
// - SKILL.md (required)
// - references/ (optional)
// - scripts/ (optional)
func (sr *SkillRegistry) RegisterFilesystemSkill(basePath string) (*Skill, error) {
	// Normalize the base path
	basePath = filepath.Clean(basePath)

	// Check if base path exists
	if _, err := os.Stat(basePath); err != nil {
		return nil, fmt.Errorf("skill base path does not exist: %w", err)
	}

	// Look for SKILL.md
	skillMdPath := filepath.Join(basePath, "SKILL.md")
	if _, err := os.Stat(skillMdPath); err != nil {
		return nil, fmt.Errorf("SKILL.md not found in %s: %w", basePath, err)
	}

	// Read SKILL.md content
	content, err := os.ReadFile(skillMdPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SKILL.md: %w", err)
	}

	// Parse the skill document
	doc, err := sr.docManager.parseSkillDocument(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SKILL.md: %w", err)
	}

	// Use directory name as default skill name if not specified in document
	if doc.Name == "" {
		doc.Name = filepath.Base(basePath)
	}

	// Check for optional directories
	referencesPath := filepath.Join(basePath, "references")
	scriptsPath := filepath.Join(basePath, "scripts")

	// Create skill
	skill := &Skill{
		Name:           doc.Name,
		Description:    doc.Description,
		Type:           SkillTypeFilesystem,
		Path:           fmt.Sprintf("/toolfs/skills/%s", doc.Name),
		BasePath:       basePath,
		SkillMdPath:    skillMdPath,
		ReferencesPath: referencesPath,
		ScriptsPath:    scriptsPath,
		Metadata:       doc.Metadata,
		Document:       doc,
	}

	// Check if references and scripts directories exist
	if _, err := os.Stat(referencesPath); err == nil {
		skill.Metadata["has_references"] = true
	}
	if _, err := os.Stat(scriptsPath); err == nil {
		skill.Metadata["has_scripts"] = true
	}

	// Register with document manager
	doc.Path = basePath
	sr.docManager.documents[doc.Name] = doc

	// Register the skill
	if err := sr.RegisterSkill(skill); err != nil {
		return nil, err
	}

	return skill, nil
}

// RegisterCodeSkill registers a code-based skill (executor)
// This is the unified way to register what was previously called "skills"
func (sr *SkillRegistry) RegisterCodeSkill(executor SkillExecutor, mountPath string) (*Skill, error) {
	// Register skill with document manager
	if err := sr.docManager.RegisterExecutor(executor); err != nil {
		return nil, err
	}

	// Get document (may not exist if executor doesn't provide one)
	doc, err := sr.docManager.GetDocument(executor.Name())
	if err != nil {
		// Executor doesn't provide a skill document, create a basic one
		doc = &SkillDocument{
			Name:        executor.Name(),
			Description: fmt.Sprintf("Skill: %s v%s", executor.Name(), executor.Version()),
			Content:     "",
			Metadata:    make(map[string]interface{}),
			Path:        fmt.Sprintf("executor:%s", executor.Name()),
		}
	}

	// Add version to metadata
	if doc.Metadata == nil {
		doc.Metadata = make(map[string]interface{})
	}
	doc.Metadata["version"] = executor.Version()
	doc.Metadata["skill_type"] = "code"

	// Create skill
	skill := &Skill{
		Name:        executor.Name(),
		Description: doc.Description,
		Type:        SkillTypeCode,
		Path:        mountPath,
		Metadata:    doc.Metadata,
		Document:    doc,
		Executor:    executor,
	}

	// Register the skill
	if err := sr.RegisterSkill(skill); err != nil {
		return nil, err
	}

	return skill, nil
}

// RegisterBuiltinSkill registers a built-in skill from embedded filesystem
func (sr *SkillRegistry) RegisterBuiltinSkill(name, path string) (*Skill, error) {
	// Get document from document manager
	doc, err := sr.docManager.GetDocument(name)
	if err != nil {
		return nil, fmt.Errorf("builtin skill document not found: %w", err)
	}

	skill := &Skill{
		Name:        doc.Name,
		Description: doc.Description,
		Type:        SkillTypeBuiltin,
		Path:        path,
		Metadata:    doc.Metadata,
		Document:    doc,
	}

	if err := sr.RegisterSkill(skill); err != nil {
		return nil, err
	}

	return skill, nil
}

// UnregisterSkill removes a skill from the registry
func (sr *SkillRegistry) UnregisterSkill(name string) error {
	skill, exists := sr.skills[name]
	if !exists {
		return fmt.Errorf("skill '%s' not found", name)
	}

	// Remove path mapping
	if skill.Path != "" {
		delete(sr.pathToSkill, normalizeVirtualPath(skill.Path))
	}

	// Remove from skills map
	delete(sr.skills, name)

	return nil
}

// GetSkill retrieves a skill by name
func (sr *SkillRegistry) GetSkill(name string) (*Skill, error) {
	skill, exists := sr.skills[name]
	if !exists {
		return nil, fmt.Errorf("skill '%s' not found", name)
	}
	return skill, nil
}

// GetSkillByPath retrieves a skill by its virtual path
func (sr *SkillRegistry) GetSkillByPath(path string) (*Skill, error) {
	path = normalizeVirtualPath(path)
	name, exists := sr.pathToSkill[path]
	if !exists {
		return nil, fmt.Errorf("no skill registered at path '%s'", path)
	}
	return sr.GetSkill(name)
}

// ListSkills returns all registered skills
func (sr *SkillRegistry) ListSkills() []*Skill {
	skills := make([]*Skill, 0, len(sr.skills))
	for _, skill := range sr.skills {
		skills = append(skills, skill)
	}
	return skills
}

// ListSkillsByType returns skills filtered by type
func (sr *SkillRegistry) ListSkillsByType(skillType SkillType) []*Skill {
	skills := make([]*Skill, 0)
	for _, skill := range sr.skills {
		if skill.Type == skillType {
			skills = append(skills, skill)
		}
	}
	return skills
}

// ListSkillNames returns all registered skill names
func (sr *SkillRegistry) ListSkillNames() []string {
	names := make([]string, 0, len(sr.skills))
	for name := range sr.skills {
		names = append(names, name)
	}
	return names
}

// LoadSkillsFromDirectory loads all skills from a directory
// Each subdirectory should contain a SKILL.md file
func (sr *SkillRegistry) LoadSkillsFromDirectory(dirPath string) ([]string, error) {
	dirPath = filepath.Clean(dirPath)

	// Check if directory exists
	if _, err := os.Stat(dirPath); err != nil {
		return nil, fmt.Errorf("skills directory does not exist: %w", err)
	}

	var loadedSkills []string
	var loadErrors []error

	// Walk through the directory
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory
		if path == dirPath {
			return nil
		}

		// Only process directories at the first level
		if d.IsDir() {
			relPath, _ := filepath.Rel(dirPath, path)
			// Skip nested directories (only process first-level subdirectories)
			if strings.Contains(relPath, string(filepath.Separator)) {
				return filepath.SkipDir
			}

			// Try to register this as a skill
			skill, err := sr.RegisterFilesystemSkill(path)
			if err != nil {
				loadErrors = append(loadErrors, fmt.Errorf("failed to load skill from %s: %w", path, err))
			} else {
				loadedSkills = append(loadedSkills, skill.Name)
			}

			// Skip subdirectories
			return filepath.SkipDir
		}

		return nil
	})
	if err != nil {
		return loadedSkills, fmt.Errorf("error walking skills directory: %w", err)
	}

	// Return error if any skills failed to load
	if len(loadErrors) > 0 {
		errMsg := "some skills failed to load:\n"
		for _, e := range loadErrors {
			errMsg += fmt.Sprintf("  - %s\n", e.Error())
		}
		return loadedSkills, errors.New(errMsg)
	}

	return loadedSkills, nil
}

// GetSkillDocument retrieves the full skill document for a skill
func (sr *SkillRegistry) GetSkillDocument(name string) (*SkillDocument, error) {
	skill, err := sr.GetSkill(name)
	if err != nil {
		return nil, err
	}

	if skill.Document != nil {
		return skill.Document, nil
	}

	// Try to get from document manager
	return sr.docManager.GetDocument(name)
}

// ExportSkillsJSON exports all skills as JSON
func (sr *SkillRegistry) ExportSkillsJSON() ([]byte, error) {
	skills := sr.ListSkills()
	return json.MarshalIndent(skills, "", "  ")
}

// ImportSkillsJSON imports skills from JSON (for configuration)
// Only filesystem skills can be imported from JSON
func (sr *SkillRegistry) ImportSkillsJSON(data []byte) error {
	var skills []*Skill
	if err := json.Unmarshal(data, &skills); err != nil {
		return fmt.Errorf("failed to parse skills JSON: %w", err)
	}

	for _, skill := range skills {
		// Only import filesystem skills from JSON
		// Skills must be registered programmatically
		if skill.Type == SkillTypeFilesystem && skill.BasePath != "" {
			_, err := sr.RegisterFilesystemSkill(skill.BasePath)
			if err != nil {
				return fmt.Errorf("failed to register skill '%s': %w", skill.Name, err)
			}
		}
	}

	return nil
}

// ToolFS integration methods

// AddSkillRegistry adds a skill registry to ToolFS
func (fs *ToolFS) AddSkillRegistry(registry *SkillRegistry) {
	if registry.docManager == nil {
		registry.docManager = fs.skillDocManager
	}
	fs.skillRegistry = registry
}

// GetSkillRegistry returns the skill registry
func (fs *ToolFS) GetSkillRegistry() *SkillRegistry {
	return fs.skillRegistry
}

// RegisterSkillWithSkillDocs registers an executor with the skill document manager
func (fs *ToolFS) RegisterSkillWithSkillDocs(executor SkillExecutor) error {
	return fs.skillDocManager.RegisterExecutor(executor)
}

// RegisterSkill is a convenience method to register a skill with ToolFS
func (fs *ToolFS) RegisterSkill(skill *Skill) error {
	if fs.skillRegistry == nil {
		fs.skillRegistry = NewSkillRegistry(fs.skillDocManager)
	}
	return fs.skillRegistry.RegisterSkill(skill)
}

// RegisterFilesystemSkill is a convenience method to register a filesystem skill
func (fs *ToolFS) RegisterFilesystemSkill(basePath string) (*Skill, error) {
	if fs.skillRegistry == nil {
		fs.skillRegistry = NewSkillRegistry(fs.skillDocManager)
	}
	return fs.skillRegistry.RegisterFilesystemSkill(basePath)
}

// RegisterCodeSkill is a convenience method to register a skill as a skill
// RegisterCodeSkill is a convenience method to register a code-based skill with ToolFS
func (fs *ToolFS) RegisterCodeSkill(executor SkillExecutor, mountPath string) (*Skill, error) {
	if fs.skillRegistry == nil {
		fs.skillRegistry = NewSkillRegistry(fs.skillDocManager)
	}
	return fs.skillRegistry.RegisterCodeSkill(executor, mountPath)
}

// LoadSkillsFromDirectory is a convenience method to load skills from a directory
func (fs *ToolFS) LoadSkillsFromDirectory(dirPath string) ([]string, error) {
	if fs.skillRegistry == nil {
		fs.skillRegistry = NewSkillRegistry(fs.skillDocManager)
	}
	return fs.skillRegistry.LoadSkillsFromDirectory(dirPath)
}

// ListSkills is a convenience method to list all registered skills
func (fs *ToolFS) ListSkills() []*Skill {
	if fs.skillRegistry == nil {
		return []*Skill{}
	}
	return fs.skillRegistry.ListSkills()
}

// ListSkillsByType is a convenience method to list skills by type
func (fs *ToolFS) ListSkillsByType(skillType SkillType) []*Skill {
	if fs.skillRegistry == nil {
		return []*Skill{}
	}
	return fs.skillRegistry.ListSkillsByType(skillType)
}

// GetSkill is a convenience method to get a skill by name
func (fs *ToolFS) GetSkill(name string) (*Skill, error) {
	if fs.skillRegistry == nil {
		return nil, errors.New("skill registry not initialized")
	}
	return fs.skillRegistry.GetSkill(name)
}

// ExecuteSkill executes a skill with the given input
// This provides a unified interface for executing both skill-based and filesystem-based skills
func (sr *SkillRegistry) ExecuteSkill(name string, input []byte, session *Session) ([]byte, error) {
	skill, err := sr.GetSkill(name)
	if err != nil {
		return nil, err
	}

	switch skill.Type {
	case SkillTypeCode:
		// Execute code-based skill
		if skill.Executor == nil {
			return nil, fmt.Errorf("skill '%s' has no executor", name)
		}
		return skill.Executor.Execute(input)

	case SkillTypeFilesystem:
		// For filesystem skills, we could execute scripts or return documentation
		// This is a placeholder - actual implementation depends on requirements
		return nil, fmt.Errorf("filesystem skill execution not yet implemented for '%s'", name)

	case SkillTypeBuiltin:
		// Builtin skills would have their own execution logic
		return nil, fmt.Errorf("builtin skill execution not yet implemented for '%s'", name)

	default:
		return nil, fmt.Errorf("unknown skill type: %s", skill.Type)
	}
}

// ExecuteSkill is a convenience method to execute a skill
func (fs *ToolFS) ExecuteSkill(name string, input []byte, session *Session) ([]byte, error) {
	if fs.skillRegistry == nil {
		return nil, errors.New("skill registry not initialized")
	}
	return fs.skillRegistry.ExecuteSkill(name, input, session)
}

// LoadSkill loads a skill from a file and registers it as a skill
// This integrates WASM skill loading into the skill system
func (sr *SkillRegistry) LoadSkill(skillPath, mountPath string, context *SkillContext, config map[string]interface{}) (*Skill, error) {
	// This would use a WASM loader or native skill loader
	// For now, return an error indicating this needs to be implemented
	return nil, fmt.Errorf("skill loading from file not yet implemented")
}

// LoadSkill is a convenience method to load a skill skill from a file
func (fs *ToolFS) LoadSkill(skillPath, mountPath string, config map[string]interface{}) (*Skill, error) {
	if fs.skillRegistry == nil {
		fs.skillRegistry = NewSkillRegistry(fs.skillDocManager)
	}

	// Create skill context
	session, _ := fs.NewSession("__skill_loader__", []string{})
	context := NewSkillContext(fs, session)

	return fs.skillRegistry.LoadSkill(skillPath, mountPath, context, config)
}

// MountSkill mounts a skill to a ToolFS path
// This integrates skill mounting with the existing mount system
func (fs *ToolFS) MountSkill(skill *Skill) error {
	if skill.Type == SkillTypeCode && skill.Executor != nil {
		// Mount code-based skill using existing skill mount mechanism
		return fs.MountSkillExecutor(skill.Path, skill.Name)
	}

	// For filesystem skills, we could mount the base path
	if skill.Type == SkillTypeFilesystem && skill.BasePath != "" {
		return fs.MountLocal(skill.Path, skill.BasePath, true)
	}

	return nil
}

// UnmountSkill unmounts a skill from ToolFS
func (fs *ToolFS) UnmountSkill(skillName string) error {
	skill, err := fs.GetSkill(skillName)
	if err != nil {
		return err
	}

	if skill.Type == SkillTypeCode {
		return fs.UnmountSkillExecutor(skill.Path)
	}

	// For filesystem skills, we would need to implement unmount logic
	return nil
}

// InitializeSkill initializes a skill with configuration
// This calls the Init method for skill skills
func (sr *SkillRegistry) InitializeSkill(name string, config map[string]interface{}) error {
	skill, err := sr.GetSkill(name)
	if err != nil {
		return err
	}

	if skill.Type == SkillTypeCode && skill.Executor != nil {
		return skill.Executor.Init(config)
	}

	// For other skill types, initialization might involve different logic
	return nil
}

// InitializeSkill is a convenience method to initialize a skill
func (fs *ToolFS) InitializeSkill(name string, config map[string]interface{}) error {
	if fs.skillRegistry == nil {
		return errors.New("skill registry not initialized")
	}
	return fs.skillRegistry.InitializeSkill(name, config)
}

// =============================================

// =============================================
// Skill Executor Registry (Internal API)
// =============================================

// SkillExecutorRegistry manages registered executors.
type SkillExecutorRegistry struct {
	executors map[string]SkillExecutor
	contexts  map[string]*SkillContext
}

// NewSkillExecutorRegistry creates a new executor registry.
func NewSkillExecutorRegistry() *SkillExecutorRegistry {
	return &SkillExecutorRegistry{
		executors: make(map[string]SkillExecutor),
		contexts:  make(map[string]*SkillContext),
	}
}

// Register registers a skill with the registry.
func (r *SkillExecutorRegistry) Register(skill SkillExecutor, context *SkillContext) error {
	name := skill.Name()
	if _, exists := r.executors[name]; exists {
		return fmt.Errorf("skill '%s' is already registered", name)
	}

	r.executors[name] = skill
	if context != nil {
		r.contexts[name] = context
	}

	return nil
}

// Get retrieves a skill by name.
func (r *SkillExecutorRegistry) Get(name string) (SkillExecutor, error) {
	skill, exists := r.executors[name]
	if !exists {
		return nil, fmt.Errorf("skill '%s' not found", name)
	}
	return skill, nil
}

// GetContext retrieves the context for a skill.
func (r *SkillExecutorRegistry) GetContext(name string) (*SkillContext, error) {
	context, exists := r.contexts[name]
	if !exists {
		return nil, fmt.Errorf("context for skill '%s' not found", name)
	}
	return context, nil
}

// List returns all registered skill names.
func (r *SkillExecutorRegistry) List() []string {
	names := make([]string, 0, len(r.executors))
	for name := range r.executors {
		names = append(names, name)
	}
	return names
}

// Unregister removes a skill from the registry.
func (r *SkillExecutorRegistry) Unregister(name string) {
	delete(r.executors, name)
	delete(r.contexts, name)
}

// ExecuteSkill executes a skill with the given input.
func (r *SkillExecutorRegistry) ExecuteSkill(name string, request *SkillRequest) (*SkillResponse, error) {
	skill, err := r.Get(name)
	if err != nil {
		return &SkillResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	input, err := json.Marshal(request)
	if err != nil {
		return &SkillResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to encode request: %v", err),
		}, err
	}

	output, err := skill.Execute(input)
	if err != nil {
		return &SkillResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	var response SkillResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return &SkillResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, err
	}

	return &response, nil
}

// InitSkill initializes a skill with the given configuration.
func (r *SkillExecutorRegistry) InitSkill(name string, config map[string]interface{}) error {
	skill, err := r.Get(name)
	if err != nil {
		return err
	}
	return skill.Init(config)
}

// AddSkillExecutorRegistry adds executor registry support to ToolFS.
func (fs *ToolFS) AddSkillExecutorRegistry(registry *SkillExecutorRegistry) {
	if registry == nil {
		return
	}
	if fs.executorManager != nil {
		fs.executorManager.registry = registry
	}
	fs.executorRegistry = registry
}

// GetSkillExecutorRegistry returns the executor registry associated with this ToolFS instance.
func (fs *ToolFS) GetSkillExecutorRegistry() *SkillExecutorRegistry {
	if fs.executorManager != nil && fs.executorManager.registry != nil {
		return fs.executorManager.registry
	}
	return fs.executorRegistry
}

// =============================================
// WASM Skill Support
// =============================================

// WASMSkillRunner is an interface for running WASM-compiled skills.
type WASMSkillRunner interface {
	Load(wasmBytes []byte) error
	Call(function string, input []byte) ([]byte, error)
	Close() error
}

// WASMSkillLoader defines interface for loading WASM skills.
type WASMSkillLoader interface {
	LoadWASM(path string) ([]byte, error)
	Instantiate(wasmBytes []byte, context *SkillContext) (SkillExecutor, error)
}

// =============================================
// Skill Manager (Legacy API)
// =============================================

// SkillExecutorManager manages skill lifecycle, loading, and execution.
type SkillExecutorManager struct {
	registry   *SkillExecutorRegistry
	executors  map[string]*ManagedSkill
	wasmLoader WASMSkillLoader
	timeout    time.Duration
}

// NewSkillExecutorManager creates a new SkillExecutorManager with default settings.
func NewSkillExecutorManager() *SkillExecutorManager {
	return &SkillExecutorManager{
		registry:  NewSkillExecutorRegistry(),
		executors: make(map[string]*ManagedSkill),
		timeout:   30 * time.Second,
	}
}

// SetTimeout sets the default execution timeout for executors.
func (pm *SkillExecutorManager) SetTimeout(timeout time.Duration) {
	pm.timeout = timeout
}

// SetWASMLoader sets the WASM loader for loading WASM executors.
func (pm *SkillExecutorManager) SetWASMLoader(loader WASMSkillLoader) {
	pm.wasmLoader = loader
}

// LoadSkill loads a executor from a file path.
func (pm *SkillExecutorManager) LoadSkill(path string, context *SkillContext, config map[string]interface{}) error {
	if path == "" {
		return errors.New("executor path cannot be empty")
	}

	if _, exists := pm.executors[path]; exists {
		return fmt.Errorf("executor already loaded from path: %s", path)
	}

	var executor SkillExecutor
	var wasmBytes []byte

	if strings.HasSuffix(strings.ToLower(path), ".wasm") {
		if pm.wasmLoader == nil {
			return errors.New("WASM loader not configured, cannot load WASM executor")
		}

		var err error
		wasmBytes, err = pm.wasmLoader.LoadWASM(path)
		if err != nil {
			return fmt.Errorf("failed to load WASM executor: %w", err)
		}

		executor, err = pm.wasmLoader.Instantiate(wasmBytes, context)
		if err != nil {
			return fmt.Errorf("failed to instantiate WASM executor: %w", err)
		}
	} else {
		return errors.New("native Go executor loading not yet implemented, use InjectSkill instead")
	}

	if config == nil {
		config = make(map[string]interface{})
	}

	if err := executor.Init(config); err != nil {
		return fmt.Errorf("executor initialization failed: %w", err)
	}

	managed := &ManagedSkill{
		Executor:  executor,
		Context:   context,
		Source:    path,
		LoadedAt:  time.Now(),
		Config:    config,
		Timeout:   pm.timeout,
		Sandboxed: true,
	}

	pm.executors[executor.Name()] = managed

	if err := pm.registry.Register(executor, context); err != nil {
		delete(pm.executors, executor.Name())
		return fmt.Errorf("failed to register executor: %w", err)
	}

	return nil
}

// InjectSkill injects a executor directly into the ToolFS runtime.
func (pm *SkillExecutorManager) InjectSkill(executor SkillExecutor, context *SkillContext, config map[string]interface{}) error {
	if executor == nil {
		return errors.New("executor cannot be nil")
	}

	name := executor.Name()
	if name == "" {
		return errors.New("executor name cannot be empty")
	}

	if _, exists := pm.executors[name]; exists {
		return fmt.Errorf("executor '%s' is already loaded", name)
	}

	if config == nil {
		config = make(map[string]interface{})
	}

	if err := executor.Init(config); err != nil {
		return fmt.Errorf("executor initialization failed: %w", err)
	}

	managed := &ManagedSkill{
		Executor:  executor,
		Context:   context,
		Source:    "injected",
		LoadedAt:  time.Now(),
		Config:    config,
		Timeout:   pm.timeout,
		Sandboxed: false,
	}

	pm.executors[name] = managed

	if err := pm.registry.Register(executor, context); err != nil {
		delete(pm.executors, name)
		return fmt.Errorf("failed to register executor: %w", err)
	}

	return nil
}

// ListSkills returns a list of all loaded executor names.
func (pm *SkillExecutorManager) ListSkills() []string {
	names := make([]string, 0, len(pm.executors))
	for name := range pm.executors {
		names = append(names, name)
	}
	return names
}

// GetSkillInfo returns information about a loaded executor.
func (pm *SkillExecutorManager) GetSkillInfo(name string) (*ManagedSkill, error) {
	managed, exists := pm.executors[name]
	if !exists {
		return nil, fmt.Errorf("executor '%s' not found", name)
	}
	return managed, nil
}

// ExecuteSkill executes a executor with the given input, respecting timeout.
func (pm *SkillExecutorManager) ExecuteSkill(name string, input []byte) ([]byte, error) {
	managed, exists := pm.executors[name]
	if !exists {
		return nil, fmt.Errorf("executor '%s' not found", name)
	}

	timeout := managed.Timeout
	if timeout == 0 {
		timeout = pm.timeout
	}

	resultChan := make(chan executeResult, 1)

	go func() {
		output, err := managed.Executor.Execute(input)
		resultChan <- executeResult{output: output, err: err}
	}()

	select {
	case result := <-resultChan:
		if result.err != nil {
			return nil, result.err
		}
		return result.output, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("executor execution timeout after %v", timeout)
	}
}

type executeResult struct {
	output []byte
	err    error
}

// UnloadSkill removes a executor from the manager.
func (pm *SkillExecutorManager) UnloadSkill(name string) error {
	_, exists := pm.executors[name]
	if !exists {
		return fmt.Errorf("executor '%s' not found", name)
	}

	pm.registry.Unregister(name)
	delete(pm.executors, name)

	return nil
}

// SetSkillTimeout sets a custom timeout for a specific executor.
func (pm *SkillExecutorManager) SetSkillTimeout(name string, timeout time.Duration) error {
	managed, exists := pm.executors[name]
	if !exists {
		return fmt.Errorf("executor '%s' not found", name)
	}

	if timeout <= 0 {
		return errors.New("timeout must be positive")
	}

	managed.Timeout = timeout
	return nil
}

// SetSkillSandboxed sets whether a executor runs in sandbox mode.
func (pm *SkillExecutorManager) SetSkillSandboxed(name string, sandboxed bool) error {
	managed, exists := pm.executors[name]
	if !exists {
		return fmt.Errorf("executor '%s' not found", name)
	}

	managed.Sandboxed = sandboxed
	return nil
}
