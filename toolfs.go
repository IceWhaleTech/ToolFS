package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileInfo represents file metadata
type FileInfo struct {
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Mount represents a mounted directory with its permissions
type Mount struct {
	LocalPath string
	ReadOnly  bool
}

// MemoryEntry represents a memory entry with content and metadata
type MemoryEntry struct {
	ID        string                 `json:"id"`
	Content   string                 `json:"content"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// RAGResult represents a single RAG search result
type RAGResult struct {
	ID       string                 `json:"id"`
	Content  string                 `json:"content"`
	Score    float64                `json:"score"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// RAGSearchResults represents RAG search results
type RAGSearchResults struct {
	Query   string      `json:"query"`
	TopK    int         `json:"top_k"`
	Results []RAGResult `json:"results"`
}

// MemoryStore defines the interface for memory storage
type MemoryStore interface {
	Get(id string) (*MemoryEntry, error)
	Set(id string, content string, metadata map[string]interface{}) error
	List() ([]string, error)
}

// RAGStore defines the interface for RAG storage and search
type RAGStore interface {
	Search(query string, topK int) ([]RAGResult, error)
}

// AuditLogEntry represents a single audit log entry
type AuditLogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	SessionID    string    `json:"session_id"`
	Operation    string    `json:"operation"` // "ReadFile", "WriteFile", "ListDir", "Stat"
	Path         string    `json:"path"`
	Success      bool      `json:"success"`
	Error        string    `json:"error,omitempty"`
	BytesRead    int64     `json:"bytes_read,omitempty"`
	BytesWritten int64     `json:"bytes_written,omitempty"`
	AccessDenied bool      `json:"access_denied,omitempty"`
}

// AuditLogger defines the interface for audit logging
type AuditLogger interface {
	Log(entry AuditLogEntry) error
}

// StdoutAuditLogger logs audit entries to stdout as JSON
type StdoutAuditLogger struct{}

// Log writes an audit entry to stdout as JSON
func (l *StdoutAuditLogger) Log(entry AuditLogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// Session represents an isolated LLM session with access restrictions
type Session struct {
	ID               string
	CreatedAt        time.Time
	AllowedPaths     []string // List of allowed path prefixes
	AuditLogger      AuditLogger
	CommandValidator CommandValidator // Optional command validator
}

// NewSession creates a new session with the given ID and allowed paths
func NewSession(id string, allowedPaths []string) *Session {
	return &Session{
		ID:           id,
		CreatedAt:    time.Now(),
		AllowedPaths: allowedPaths,
		AuditLogger:  &StdoutAuditLogger{},
	}
}

// SetAuditLogger sets a custom audit logger for the session
func (s *Session) SetAuditLogger(logger AuditLogger) {
	s.AuditLogger = logger
}

// SetCommandValidator sets a command validator for the session
func (s *Session) SetCommandValidator(validator CommandValidator) {
	s.CommandValidator = validator
}

// ValidateCommand checks if a command is allowed for this session
func (s *Session) ValidateCommand(command string, args []string) (bool, string) {
	if s.CommandValidator == nil {
		return true, "" // No filter means all commands allowed
	}
	return s.CommandValidator.IsCommandAllowed(command, args)
}

// IsPathAllowed checks if a path is allowed for this session
func (s *Session) IsPathAllowed(path string) bool {
	if len(s.AllowedPaths) == 0 {
		return true // No restrictions if no paths specified
	}

	path = normalizeVirtualPath(path)
	for _, allowed := range s.AllowedPaths {
		allowed = normalizeVirtualPath(allowed)
		if strings.HasPrefix(path, allowed) {
			return true
		}
	}
	return false
}

// logAudit logs an audit entry for this session
func (s *Session) logAudit(operation, path string, success bool, err error, bytesRead, bytesWritten int64) {
	if s.AuditLogger == nil {
		return
	}

	entry := AuditLogEntry{
		Timestamp:    time.Now(),
		SessionID:    s.ID,
		Operation:    operation,
		Path:         path,
		Success:      success,
		BytesRead:    bytesRead,
		BytesWritten: bytesWritten,
		AccessDenied: !success && err != nil && strings.Contains(err.Error(), "access denied"),
	}

	if err != nil {
		entry.Error = err.Error()
	}

	s.AuditLogger.Log(entry)
}

// SnapshotMetadata represents metadata for a snapshot
type SnapshotMetadata struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`       // Total size of snapshot data
	FileCount int       `json:"file_count"` // Number of files in snapshot
}

// FileSnapshot represents a snapshot of a single file
type FileSnapshot struct {
	Path      string    `json:"path"`
	Content   []byte    `json:"content"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	IsDir     bool      `json:"is_dir"`
	Operation string    `json:"operation"` // "created", "modified", "deleted", "unchanged"
}

// Snapshot represents a complete filesystem snapshot
type Snapshot struct {
	Metadata     SnapshotMetadata         `json:"metadata"`
	Files        map[string]*FileSnapshot `json:"files"`                   // Path -> FileSnapshot
	Changes      []ChangeRecord           `json:"changes"`                 // Tracked changes
	BaseSnapshot string                   `json:"base_snapshot,omitempty"` // For copy-on-write
}

// ChangeRecord tracks a change made after snapshot creation
type ChangeRecord struct {
	Path      string    `json:"path"`
	Operation string    `json:"operation"` // "write", "delete", "create"
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id,omitempty"`
}

// SandboxBackend defines interface for optional sandbox/MicroVM integration
type SandboxBackend interface {
	CreateSnapshot(name string) error
	RestoreSnapshot(name string) error
	DeleteSnapshot(name string) error
	ListSnapshots() ([]string, error)
}

// PluginMount represents a plugin mounted to a path
type PluginMount struct {
	PluginName string
	Plugin     ToolFSPlugin
	ReadOnly   bool // Whether the plugin mount is read-only
}

// ToolFS represents the filesystem instance
type ToolFS struct {
	rootPath        string
	mounts          map[string]*Mount
	pluginMounts    map[string]*PluginMount // Path -> PluginMount
	memoryStore     MemoryStore
	ragStore        RAGStore
	sessions        map[string]*Session
	snapshots       map[string]*Snapshot
	currentSnapshot string                // Currently active snapshot (if any)
	sandboxBackend  SandboxBackend        // Optional sandbox integration
	pluginManager   *PluginManager        // Optional plugin manager
	pluginRegistry  *PluginRegistry       // Optional direct plugin registry
	skillDocManager *SkillDocumentManager // Skill document manager
	builtinPlugins  *BuiltinPlugins       // Built-in plugins (Memory, RAG)

	// Performance optimizations: cached paths
	memoryPath         string   // Cached memory path: rootPath + "/memory"
	ragPath            string   // Cached RAG path: rootPath + "/rag"
	pathNormalizeCache sync.Map // Cache for path normalization results
	pathResolveCache   sync.Map // Cache for path resolution results (path -> *resolveCacheEntry)
}

// resolveCacheEntry represents a cached path resolution result
type resolveCacheEntry struct {
	localPath string
	mount     *Mount
	mu        sync.RWMutex
}

// NewToolFS creates a new ToolFS instance with the specified root path
func NewToolFS(rootPath string) *ToolFS {
	rootPath = normalizeVirtualPath(rootPath)
	fs := &ToolFS{
		rootPath:        rootPath,
		mounts:          make(map[string]*Mount),
		pluginMounts:    make(map[string]*PluginMount),
		memoryStore:     NewInMemoryStore(),
		ragStore:        NewInMemoryRAGStore(),
		sessions:        make(map[string]*Session),
		snapshots:       make(map[string]*Snapshot),
		currentSnapshot: "",
		skillDocManager: NewSkillDocumentManager(),
	}

	// Pre-compute and cache virtual paths for performance
	fs.memoryPath = normalizeVirtualPath(rootPath + "/memory")
	fs.ragPath = normalizeVirtualPath(rootPath + "/rag")

	// Load built-in skill documents from filesystem
	_ = fs.skillDocManager.LoadBuiltinSkillDocs()

	return fs
}

// SetPluginManager sets the plugin manager for ToolFS plugin mounts
// If builtin plugins haven't been registered yet, they will be registered automatically
func (fs *ToolFS) SetPluginManager(manager *PluginManager) {
	fs.pluginManager = manager

	// Auto-register builtin plugins if not already registered
	if fs.builtinPlugins == nil && manager != nil {
		// Create a default session for builtin plugins
		defaultSession, _ := fs.NewSession("__builtin__", []string{})
		builtinPlugins, err := RegisterBuiltinPlugins(fs, manager, defaultSession)
		if err == nil {
			fs.builtinPlugins = builtinPlugins

			// Register builtin plugins with skill document manager
			if fs.skillDocManager != nil {
				fs.skillDocManager.RegisterPlugin(builtinPlugins.Memory)
				fs.skillDocManager.RegisterPlugin(builtinPlugins.RAG)
			}
		}
	}
}

// SetSandboxBackend sets an optional sandbox backend for snapshot integration
func (fs *ToolFS) SetSandboxBackend(backend SandboxBackend) {
	fs.sandboxBackend = backend
}

// NewSession creates a new session and registers it with the ToolFS instance
func (fs *ToolFS) NewSession(sessionID string, allowedPaths []string) (*Session, error) {
	if _, exists := fs.sessions[sessionID]; exists {
		return nil, errors.New("session already exists")
	}

	session := NewSession(sessionID, allowedPaths)
	fs.sessions[sessionID] = session
	return session, nil
}

// GetSession retrieves a session by ID
func (fs *ToolFS) GetSession(sessionID string) (*Session, error) {
	session, exists := fs.sessions[sessionID]
	if !exists {
		return nil, errors.New("session not found")
	}
	return session, nil
}

// DeleteSession removes a session
func (fs *ToolFS) DeleteSession(sessionID string) {
	delete(fs.sessions, sessionID)
}

// SetMemoryStore sets the memory store for the ToolFS instance
func (fs *ToolFS) SetMemoryStore(store MemoryStore) {
	fs.memoryStore = store
}

// SetRAGStore sets the RAG store for the ToolFS instance
func (fs *ToolFS) SetRAGStore(store RAGStore) {
	fs.ragStore = store
}

// normalizeVirtualPath normalizes a virtual path to use forward slashes
// Optimized: uses builder to reduce allocations and handles common cases efficiently
func normalizeVirtualPath(path string) string {
	if path == "" {
		return ""
	}

	// Fast path: check if path is already normalized (common case)
	needsNormalization := false
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' || (i < len(path)-1 && path[i] == '/' && path[i+1] == '/') {
			needsNormalization = true
			break
		}
	}
	if !needsNormalization && !strings.HasPrefix(path, "./") {
		return path
	}

	// Slow path: normalize the path
	var builder strings.Builder
	builder.Grow(len(path)) // Pre-allocate capacity

	skipSlash := false
	start := 0

	// Skip "./" prefix
	if strings.HasPrefix(path, "./") {
		start = 2
	}

	for i := start; i < len(path); i++ {
		c := path[i]
		if c == '\\' {
			// Replace backslash with forward slash
			if !skipSlash {
				builder.WriteByte('/')
				skipSlash = true
			}
		} else if c == '/' {
			// Skip duplicate slashes
			if !skipSlash {
				builder.WriteByte('/')
				skipSlash = true
			}
		} else {
			builder.WriteByte(c)
			skipSlash = false
		}
	}

	result := builder.String()
	// Remove trailing slash if it's not the root
	if len(result) > 1 && result[len(result)-1] == '/' {
		result = result[:len(result)-1]
	}

	return result
}

