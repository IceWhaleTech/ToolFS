package toolfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// Result represents a structured result from a skill API operation
type Result struct {
	Type      string      `json:"type"`                 // "memory", "rag", "file", "cli", "plugin"
	Source    string      `json:"source"`               // Source identifier (ID, path, command, plugin_name)
	Content   string      `json:"content"`              // The actual content/data
	Metadata  interface{} `json:"metadata"`             // Additional metadata
	Success   bool        `json:"success"`              // Operation success status
	Error     string      `json:"error,omitempty"`      // Error message if failed
	CLIOutput *CLIOutput  `json:"cli_output,omitempty"` // CLI command output if applicable
	Plugin    *PluginInfo `json:"plugin,omitempty"`     // Plugin information if applicable
}

// PluginInfo contains information about a plugin execution
type PluginInfo struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Output  interface{} `json:"output,omitempty"` // Plugin output (can be structured)
}

// CLIOutput represents the output from a CLI command execution
type CLIOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
}

// SearchMemoryAndOpenFile combines multiple ToolFS operations:
// 1. Searches memory for entries matching the query
// 2. Performs RAG lookup if needed
// 3. Accesses local filesystem at the specified path
// 4. Optionally executes CLI commands safely
func SearchMemoryAndOpenFile(fs *ToolFS, query string, path string, session *Session) (*Result, error) {
	var result *Result

	// Step 1: Search memory for entries matching the query
	memoryResults, memErr := searchMemory(fs, query, session)
	if memErr == nil && len(memoryResults) > 0 {
		// Found in memory, return memory result
		result = &Result{
			Type:     "memory",
			Source:   memoryResults[0].ID,
			Content:  memoryResults[0].Content,
			Metadata: memoryResults[0].Metadata,
			Success:  true,
		}
		return result, nil
	}

	// Step 2: If not in memory, try RAG lookup
	ragResults, ragErr := searchRAG(fs, query, 3, session)
	if ragErr == nil && len(ragResults.Results) > 0 {
		// Found in RAG, now try to access the file
		bestMatch := ragResults.Results[0]
		result = &Result{
			Type:     "rag",
			Source:   bestMatch.ID,
			Content:  bestMatch.Content,
			Metadata: bestMatch.Metadata,
			Success:  true,
		}

		// If path is provided, also try to read the file
		if path != "" {
			fileResult := tryReadFile(fs, path, session)
			if fileResult != nil && fileResult.Success {
				// File read successfully, combine with RAG result
				result.Metadata = map[string]interface{}{
					"rag": map[string]interface{}{
						"score": bestMatch.Score,
						"id":    bestMatch.ID,
					},
					"file": map[string]interface{}{
						"path": path,
						"size": len(fileResult.Content),
					},
				}
			}
		}
		return result, nil
	}

	// Step 3: Try to access local filesystem directly
	if path != "" {
		fileResult := tryReadFile(fs, path, session)
		if fileResult != nil && fileResult.Success {
			return fileResult, nil
		}
	}

	// Step 4: If all else fails, return error with what we found
	return &Result{
		Type:    "error",
		Success: false,
		Error:   fmt.Sprintf("could not find content: memory error: %v, rag error: %v", memErr, ragErr),
	}, errors.New("no results found in memory, RAG, or filesystem")
}

// searchMemory searches memory entries for a query string
func searchMemory(fs *ToolFS, query string, session *Session) ([]MemoryEntry, error) {
	if fs.memoryStore == nil {
		return nil, errors.New("memory store not available")
	}

	// List all memory entries
	ids, err := fs.memoryStore.List()
	if err != nil {
		return nil, err
	}

	var matches []MemoryEntry
	queryLower := strings.ToLower(query)

	// Simple text matching (in a real implementation, this would be semantic search)
	for _, id := range ids {
		entry, err := fs.memoryStore.Get(id)
		if err != nil {
			continue
		}

		contentLower := strings.ToLower(entry.Content)
		if strings.Contains(contentLower, queryLower) {
			matches = append(matches, *entry)
		}
	}

	if len(matches) == 0 {
		return nil, errors.New("no memory entries found")
	}

	return matches, nil
}