// MountLocal mounts a local directory at the specified mount point
// mountPoint is the path within the ToolFS root (e.g., "/data")
// localPath is the actual local filesystem path
// readOnly determines if the mount is read-only
func (fs *ToolFS) MountLocal(mountPoint string, localPath string, readOnly bool) error {
	// Normalize mount point to use forward slashes
	mountPoint = normalizeVirtualPath(mountPoint)

	if !strings.HasPrefix(mountPoint, fs.rootPath) {
		// Join with root path, ensuring forward slashes
		if !strings.HasPrefix(mountPoint, "/") {
			mountPoint = "/" + mountPoint
		}
		mountPoint = normalizeVirtualPath(fs.rootPath + mountPoint)
	}

	// Verify local path exists
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("local path must be a directory")
	}

	fs.mounts[mountPoint] = &Mount{
		LocalPath: localPath,
		ReadOnly:  readOnly,
	}

	// Invalidate path resolution cache since mounts changed
	fs.pathResolveCache.Range(func(key, value interface{}) bool {
		path := key.(string)
		// Remove cache entries that might be affected by this mount
		if strings.HasPrefix(path, mountPoint) || strings.HasPrefix(mountPoint, path) {
			fs.pathResolveCache.Delete(key)
		}
		return true
	})

	return nil
}

// isVirtualPath checks if the path is a virtual path (memory or rag)
// Optimized: uses pre-computed cached paths
func (fs *ToolFS) isVirtualPath(path string) (bool, string) {
	path = normalizeVirtualPath(path)

	if strings.HasPrefix(path, fs.memoryPath) {
		return true, "memory"
	}
	if strings.HasPrefix(path, fs.ragPath) {
		return true, "rag"
	}
	return false, ""
}

// isPluginMount checks if the path is mounted to a plugin
func (fs *ToolFS) isPluginMount(path string) (*PluginMount, string) {
	path = normalizeVirtualPath(path)

	// Find the longest matching plugin mount point
	var bestMount *PluginMount
	var bestMountPoint string
	var relPath string

	for mountPoint, pluginMount := range fs.pluginMounts {
		mountPoint = normalizeVirtualPath(mountPoint)
		if strings.HasPrefix(path, mountPoint) {
			if len(mountPoint) > len(bestMountPoint) {
				bestMountPoint = mountPoint
				bestMount = pluginMount
				relPath = strings.TrimPrefix(path, mountPoint)
				relPath = strings.TrimPrefix(relPath, "/")
				if relPath == "" {
					relPath = "/"
				} else {
					relPath = "/" + relPath
				}
			}
		}
	}

	if bestMount != nil {
		return bestMount, relPath
	}

	return nil, ""
}