// searchRAG performs a RAG search
func searchRAG(fs *ToolFS, query string, topK int, session *Session) (*RAGSearchResults, error) {
	if fs.ragStore == nil {
		return nil, errors.New("RAG store not available")
	}

	ragPath := fmt.Sprintf("/toolfs/rag/query?text=%s&top_k=%d",
		strings.ReplaceAll(url.QueryEscape(query), "+", "%20"), topK)

	var data []byte
	var err error
	if session != nil {
		data, err = fs.ReadFileWithSession(ragPath, session)
	} else {
		data, err = fs.ReadFile(ragPath)
	}

	if err != nil {
		return nil, err
	}

	var results RAGSearchResults
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, err
	}

	return &results, nil
}

// tryReadFile attempts to read a file and returns a Result
func tryReadFile(fs *ToolFS, path string, session *Session) *Result {
	var data []byte
	var err error

	if session != nil {
		data, err = fs.ReadFileWithSession(path, session)
	} else {
		data, err = fs.ReadFile(path)
	}

	if err != nil {
		return &Result{
			Type:    "file",
			Source:  path,
			Success: false,
			Error:   err.Error(),
		}
	}

	return &Result{
		Type:    "file",
		Source:  path,
		Content: string(data),
		Success: true,
	}
}

// ExecuteCLI safely executes a CLI command and captures stdout/stderr
func ExecuteCLI(command string, args []string, session *Session, fs *ToolFS) (*Result, error) {
	if session != nil {
		// Validate command if session has a validator
		allowed, reason := session.ValidateCommand(command, args)
		if !allowed {
			return &Result{
				Type:    "cli",
				Source:  command,
				Success: false,
				Error:   fmt.Sprintf("command not allowed: %s", reason),
			}, fmt.Errorf("command not allowed: %s", reason)
		}

		// Log command execution attempt
		if session.AuditLogger != nil {
			// This will be logged by ExecuteCommandWithSession if we use it
		}
	}

	// Execute the command
	cmd := exec.Command(command, args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			return &Result{
				Type:    "cli",
				Source:  command,
				Success: false,
				Error:   err.Error(),
			}, err
		}
	}

	fullCommand := command
	if len(args) > 0 {
		fullCommand = command + " " + strings.Join(args, " ")
	}

	result := &Result{
		Type:    "cli",
		Source:  fullCommand,
		Content: stdout.String(),
		Success: exitCode == 0,
		CLIOutput: &CLIOutput{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
			Command:  fullCommand,
		},
	}

	if exitCode != 0 {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = fmt.Sprintf("command exited with code %d", exitCode)
		}
	}

	// Log audit entry if session is provided
	if session != nil && session.AuditLogger != nil {
		session.logAudit("ExecuteCLI", fullCommand, result.Success,
			errors.New(result.Error), int64(len(stdout.String())), 0)
	}

	return result, nil
}