// MountPlugin mounts a plugin to a ToolFS path.
// When operations are performed on paths under the mount point,
// they are forwarded to the plugin's Execute method.
//
// Example:
//
//	fs.MountPlugin("/toolfs/rag", "rag-plugin")
//	// ReadFile("/toolfs/rag/query?text=test") will forward to plugin
func (fs *ToolFS) MountPlugin(path string, pluginName string) error {
	if path == "" {
		return errors.New("mount path cannot be empty")
	}
	if pluginName == "" {
		return errors.New("plugin name cannot be empty")
	}

	// Normalize path
	path = normalizeVirtualPath(path)

	if !strings.HasPrefix(path, fs.rootPath) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		path = normalizeVirtualPath(fs.rootPath + path)
	}

	// Check if plugin exists in plugin manager or registry
	var plugin ToolFSPlugin
	registry := fs.GetPluginRegistry()
	if registry != nil {
		var err error
		plugin, err = registry.Get(pluginName)
		if err != nil {
			return fmt.Errorf("plugin '%s' not found in registry: %w", pluginName, err)
		}
	} else {
		return errors.New("plugin registry not set, use AddPluginRegistry() or SetPluginManager() first")
	}

	// Check if path is already mounted
	if _, exists := fs.pluginMounts[path]; exists {
		return fmt.Errorf("path '%s' is already mounted to a plugin", path)
	}

	// Create plugin mount
	fs.pluginMounts[path] = &PluginMount{
		PluginName: pluginName,
		Plugin:     plugin,
		ReadOnly:   true, // Plugins are read-only by default for safety
	}

	// Invalidate path resolution cache since plugin mounts changed
	fs.pathResolveCache.Range(func(key, value interface{}) bool {
		pathKey := key.(string)
		// Remove cache entries that might be affected by this mount
		if strings.HasPrefix(pathKey, path) || strings.HasPrefix(path, pathKey) {
			fs.pathResolveCache.Delete(key)
		}
		return true
	})

	return nil
}

// UnmountPlugin removes a plugin mount from a path.
func (fs *ToolFS) UnmountPlugin(path string) error {
	path = normalizeVirtualPath(path)

	if !strings.HasPrefix(path, fs.rootPath) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		path = normalizeVirtualPath(fs.rootPath + path)
	}

	if _, exists := fs.pluginMounts[path]; !exists {
		return fmt.Errorf("no plugin mounted at path '%s'", path)
	}

	delete(fs.pluginMounts, path)

	// Invalidate path resolution cache since plugin mounts changed
	fs.pathResolveCache.Range(func(key, value interface{}) bool {
		pathKey := key.(string)
		// Remove cache entries that might be affected by this unmount
		if strings.HasPrefix(pathKey, path) {
			fs.pathResolveCache.Delete(key)
		}
		return true
	})

	return nil
}

// executePluginMount executes a plugin for a given path and operation.
func (fs *ToolFS) executePluginMount(pluginMount *PluginMount, path, relPath, operation string, inputData []byte, session *Session) ([]byte, error) {
	// Create plugin request
	request := PluginRequest{
		Operation: operation,
		Path:      path,
		Data: map[string]interface{}{
			"relative_path": relPath,
			"full_path":     path,
		},
	}

	// Add input data if provided
	if inputData != nil {
		request.Data["input"] = string(inputData)
	}

	// Add session info if available
	if session != nil {
		request.Data["session_id"] = session.ID
	}

	// Marshal request
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin request: %w", err)
	}

	// Execute plugin with error recovery
	var output []byte
	var execErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Plugin execution panicked - convert to error but don't crash
				execErr = fmt.Errorf("plugin execution panicked: %v", r)
			}
		}()

		// Execute plugin
		output, execErr = pluginMount.Plugin.Execute(requestBytes)
	}()

	if execErr != nil {
		return nil, fmt.Errorf("plugin execution failed: %w", execErr)
	}

	// Parse plugin response
	var response PluginResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to parse plugin response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("plugin returned error: %s", response.Error)
	}

	// Extract result
	if resultStr, ok := response.Result.(string); ok {
		return []byte(resultStr), nil
	}

	// Try to marshal result to JSON if it's not a string
	resultBytes, err := json.Marshal(response.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plugin result: %w", err)
	}

	return resultBytes, nil
}

// resolvePath resolves a ToolFS path to a local filesystem path
// Optimized: uses result caching to avoid repeated resolution
func (fs *ToolFS) resolvePath(path string) (string, *Mount, error) {
	// Normalize the virtual path to use forward slashes
	path = normalizeVirtualPath(path)

	// Try to get from cache first
	// Note: Cache is invalidated when mounts change (MountLocal/UnmountPlugin)
	if cached, ok := fs.pathResolveCache.Load(path); ok {
		entry := cached.(*resolveCacheEntry)
		entry.mu.RLock()
		localPath := entry.localPath
		mount := entry.mount
		entry.mu.RUnlock()

		// Return cached result
		// Cache is invalidated when mounts change, so this is safe
		return localPath, mount, nil
	}

	// Not in cache, resolve the path
	var localPath string
	var mount *Mount
	var err error

	// Check if this is a plugin mount first (highest priority)
	if pluginMount, relPath := fs.isPluginMount(path); pluginMount != nil {
		// Return special marker for plugin mount
		localPath = relPath
		mount = &Mount{LocalPath: "__PLUGIN_MOUNT__:" + pluginMount.PluginName, ReadOnly: pluginMount.ReadOnly}
	} else if isVirtual, vType := fs.isVirtualPath(path); isVirtual {
		// Check if this is a virtual path (memory or rag)
		localPath = ""
		if vType == "memory" {
			mount = &Mount{LocalPath: "__VIRTUAL_MEMORY__", ReadOnly: false}
		} else if vType == "rag" {
			mount = &Mount{LocalPath: "__VIRTUAL_RAG__", ReadOnly: true}
		}
	} else {
		// Find the longest matching mount point
		var bestMount *Mount
		var bestMountPoint string
		var bestLocalPath string

		for mountPoint, m := range fs.mounts {
			if strings.HasPrefix(path, mountPoint) {
				if len(mountPoint) > len(bestMountPoint) {
					bestMountPoint = mountPoint
					bestMount = m
					// Calculate the relative path within the mount
					relPath := strings.TrimPrefix(path, mountPoint)
					relPath = strings.TrimPrefix(relPath, "/")
					relPath = strings.TrimPrefix(relPath, "\\")
					if relPath == "" {
						bestLocalPath = m.LocalPath
					} else {
						bestLocalPath = filepath.Join(m.LocalPath, relPath)
					}
				}
			}
		}

		if bestMount == nil {
			err = errors.New("path not found in any mount")
			return "", nil, err
		}

		localPath = bestLocalPath
		mount = bestMount
	}

	// Cache the result (only for successful resolutions)
	// Note: err is nil at this point if we reached here
	entry := &resolveCacheEntry{
		localPath: localPath,
		mount:     mount,
	}
	fs.pathResolveCache.Store(path, entry)

	return localPath, mount, nil
}

// ReadFile reads a file from the ToolFS
func (fs *ToolFS) ReadFile(path string) ([]byte, error) {
	return fs.ReadFileWithSession(path, nil)
}