// ChainOperations executes multiple ToolFS operations in sequence
func ChainOperations(fs *ToolFS, operations []Operation, session *Session) ([]*Result, error) {
	var results []*Result

	for _, op := range operations {
		var result *Result
		var err error

		switch op.Type {
		case "read_file":
			result = tryReadFile(fs, op.Path, session)
		case "write_file":
			if session != nil {
				err = fs.WriteFileWithSession(op.Path, []byte(op.Content), session)
			} else {
				err = fs.WriteFile(op.Path, []byte(op.Content))
			}
			if err != nil {
				result = &Result{
					Type:    "file",
					Source:  op.Path,
					Success: false,
					Error:   err.Error(),
				}
			} else {
				result = &Result{
					Type:    "file",
					Source:  op.Path,
					Content: op.Content,
					Success: true,
				}
			}
		case "list_dir":
			var entries []string
			if session != nil {
				entries, err = fs.ListDirWithSession(op.Path, session)
			} else {
				entries, err = fs.ListDir(op.Path)
			}
			if err != nil {
				result = &Result{
					Type:    "file",
					Source:  op.Path,
					Success: false,
					Error:   err.Error(),
				}
			} else {
				result = &Result{
					Type:    "file",
					Source:  op.Path,
					Content: strings.Join(entries, "\n"),
					Success: true,
				}
			}
		case "search_memory":
			var entries []MemoryEntry
			entries, err = searchMemory(fs, op.Query, session)
			if err != nil {
				result = &Result{
					Type:    "memory",
					Success: false,
					Error:   err.Error(),
				}
			} else if len(entries) > 0 {
				result = &Result{
					Type:     "memory",
					Source:   entries[0].ID,
					Content:  entries[0].Content,
					Metadata: entries[0].Metadata,
					Success:  true,
				}
			}
		case "search_rag":
			var ragResults *RAGSearchResults
			ragResults, err = searchRAG(fs, op.Query, op.TopK, session)
			if err != nil {
				result = &Result{
					Type:    "rag",
					Success: false,
					Error:   err.Error(),
				}
			} else if len(ragResults.Results) > 0 {
				result = &Result{
					Type:     "rag",
					Source:   ragResults.Results[0].ID,
					Content:  ragResults.Results[0].Content,
					Metadata: ragResults.Results[0].Metadata,
					Success:  true,
				}
			}
		case "execute_cli":
			result, err = ExecuteCLI(op.Command, op.Args, session, fs)
		case "execute_plugin":
			result, err = ExecutePlugin(fs, op.PluginName, op.PluginPath, op.Query, op.PluginData, session)
		default:
			result = &Result{
				Type:    "error",
				Success: false,
				Error:   fmt.Sprintf("unknown operation type: %s", op.Type),
			}
		}

		results = append(results, result)
	}

	return results, nil
}

// ExecutePlugin executes a plugin through ToolFS mount or PluginManager
func ExecutePlugin(fs *ToolFS, pluginName, pluginPath, query string, pluginData map[string]interface{}, session *Session) (*Result, error) {
	// If pluginPath is provided, use it directly (plugin is mounted)
	if pluginPath != "" {
		return executeMountedPlugin(fs, pluginPath, query, pluginData, session)
	}

	// Otherwise, try to use PluginManager
	if fs.pluginManager == nil {
		return &Result{
			Type:    "plugin",
			Source:  pluginName,
			Success: false,
			Error:   "plugin manager not available",
		}, errors.New("plugin manager not available")
	}

	// Build plugin request
	request := &PluginRequest{
		Operation: "read_file",
		Data:      make(map[string]interface{}),
	}

	if query != "" {
		request.Data["query"] = query
		request.Data["input"] = query
	}

	// Merge pluginData
	if pluginData != nil {
		for k, v := range pluginData {
			request.Data[k] = v
		}
	}

	// Marshal request to JSON
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return &Result{
			Type:    "plugin",
			Source:  pluginName,
			Success: false,
			Error:   fmt.Sprintf("failed to marshal request: %v", err),
		}, err
	}

	// Execute plugin (returns []byte, not PluginResponse)
	outputBytes, err := fs.pluginManager.ExecutePlugin(pluginName, requestBytes)
	if err != nil {
		return &Result{
			Type:    "plugin",
			Source:  pluginName,
			Success: false,
			Error:   err.Error(),
		}, err
	}

	// Parse plugin response
	var response PluginResponse
	if err := json.Unmarshal(outputBytes, &response); err != nil {
		// If not JSON, treat as plain content
		var pluginInfo *PluginInfo
		if plugin, err2 := fs.pluginManager.registry.Get(pluginName); err2 == nil {
			pluginInfo = &PluginInfo{
				Name:    plugin.Name(),
				Version: plugin.Version(),
			}
		}
		return &Result{
			Type:    "plugin",
			Source:  pluginName,
			Content: string(outputBytes),
			Success: true,
			Plugin:  pluginInfo,
		}, nil
	}

	if !response.Success {
		return &Result{
			Type:    "plugin",
			Source:  pluginName,
			Success: false,
			Error:   response.Error,
		}, errors.New(response.Error)
	}

	// Extract content from plugin response
	content := ""
	if resultStr, ok := response.Result.(string); ok {
		content = resultStr
	} else {
		// Marshal to JSON if not string
		if contentBytes, err := json.Marshal(response.Result); err == nil {
			content = string(contentBytes)
		}
	}

	// Get plugin info if available
	var pluginInfo *PluginInfo
	if plugin, err := fs.pluginManager.registry.Get(pluginName); err == nil {
		pluginInfo = &PluginInfo{
			Name:    plugin.Name(),
			Version: plugin.Version(),
			Output:  response.Result,
		}
	}

	return &Result{
		Type:     "plugin",
		Source:   pluginName,
		Content:  content,
		Metadata: response.Metadata,
		Success:  true,
		Plugin:   pluginInfo,
	}, nil
}