// ReadFileWithSession reads a file from the ToolFS with session-based access control
func (fs *ToolFS) ReadFileWithSession(path string, session *Session) ([]byte, error) {
	// Check access control
	if session != nil && !session.IsPathAllowed(path) {
		err := fmt.Errorf("access denied: path '%s' is not allowed for session '%s'", path, session.ID)
		session.logAudit("ReadFile", path, false, err, 0, 0)
		return nil, err
	}

	localPath, mount, err := fs.resolvePath(path)
	if err != nil {
		if session != nil {
			session.logAudit("ReadFile", path, false, err, 0, 0)
		}
		return nil, err
	}

	var data []byte

	// Handle plugin mounts
	if strings.HasPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:") {
		pluginName := strings.TrimPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:")
		// Find the plugin mount by matching path prefix
		var pluginMount *PluginMount
		var mountPoint string
		for mp, pm := range fs.pluginMounts {
			if pm.PluginName == pluginName && strings.HasPrefix(path, mp) {
				if len(mp) > len(mountPoint) {
					mountPoint = mp
					pluginMount = pm
				}
			}
		}

		if pluginMount != nil {
			// Execute plugin with error recovery
			data, err = fs.executePluginMount(pluginMount, path, localPath, "read_file", nil, session)
			if err != nil {
				// Return error but don't crash
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("plugin mount not found for path: %s", path)
		}
	} else if mount.LocalPath == "__VIRTUAL_MEMORY__" {
		data, err = fs.readMemory(path)
	} else if mount.LocalPath == "__VIRTUAL_RAG__" {
		data, err = fs.readRAG(path)
	} else {
		data, err = os.ReadFile(localPath)
	}

	// Log audit entry
	if session != nil {
		bytesRead := int64(0)
		if err == nil {
			bytesRead = int64(len(data))
		}
		session.logAudit("ReadFile", path, err == nil, err, bytesRead, 0)
	}

	return data, err
}

// readMemory reads a memory entry
// Optimized: uses pre-computed cached memoryPath
func (fs *ToolFS) readMemory(path string) ([]byte, error) {
	path = normalizeVirtualPath(path)
	memoryPathWithSlash := fs.memoryPath + "/"

	// Extract memory entry ID
	relPath := strings.TrimPrefix(path, memoryPathWithSlash)
	parts := strings.Split(relPath, "/")
	if len(parts) == 0 || parts[0] == "" {
		// Listing memory directory - return empty for now
		return nil, errors.New("cannot read memory directory directly, use ListDir")
	}

	entryID := parts[0]
	entry, err := fs.memoryStore.Get(entryID)
	if err != nil {
		return nil, err
	}

	// Return plain text content for filesystem compatibility
	// JSON representation is available through the API/plugin interface
	return []byte(entry.Content), nil
}

// readRAG performs a RAG search
// Optimized: uses pre-computed cached ragPath
func (fs *ToolFS) readRAG(path string) ([]byte, error) {
	path = normalizeVirtualPath(path)
	ragPathWithSlash := fs.ragPath + "/"

	// Check if this is a query
	relPath := strings.TrimPrefix(path, ragPathWithSlash)
	if strings.HasPrefix(relPath, "query") {
		// Split on "?" to separate path from query string
		parts := strings.SplitN(relPath, "?", 2)
		if len(parts) < 2 {
			return nil, errors.New("invalid RAG query format, missing query parameters")
		}

		// Parse query parameters
		queryURL, err := url.ParseQuery(parts[1])
		if err != nil {
			return nil, errors.New("invalid RAG query format")
		}

		query := queryURL.Get("text")
		if query == "" {
			query = queryURL.Get("q")
		}
		if query == "" {
			return nil, errors.New("missing 'text' or 'q' parameter in RAG query")
		}

		// Decode URL-encoded query (e.g., "AI+agent" -> "AI agent")
		decodedQuery, err := url.QueryUnescape(query)
		if err == nil {
			query = decodedQuery
		}

		topK := 5 // default
		if topKStr := queryURL.Get("top_k"); topKStr != "" {
			var err error
			topK, err = strconv.Atoi(topKStr)
			if err != nil || topK <= 0 {
				return nil, errors.New("invalid top_k parameter")
			}
		}

		results, err := fs.ragStore.Search(query, topK)
		if err != nil {
			return nil, err
		}

		searchResults := RAGSearchResults{
			Query:   query,
			TopK:    topK,
			Results: results,
		}

		return json.Marshal(searchResults)
	}

	return nil, errors.New("invalid RAG path, use /toolfs/rag/query?text=...&top_k=...")
}

// WriteFile writes data to a file in the ToolFS
func (fs *ToolFS) WriteFile(path string, data []byte) error {
	return fs.WriteFileWithSession(path, data, nil)
}

// WriteFileWithSession writes data to a file in the ToolFS with session-based access control
func (fs *ToolFS) WriteFileWithSession(path string, data []byte, session *Session) error {
	// Check access control
	if session != nil && !session.IsPathAllowed(path) {
		err := fmt.Errorf("access denied: path '%s' is not allowed for session '%s'", path, session.ID)
		session.logAudit("WriteFile", path, false, err, 0, 0)
		return err
	}

	localPath, mount, err := fs.resolvePath(path)
	if err != nil {
		if session != nil {
			session.logAudit("WriteFile", path, false, err, 0, 0)
		}
		return err
	}

	// Handle plugin mounts
	if strings.HasPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:") {
		pluginName := strings.TrimPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:")
		var pluginMount *PluginMount
		var mountPoint string
		for mp, pm := range fs.pluginMounts {
			if pm.PluginName == pluginName && strings.HasPrefix(path, mp) {
				if len(mp) > len(mountPoint) {
					mountPoint = mp
					pluginMount = pm
				}
			}
		}

		if pluginMount != nil {
			if pluginMount.ReadOnly {
				err := errors.New("cannot write to read-only plugin mount")
				if session != nil {
					session.logAudit("WriteFile", path, false, err, 0, 0)
				}
				return err
			}
			// Execute plugin for write_file operation
			_, err = fs.executePluginMount(pluginMount, path, localPath, "write_file", data, session)
			if err != nil {
				// Return error but don't crash
				if session != nil {
					session.logAudit("WriteFile", path, false, err, 0, 0)
				}
				return err
			}
		} else {
			err = fmt.Errorf("plugin mount not found for path: %s", path)
		}
	} else if mount.ReadOnly {
		err := errors.New("cannot write to read-only mount")
		if session != nil {
			session.logAudit("WriteFile", path, false, err, 0, 0)
		}
		return err
	} else if mount.LocalPath == "__VIRTUAL_MEMORY__" {
		err = fs.writeMemory(path, data)
	} else if mount.LocalPath == "__VIRTUAL_RAG__" {
		err = errors.New("cannot write to RAG store")
	} else {
		// Create parent directory if it doesn't exist
		parentDir := filepath.Dir(localPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			if session != nil {
				session.logAudit("WriteFile", path, false, err, 0, 0)
			}
			return err
		}
		err = os.WriteFile(localPath, data, 0644)
	}

	// Log audit entry
	if session != nil {
		bytesWritten := int64(0)
		if err == nil {
			bytesWritten = int64(len(data))
		}
		session.logAudit("WriteFile", path, err == nil, err, 0, bytesWritten)
	}

	// Track change for snapshot
	if err == nil {
		sessionID := ""
		if session != nil {
			sessionID = session.ID
		}
		// Optimization: Check file existence before write to avoid extra Stat call
		// We can use os.Stat on localPath before writing to determine create vs modify
		operation := "write"
		if localPath != "" && mount != nil && mount.LocalPath != "__VIRTUAL_MEMORY__" && mount.LocalPath != "__VIRTUAL_RAG__" {
			// Check if file existed before write (optimize: only for local filesystem)
			if _, statErr := os.Stat(localPath); statErr != nil {
				operation = "create"
			}
		}
		// Note: For virtual paths, we default to "write" since existence check
		// would require calling the virtual store, which may be expensive
		fs.TrackChange(path, operation, sessionID)
	}

	return err
}

// writeMemory writes to a memory entry
// Optimized: uses pre-computed cached memoryPath
func (fs *ToolFS) writeMemory(path string, data []byte) error {
	path = normalizeVirtualPath(path)
	memoryPathWithSlash := fs.memoryPath + "/"

	// Extract memory entry ID
	relPath := strings.TrimPrefix(path, memoryPathWithSlash)
	parts := strings.Split(relPath, "/")
	if len(parts) == 0 || parts[0] == "" {
		return errors.New("invalid memory path, expected /toolfs/memory/<id>")
	}

	entryID := parts[0]

	// Try to parse as JSON first (for metadata)
	var entry MemoryEntry
	if err := json.Unmarshal(data, &entry); err == nil {
		// JSON format with metadata
		metadata := entry.Metadata
		if metadata == nil {
			metadata = make(map[string]interface{})
		}
		return fs.memoryStore.Set(entryID, entry.Content, metadata)
	}

	// Plain text content
	return fs.memoryStore.Set(entryID, string(data), nil)
}

// ListDir lists the contents of a directory
func (fs *ToolFS) ListDir(path string) ([]string, error) {
	return fs.ListDirWithSession(path, nil)
}

// ListDirWithSession lists the contents of a directory with session-based access control
func (fs *ToolFS) ListDirWithSession(path string, session *Session) ([]string, error) {
	// Check access control
	if session != nil && !session.IsPathAllowed(path) {
		err := fmt.Errorf("access denied: path '%s' is not allowed for session '%s'", path, session.ID)
		session.logAudit("ListDir", path, false, err, 0, 0)
		return nil, err
	}

	localPath, mount, err := fs.resolvePath(path)
	if err != nil {
		if session != nil {
			session.logAudit("ListDir", path, false, err, 0, 0)
		}
		return nil, err
	}

	var entries []string

	// Handle plugin mounts
	if strings.HasPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:") {
		pluginName := strings.TrimPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:")
		var pluginMount *PluginMount
		var mountPoint string
		for mp, pm := range fs.pluginMounts {
			if pm.PluginName == pluginName && strings.HasPrefix(path, mp) {
				if len(mp) > len(mountPoint) {
					mountPoint = mp
					pluginMount = pm
				}
			}
		}

		if pluginMount != nil {
			// Execute plugin for list_dir operation
			data, execErr := fs.executePluginMount(pluginMount, path, localPath, "list_dir", nil, session)
			if execErr != nil {
				err = execErr
			} else {
				// Parse the JSON response to extract entries
				var resultData interface{}
				if unmarshalErr := json.Unmarshal(data, &resultData); unmarshalErr == nil {
					if resultMap, ok := resultData.(map[string]interface{}); ok {
						if entriesArr, ok := resultMap["entries"].([]interface{}); ok {
							entries = make([]string, 0, len(entriesArr))
							for _, e := range entriesArr {
								if str, ok := e.(string); ok {
									entries = append(entries, str)
								}
							}
						}
					} else if entriesArr, ok := resultData.([]interface{}); ok {
						// Result is directly an array
						entries = make([]string, 0, len(entriesArr))
						for _, e := range entriesArr {
							if str, ok := e.(string); ok {
								entries = append(entries, str)
							}
						}
					}
				}

				if len(entries) == 0 {
					entries = []string{}
				}
			}
		} else {
			err = fmt.Errorf("plugin mount not found for path: %s", path)
		}
	} else if mount.LocalPath == "__VIRTUAL_MEMORY__" {
		path = normalizeVirtualPath(path)
		// Optimized: uses pre-computed cached memoryPath
		if path == fs.memoryPath || strings.HasPrefix(path, fs.memoryPath+"/") {
			entries, err = fs.memoryStore.List()
		} else {
			err = errors.New("cannot list RAG directory")
		}
	} else if mount.LocalPath == "__VIRTUAL_RAG__" {
		// RAG is read-only and doesn't support listing
		entries = []string{"query"}
	} else {
		dirEntries, readErr := os.ReadDir(localPath)
		if readErr != nil {
			err = readErr
		} else {
			entries = make([]string, 0, len(dirEntries))
			for _, entry := range dirEntries {
				entries = append(entries, entry.Name())
			}
		}
	}

	// Log audit entry
	if session != nil {
		session.logAudit("ListDir", path, err == nil, err, 0, 0)
	}

	return entries, err
}

// Stat returns file metadata for the given path
func (fs *ToolFS) Stat(path string) (*FileInfo, error) {
	return fs.StatWithSession(path, nil)
}

// StatWithSession returns file metadata for the given path with session-based access control
func (fs *ToolFS) StatWithSession(path string, session *Session) (*FileInfo, error) {
	// Check access control
	if session != nil && !session.IsPathAllowed(path) {
		err := fmt.Errorf("access denied: path '%s' is not allowed for session '%s'", path, session.ID)
		session.logAudit("Stat", path, false, err, 0, 0)
		return nil, err
	}

	localPath, mount, err := fs.resolvePath(path)
	if err != nil {
		if session != nil {
			session.logAudit("Stat", path, false, err, 0, 0)
		}
		return nil, err
	}

	// Handle virtual paths (memory, rag, plugins)
	if mount != nil {
		if mount.LocalPath == "__VIRTUAL_MEMORY__" {
			// For memory entries, check if it's a directory or file
			path = normalizeVirtualPath(path)
			// Optimized: uses pre-computed cached memoryPath
			if path == fs.memoryPath {
				// Root memory directory
				return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: true}, nil
			}
			// Check if entry exists
			relPath := strings.TrimPrefix(path, fs.memoryPath+"/")
			parts := strings.Split(relPath, "/")
			if len(parts) > 0 && parts[0] != "" {
				entry, err := fs.memoryStore.Get(parts[0])
				if err != nil {
					return nil, err
				}
				// Return actual content size (plain text, not JSON)
				contentSize := int64(len(entry.Content))
				return &FileInfo{Size: contentSize, ModTime: entry.UpdatedAt, IsDir: false}, nil
			}
			return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: true}, nil
		}
		if mount.LocalPath == "__VIRTUAL_RAG__" {
			// RAG is always a directory at root, query is a virtual file
			path = normalizeVirtualPath(path)
			// Optimized: uses pre-computed cached ragPath
			if path == fs.ragPath {
				return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: true}, nil
			}
			// Query files are virtual
			if strings.HasPrefix(path, fs.ragPath+"/query") {
				return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: false}, nil
			}
			return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: true}, nil
		}
		if strings.HasPrefix(mount.LocalPath, "__PLUGIN_MOUNT__:") {
			// Plugin mounts - treat as directory for now
			// In a real implementation, plugins should provide stat info
			return &FileInfo{Size: 0, ModTime: time.Now(), IsDir: true}, nil
		}
	}

	info, err := os.Stat(localPath)
	if err != nil {
		if session != nil {
			session.logAudit("Stat", path, false, err, 0, 0)
		}
		return nil, err
	}

	result := &FileInfo{
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}

	// Log audit entry
	if session != nil {
		session.logAudit("Stat", path, true, nil, 0, 0)
	}

	return result, nil
}