// executeMountedPlugin executes a plugin mounted to a path
func executeMountedPlugin(fs *ToolFS, pluginPath, query string, pluginData map[string]interface{}, session *Session) (*Result, error) {
	// Build full path with query
	path := pluginPath
	if query != "" {
		if strings.Contains(path, "?") {
			path += "&text=" + url.QueryEscape(query)
		} else {
			path += "?text=" + url.QueryEscape(query)
		}
	}

	// Read from plugin mount
	var data []byte
	var err error
	if session != nil {
		data, err = fs.ReadFileWithSession(path, session)
	} else {
		data, err = fs.ReadFile(path)
	}

	if err != nil {
		return &Result{
			Type:    "plugin",
			Source:  pluginPath,
			Success: false,
			Error:   err.Error(),
		}, err
	}

	// Parse plugin response
	var pluginResponse PluginResponse
	if err := json.Unmarshal(data, &pluginResponse); err != nil {
		// If not JSON, treat as plain content
		return &Result{
			Type:    "plugin",
			Source:  pluginPath,
			Content: string(data),
			Success: true,
		}, nil
	}

	if !pluginResponse.Success {
		return &Result{
			Type:    "plugin",
			Source:  pluginPath,
			Success: false,
			Error:   pluginResponse.Error,
		}, errors.New(pluginResponse.Error)
	}

	// Extract content
	content := ""
	if resultStr, ok := pluginResponse.Result.(string); ok {
		content = resultStr
	} else {
		contentBytes, _ := json.Marshal(pluginResponse.Result)
		content = string(contentBytes)
	}

	return &Result{
		Type:     "plugin",
		Source:   pluginPath,
		Content:  content,
		Metadata: pluginResponse.Metadata,
		Success:  true,
	}, nil
}