// InMemoryStore is a simple in-memory implementation of MemoryStore
// Optimized with RWMutex for concurrent access
type InMemoryStore struct {
	mu             sync.RWMutex // Protects entries map
	entries        map[string]*MemoryEntry
	listCache      []string // Cache for List() results
	listCacheValid bool     // Whether listCache is still valid
}

// NewInMemoryStore creates a new in-memory memory store
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries:        make(map[string]*MemoryEntry),
		listCache:      nil,
		listCacheValid: false,
	}
}

// Get retrieves a memory entry by ID
// Optimized with read lock for concurrent access
func (s *InMemoryStore) Get(id string) (*MemoryEntry, error) {
	s.mu.RLock()
	entry, exists := s.entries[id]
	s.mu.RUnlock()

	if !exists {
		return nil, errors.New("memory entry not found")
	}
	return entry, nil
}

// Set stores or updates a memory entry
// Optimized with write lock and list cache invalidation
func (s *InMemoryStore) Set(id string, content string, metadata map[string]interface{}) error {
	now := time.Now()
	s.mu.Lock()
	entry, exists := s.entries[id]

	if exists {
		// Update existing entry
		entry.Content = content
		entry.UpdatedAt = now
		if metadata != nil {
			entry.Metadata = metadata
		}
	} else {
		// Create new entry - reuse map allocation
		if s.entries == nil {
			s.entries = make(map[string]*MemoryEntry)
		}
		s.entries[id] = &MemoryEntry{
			ID:        id,
			Content:   content,
			CreatedAt: now,
			UpdatedAt: now,
			Metadata:  metadata,
		}
	}

	// Invalidate list cache on modification
	s.listCacheValid = false
	s.mu.Unlock()

	return nil
}

// List returns all memory entry IDs
// Optimized with read lock and result caching
func (s *InMemoryStore) List() ([]string, error) {
	s.mu.RLock()

	// Return cached result if valid
	if s.listCacheValid && s.listCache != nil {
		// Make a copy to avoid concurrent modification issues
		result := make([]string, len(s.listCache))
		copy(result, s.listCache)
		s.mu.RUnlock()
		return result, nil
	}

	// Build list from entries
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	// Cache the result (optimistic caching, may be invalidated by Set)
	s.mu.Lock()
	if !s.listCacheValid { // Double-check
		s.listCache = make([]string, len(ids))
		copy(s.listCache, ids)
		s.listCacheValid = true
	}
	s.mu.Unlock()

	return ids, nil
}

// InMemoryRAGStore is a simple in-memory implementation of RAGStore
type InMemoryRAGStore struct {
	documents []RAGDocument
}

// RAGDocument represents a document in the RAG store
type RAGDocument struct {
	ID       string
	Content  string
	Metadata map[string]interface{}
}

// NewInMemoryRAGStore creates a new in-memory RAG store
func NewInMemoryRAGStore() *InMemoryRAGStore {
	store := &InMemoryRAGStore{
		documents: []RAGDocument{
			{ID: "doc1", Content: "AI agents are intelligent systems that can perform tasks autonomously.", Metadata: map[string]interface{}{"topic": "AI"}},
			{ID: "doc2", Content: "Memory systems help AI agents remember past interactions and context.", Metadata: map[string]interface{}{"topic": "Memory"}},
			{ID: "doc3", Content: "RAG (Retrieval-Augmented Generation) enhances AI responses with relevant information.", Metadata: map[string]interface{}{"topic": "RAG"}},
			{ID: "doc4", Content: "ToolFS provides a unified filesystem interface for AI agents.", Metadata: map[string]interface{}{"topic": "ToolFS"}},
			{ID: "doc5", Content: "Semantic search finds documents based on meaning rather than exact matches.", Metadata: map[string]interface{}{"topic": "Search"}},
		},
	}
	return store
}

// Search performs a simple keyword-based search (simulating semantic search)
func (s *InMemoryRAGStore) Search(query string, topK int) ([]RAGResult, error) {
	queryLower := strings.ToLower(query)
	var results []RAGResult

	for _, doc := range s.documents {
		// Simple keyword matching (in a real implementation, this would be semantic)
		contentLower := strings.ToLower(doc.Content)
		score := 0.0

		queryWords := strings.Fields(queryLower)
		for _, word := range queryWords {
			if strings.Contains(contentLower, word) {
				score += 1.0
			}
		}

		if score > 0 {
			// Normalize score (simple heuristic)
			score = score / float64(len(queryWords))
			results = append(results, RAGResult{
				ID:       doc.ID,
				Content:  doc.Content,
				Score:    score,
				Metadata: doc.Metadata,
			})
		}
	}

	// Sort by score (descending) and limit to topK
	if len(results) > topK {
		// Simple sort by score
		for i := 0; i < len(results)-1; i++ {
			for j := i + 1; j < len(results); j++ {
				if results[i].Score < results[j].Score {
					results[i], results[j] = results[j], results[i]
				}
			}
		}
		results = results[:topK]
	}

	if len(results) == 0 {
		// Return empty results rather than error
		return []RAGResult{}, nil
	}

	return results, nil
}

// CreateSnapshot creates a snapshot of the current filesystem state
func (fs *ToolFS) CreateSnapshot(name string) error {
	if name == "" {
		return errors.New("snapshot name cannot be empty")
	}

	if _, exists := fs.snapshots[name]; exists {
		return fmt.Errorf("snapshot '%s' already exists", name)
	}

	// If sandbox backend is available, use it
	if fs.sandboxBackend != nil {
		if err := fs.sandboxBackend.CreateSnapshot(name); err != nil {
			return fmt.Errorf("sandbox backend error: %w", err)
		}
	}

	snapshot := &Snapshot{
		Metadata: SnapshotMetadata{
			Name:      name,
			CreatedAt: time.Now(),
		},
		Files:   make(map[string]*FileSnapshot),
		Changes: []ChangeRecord{},
	}

	// Use copy-on-write: reference base snapshot if one exists
	// Don't copy file references here - we'll capture current state in snapshotDirectory
	// This ensures we get the current state of files, even if they were modified
	if fs.currentSnapshot != "" {
		if _, exists := fs.snapshots[fs.currentSnapshot]; exists {
			snapshot.BaseSnapshot = fs.currentSnapshot
			// We'll use the base snapshot for unchanged files during snapshotDirectory
		}
	}

	// Snapshot all mounted filesystems (copy-on-write)
	var totalSize int64
	fileCount := 0

	for mountPoint, mount := range fs.mounts {
		if mount.ReadOnly {
			continue // Skip read-only mounts for snapshots
		}

		err := fs.snapshotDirectory(mountPoint, mount.LocalPath, snapshot)
		if err != nil {
			return fmt.Errorf("failed to snapshot directory %s: %w", mountPoint, err)
		}
	}

	// Count files and calculate size
	for _, fileSnap := range snapshot.Files {
		if !fileSnap.IsDir {
			fileCount++
			totalSize += fileSnap.Size
		}
	}

	snapshot.Metadata.FileCount = fileCount
	snapshot.Metadata.Size = totalSize

	fs.snapshots[name] = snapshot
	fs.currentSnapshot = name

	return nil
}

// snapshotDirectory recursively snapshots a directory (copy-on-write)
func (fs *ToolFS) snapshotDirectory(mountPoint, localPath string, snapshot *Snapshot) error {
	return filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate virtual path
		relPath, err := filepath.Rel(localPath, path)
		if err != nil {
			return err
		}

		virtualPath := normalizeVirtualPath(mountPoint + "/" + relPath)

		// Check if file exists in base snapshot chain and if it was modified
		existsInBase := false
		fileWasModified := false
		if snapshot.BaseSnapshot != "" {
			// Recursively check base snapshot chain to find file
			var checkBase func(snapName string) (bool, bool) // returns (exists, modified)
			checkBase = func(snapName string) (bool, bool) {
				if snapName == "" {
					return false, false
				}
				if baseSnap, exists := fs.snapshots[snapName]; exists {
					if baseFileSnap, found := baseSnap.Files[virtualPath]; found {
						// File exists in this snapshot
						// Compare modification time and size to detect changes
						timeModified := info.ModTime().After(baseFileSnap.ModTime)
						sizeModified := info.Size() != baseFileSnap.Size
						modified := timeModified || sizeModified

						// If time or size indicates modification, or if we can't reliably determine,
						// always compare content for files to ensure accuracy
						if !info.IsDir() {
							if !modified || sizeModified {
								// Double-check by comparing content for accuracy
								currentContent, readErr := os.ReadFile(path)
								if readErr == nil {
									// If base snapshot has content, compare it
									if len(baseFileSnap.Content) > 0 {
										if string(currentContent) != string(baseFileSnap.Content) {
											modified = true
										} else if timeModified || sizeModified {
											// Content is same but time/size differ - file was rewritten but content is same
											// Consider it unchanged for copy-on-write optimization
											modified = false
										}
									} else {
										// Base has no content, use time/size as indicator
										// (This shouldn't happen for normal files)
										modified = timeModified || sizeModified
									}
								}
							}
						}

						return true, modified
					}
					// Recurse to base snapshot's base
					if baseSnap.BaseSnapshot != "" {
						return checkBase(baseSnap.BaseSnapshot)
					}
				}
				return false, false
			}

			existsInBase, fileWasModified = checkBase(snapshot.BaseSnapshot)

			if fileWasModified {
				// File was modified, capture current state
				var content []byte
				if !info.IsDir() {
					var err error
					content, err = os.ReadFile(path)
					if err != nil {
						return err
					}
				}
				fileSnap := &FileSnapshot{
					Path:      virtualPath,
					Size:      info.Size(),
					ModTime:   info.ModTime(),
					IsDir:     info.IsDir(),
					Operation: "modified",
					Content:   content,
				}
				snapshot.Files[virtualPath] = fileSnap
				return nil
			}
		}

		// Skip if already in snapshot and not modified
		if _, exists := snapshot.Files[virtualPath]; exists {
			return nil
		}

		// For copy-on-write: if file exists in base but unchanged, we don't add it here
		// It will be resolved from the base snapshot chain during restore
		// Only capture file if it doesn't exist in base chain
		if !existsInBase {
			// New file or file not in base - capture it
			var content []byte
			if !info.IsDir() {
				var err error
				content, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			fileSnap := &FileSnapshot{
				Path:      virtualPath,
				Size:      info.Size(),
				ModTime:   info.ModTime(),
				IsDir:     info.IsDir(),
				Operation: "created",
				Content:   content,
			}
			snapshot.Files[virtualPath] = fileSnap
		}

		return nil
	})
}