// SearchMemoryAndExecutePlugin combines memory search, RAG lookup, and plugin execution:
// 1. Searches memory for entries matching the query
// 2. Performs RAG lookup if needed
// 3. Executes plugin at the specified path with the query
// 4. Merges results into a structured JSON response
func SearchMemoryAndExecutePlugin(fs *ToolFS, query string, pluginPath string, session *Session) (*Result, error) {
	var allResults []*Result

	// Step 1: Search memory
	memoryResults, memErr := searchMemory(fs, query, session)
	if memErr == nil && len(memoryResults) > 0 {
		memoryResult := &Result{
			Type:     "memory",
			Source:   memoryResults[0].ID,
			Content:  memoryResults[0].Content,
			Metadata: memoryResults[0].Metadata,
			Success:  true,
		}
		allResults = append(allResults, memoryResult)
	}

	// Step 2: Try RAG lookup
	ragResults, ragErr := searchRAG(fs, query, 3, session)
	if ragErr == nil && len(ragResults.Results) > 0 {
		bestMatch := ragResults.Results[0]
		ragResult := &Result{
			Type:     "rag",
			Source:   bestMatch.ID,
			Content:  bestMatch.Content,
			Metadata: bestMatch.Metadata,
			Success:  true,
		}
		allResults = append(allResults, ragResult)
	}

	// Step 3: Execute plugin
	var pluginResult *Result
	var pluginErr error

	if pluginPath != "" {
		// Extract plugin name from path (e.g., "/toolfs/rag" -> check for mounted plugin)
		pluginResult, pluginErr = executeMountedPlugin(fs, pluginPath, query, nil, session)

		if pluginErr == nil && pluginResult != nil && pluginResult.Success {
			allResults = append(allResults, pluginResult)
		}
	}

	// Step 4: Merge results
	return mergeResults(allResults, query, memErr, ragErr, pluginErr)
}

// mergeResults merges multiple results into a single structured result
func mergeResults(results []*Result, query string, memErr, ragErr, pluginErr error) (*Result, error) {
	if len(results) == 0 {
		return &Result{
			Type:    "error",
			Success: false,
			Error:   fmt.Sprintf("no results found: memory=%v, rag=%v, plugin=%v", memErr, ragErr, pluginErr),
		}, errors.New("no results found")
	}

	// Use the first successful result as primary
	primary := results[0]

	// Build merged metadata
	mergedMetadata := map[string]interface{}{
		"query":         query,
		"sources_found": len(results),
		"source_types":  make([]string, 0, len(results)),
		"all_results":   make([]map[string]interface{}, 0, len(results)),
	}

	for _, r := range results {
		mergedMetadata["source_types"] = append(mergedMetadata["source_types"].([]string), r.Type)

		resultMap := map[string]interface{}{
			"type":    r.Type,
			"source":  r.Source,
			"content": r.Content,
			"success": r.Success,
		}

		if r.Metadata != nil {
			resultMap["metadata"] = r.Metadata
		}
		if r.Plugin != nil {
			resultMap["plugin"] = map[string]interface{}{
				"name":    r.Plugin.Name,
				"version": r.Plugin.Version,
			}
		}

		mergedMetadata["all_results"] = append(mergedMetadata["all_results"].([]map[string]interface{}), resultMap)
	}

	// If we have multiple results, combine content
	combinedContent := primary.Content
	if len(results) > 1 {
		var parts []string
		for i, r := range results {
			parts = append(parts, fmt.Sprintf("=== Source %d: %s (%s) ===\n%s",
				i+1, r.Type, r.Source, r.Content))
		}
		combinedContent = strings.Join(parts, "\n\n")
	}

	return &Result{
		Type:     primary.Type,
		Source:   primary.Source,
		Content:  combinedContent,
		Metadata: mergedMetadata,
		Success:  true,
		Plugin:   primary.Plugin,
	}, nil
}

// Operation represents a single operation in a chain
type Operation struct {
	Type       string                 `json:"type"` // "read_file", "write_file", "list_dir", "search_memory", "search_rag", "execute_cli", "execute_plugin"
	Path       string                 `json:"path,omitempty"`
	Content    string                 `json:"content,omitempty"`
	Query      string                 `json:"query,omitempty"`
	TopK       int                    `json:"top_k,omitempty"`
	Command    string                 `json:"command,omitempty"`
	Args       []string               `json:"args,omitempty"`
	PluginName string                 `json:"plugin_name,omitempty"` // For execute_plugin
	PluginPath string                 `json:"plugin_path,omitempty"` // Plugin mount path
	PluginData map[string]interface{} `json:"plugin_data,omitempty"` // Data to pass to plugin
}