// RollbackSnapshot restores the filesystem to a previous snapshot state
func (fs *ToolFS) RollbackSnapshot(name string) error {
	snapshot, exists := fs.snapshots[name]
	if !exists {
		return fmt.Errorf("snapshot '%s' does not exist", name)
	}

	// If sandbox backend is available, use it
	if fs.sandboxBackend != nil {
		if err := fs.sandboxBackend.RestoreSnapshot(name); err != nil {
			return fmt.Errorf("sandbox backend error: %w", err)
		}
	}

	// Restore files from snapshot (copy-on-write aware)
	// Important: We're restoring to this snapshot, so this becomes the current snapshot
	// but we don't update the snapshot's content - it's immutable
	// Build complete file list from snapshot and its base chain
	filesToRestore := make(map[string]*FileSnapshot)

	// Recursively collect files from snapshot and its base chain
	// IMPORTANT: We must recurse to base first, then add current snapshot files
	// This ensures current snapshot files override base snapshot files
	var collectFiles func(snap *Snapshot)
	collectFiles = func(snap *Snapshot) {
		// First, recurse to base snapshot if exists (collect base files first)
		if snap.BaseSnapshot != "" {
			if baseSnap, exists := fs.snapshots[snap.BaseSnapshot]; exists {
				collectFiles(baseSnap)
			}
		}

		// Then add all files from this snapshot (overwrites base files if modified)
		// This ensures current snapshot takes precedence over base snapshots
		for path, fileSnap := range snap.Files {
			// Skip deleted files - don't restore them
			if fileSnap.Operation != "deleted" {
				filesToRestore[path] = fileSnap
			}
		}
	}

	// Collect all files, with current snapshot taking precedence
	collectFiles(snapshot)

	// Restore each file
	for virtualPath, fileSnap := range filesToRestore {
		if fileSnap.IsDir {
			continue // Skip directories
		}

		// Resolve to local path
		localPath, mount, err := fs.resolvePath(virtualPath)
		if err != nil {
			// File may have been deleted, try to create it
			if mount == nil {
				continue
			}
			// Reconstruct path from mount
			mountPoint := ""
			for mp, m := range fs.mounts {
				if m == mount {
					mountPoint = mp
					break
				}
			}
			if mountPoint == "" {
				continue
			}
			relPath := strings.TrimPrefix(virtualPath, mountPoint)
			relPath = strings.TrimPrefix(relPath, "/")
			localPath = filepath.Join(mount.LocalPath, relPath)
		}

		// Create parent directory if needed
		parentDir := filepath.Dir(localPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Write file content
		if err := os.WriteFile(localPath, fileSnap.Content, 0644); err != nil {
			return fmt.Errorf("failed to restore file %s: %w", virtualPath, err)
		}

		// Restore modification time if possible
		os.Chtimes(localPath, fileSnap.ModTime, fileSnap.ModTime)
	}

	// Handle deleted files (compare with current state)
	if fs.currentSnapshot != "" {
		currentSnap, exists := fs.snapshots[fs.currentSnapshot]
		if exists {
			// Find files that exist in current but not in target snapshot
			for path := range currentSnap.Files {
				if _, exists := snapshot.Files[path]; !exists {
					// File should be deleted
					localPath, _, err := fs.resolvePath(path)
					if err == nil {
						os.Remove(localPath)
					}
				}
			}
		}
	}

	// Record rollback operation (but don't modify the snapshot's content)
	fs.currentSnapshot = name

	return nil
}

// GetSnapshot retrieves snapshot metadata
func (fs *ToolFS) GetSnapshot(name string) (*SnapshotMetadata, error) {
	snapshot, exists := fs.snapshots[name]
	if !exists {
		return nil, fmt.Errorf("snapshot '%s' does not exist", name)
	}

	return &snapshot.Metadata, nil
}

// ListSnapshots returns all snapshot names
func (fs *ToolFS) ListSnapshots() ([]string, error) {
	if fs.sandboxBackend != nil {
		return fs.sandboxBackend.ListSnapshots()
	}

	names := make([]string, 0, len(fs.snapshots))
	for name := range fs.snapshots {
		names = append(names, name)
	}
	return names, nil
}

// DeleteSnapshot removes a snapshot
func (fs *ToolFS) DeleteSnapshot(name string) error {
	if name == "" {
		return errors.New("snapshot name cannot be empty")
	}

	if name == fs.currentSnapshot {
		return errors.New("cannot delete current snapshot")
	}

	if _, exists := fs.snapshots[name]; !exists {
		return fmt.Errorf("snapshot '%s' does not exist", name)
	}

	if fs.sandboxBackend != nil {
		if err := fs.sandboxBackend.DeleteSnapshot(name); err != nil {
			return fmt.Errorf("sandbox backend error: %w", err)
		}
	}

	delete(fs.snapshots, name)
	return nil
}

// TrackChange records a change for the current snapshot
func (fs *ToolFS) TrackChange(path, operation, sessionID string) {
	if fs.currentSnapshot == "" {
		return // No active snapshot to track
	}

	snapshot, exists := fs.snapshots[fs.currentSnapshot]
	if !exists {
		return
	}

	change := ChangeRecord{
		Path:      path,
		Operation: operation,
		Timestamp: time.Now(),
		SessionID: sessionID,
	}

	// Track the change for auditing only - snapshots are immutable
	// We track changes to know what happened, but don't modify the snapshot content
	snapshot.Changes = append(snapshot.Changes, change)

	// Note: We intentionally do NOT update snapshot.Files here
	// Snapshots are immutable once created. To capture modified state,
	// create a new snapshot which will capture the current filesystem state.
}

// GetSnapshotChanges returns all changes tracked for a snapshot
func (fs *ToolFS) GetSnapshotChanges(name string) ([]ChangeRecord, error) {
	snapshot, exists := fs.snapshots[name]
	if !exists {
		return nil, fmt.Errorf("snapshot '%s' does not exist", name)
	}

	return snapshot.Changes, nil
}

// CommandValidator defines the interface for validating CLI commands
type CommandValidator interface {
	IsCommandAllowed(command string, args []string) (bool, string)
}

// DangerousCommandFilter is a default implementation that blocks dangerous commands
type DangerousCommandFilter struct {
	blockedCommands map[string]bool
}

// NewDangerousCommandFilter creates a new command filter with default blocked commands
func NewDangerousCommandFilter() *DangerousCommandFilter {
	blocked := map[string]bool{
		"rm":        true,
		"rmdir":     true,
		"del":       true,
		"rd":        true,
		"rm -rf":    true,
		"rm -r":     true,
		"rm -f":     true,
		"format":    true,
		"mkfs":      true,
		"dd":        true,
		"shutdown":  true,
		"reboot":    true,
		"halt":      true,
		"poweroff":  true,
		"su":        true,
		"sudo":      true,
		"chmod":     true,
		"chown":     true,
		"mkfs.ext4": true,
		"mkfs.ntfs": true,
	}
	return &DangerousCommandFilter{
		blockedCommands: blocked,
	}
}

// IsCommandAllowed checks if a command is allowed
func (f *DangerousCommandFilter) IsCommandAllowed(command string, args []string) (bool, string) {
	cmdLower := strings.ToLower(strings.TrimSpace(command))

	// Check if the command itself is blocked
	if f.blockedCommands[cmdLower] {
		return false, fmt.Sprintf("command '%s' is blocked", command)
	}

	// Check for dangerous patterns in command + args
	fullCmd := command + " " + strings.Join(args, " ")
	fullCmdLower := strings.ToLower(fullCmd)

	for blocked := range f.blockedCommands {
		if strings.Contains(fullCmdLower, blocked) {
			return false, fmt.Sprintf("command pattern '%s' is blocked", blocked)
		}
	}

	// Check for dangerous argument patterns
	for _, arg := range args {
		argLower := strings.ToLower(arg)
		// Block recursive delete patterns
		if strings.Contains(argLower, "-rf") || strings.Contains(argLower, "-r") {
			if cmdLower == "rm" || cmdLower == "del" {
				return false, "recursive delete is blocked"
			}
		}
		// Block root/system paths
		if strings.HasPrefix(argLower, "/system") || strings.HasPrefix(argLower, "c:\\windows\\system") {
			return false, "access to system directories is blocked"
		}
	}

	return true, ""
}

// ExecuteCommand validates and optionally executes a command (validation only for now)
func (fs *ToolFS) ExecuteCommandWithSession(command string, args []string, session *Session) error {
	if session == nil {
		return errors.New("session required for command execution")
	}

	allowed, reason := session.ValidateCommand(command, args)
	if !allowed {
		err := fmt.Errorf("command not allowed: %s", reason)
		if session.AuditLogger != nil {
			session.logAudit("ExecuteCommand", command+" "+strings.Join(args, " "), false, err, 0, 0)
		}
		return err
	}

	// Log successful validation
	if session.AuditLogger != nil {
		session.logAudit("ExecuteCommand", command+" "+strings.Join(args, " "), true, nil, 0, 0)
	}

	// In a real implementation, you would execute the command here
	// For now, we just validate
	return nil
}

// GetSkillDocument retrieves a skill document by plugin name or path key
func (fs *ToolFS) GetSkillDocument(key string) (*SkillDocument, error) {
	if fs.skillDocManager == nil {
		return nil, errors.New("skill document manager not initialized")
	}
	return fs.skillDocManager.GetDocument(key)
}

// ListSkillDocuments returns all registered skill documents
func (fs *ToolFS) ListSkillDocuments() []*SkillDocument {
	if fs.skillDocManager == nil {
		return []*SkillDocument{}
	}
	return fs.skillDocManager.ListDocuments()
}

// ListSkillDocumentNames returns all registered skill document names/keys
func (fs *ToolFS) ListSkillDocumentNames() []string {
	if fs.skillDocManager == nil {
		return []string{}
	}
	return fs.skillDocManager.ListDocumentNames()
}

// GetSkillDocumentManager returns the skill document manager
func (fs *ToolFS) GetSkillDocumentManager() *SkillDocumentManager {
	return fs.skillDocManager
}

// RegisterPluginWithSkillDocs registers a plugin and its skill document
func (fs *ToolFS) RegisterPluginWithSkillDocs(plugin ToolFSPlugin) error {
	if fs.skillDocManager == nil {
		return errors.New("skill document manager not initialized")
	}

	// Register with skill document manager
	if err := fs.skillDocManager.RegisterPlugin(plugin); err != nil {
		return fmt.Errorf("failed to register plugin skill document: %w", err)
	}

	// If plugin manager exists, also register the plugin
	if fs.pluginManager != nil {
		// Try to get context from registry or create a default one
		ctx := &PluginContext{fs: fs, session: nil}
		if fs.pluginRegistry != nil {
			if pluginCtx, err := fs.pluginRegistry.GetContext(plugin.Name()); err == nil {
				ctx = pluginCtx
			}
		}
		if err := fs.pluginManager.InjectPlugin(plugin, ctx, nil); err != nil {
			// Log error but don't fail - plugin might already be registered
			_ = err
		}
	}

	return nil
}
