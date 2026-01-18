---
name: toolfs-plugin
description: Execute WASM-based plugins mounted to virtual paths. Use this skill when the user requests plugin execution such as "Execute the RAG plugin", "Run the analytics plugin", or "Call the plugin at this path".
metadata:
  author: toolfs
  version: "1.0.0"
  module: plugin
---

# ToolFS Plugin

Execute WASM-based plugins mounted to virtual paths. Plugins extend ToolFS functionality with custom handlers that can be mounted to specific paths and executed through the filesystem interface.

## How It Works

1. **Plugin Mounting**: Plugins are mounted to virtual paths like `/toolfs/<plugin_name>`
2. **WASM Execution**: Plugins run in a sandboxed WASM environment with resource limits
3. **Request/Response**: Plugins receive JSON requests and return JSON responses
4. **Resource Isolation**: Each plugin execution is isolated with configurable limits

## Usage

### Execute Plugin via Mounted Path

**ToolFS Path:**
```
/toolfs/<plugin_mount_path>?text=<query>&<param>=<value>
```

**Example:**
```json
GET /toolfs/rag/plugin/search?text=AI%20agents&top_k=5

// Response
{
  "success": true,
  "result": {
    "query": "AI agents",
    "top_k": 5,
    "results": [
      {
        "document": {
          "id": "doc-1",
          "content": "AI agents use tools to interact with their environment...",
          "metadata": {}
        },
        "score": 0.92
      }
    ],
    "count": 1
  },
  "metadata": {
    "plugin_name": "rag-plugin",
    "plugin_version": "1.0.0"
  }
}
```

### Execute Plugin via Skill API

**ToolFS Path:**
```
POST /toolfs/skills/execute
```

**Example:**
```json
POST /toolfs/skills/execute
Content-Type: application/json

{
  "operations": [
    {
      "type": "execute_plugin",
      "plugin_path": "/toolfs/rag",
      "query": "vector database",
      "plugin_data": {
        "top_k": 10
      }
    }
  ]
}

// Response
{
  "results": [
    {
      "type": "plugin",
      "source": "/toolfs/rag",
      "content": "...",
      "success": true,
      "plugin": {
        "name": "rag-plugin",
        "version": "1.0.0"
      }
    }
  ]
}
```

### List Plugins

**ToolFS Path:**
```
GET /toolfs/plugins
```

**Example:**
```json
GET /toolfs/plugins

// Response
{
  "plugins": [
    {
      "name": "rag-plugin",
      "version": "1.0.0",
      "mount_path": "/toolfs/rag",
      "source": "wasm"
    },
    {
      "name": "analytics-plugin",
      "version": "2.1.0",
      "mount_path": "/toolfs/analytics",
      "source": "injected"
    }
  ]
}
```

## Plugin Request Format

Plugins receive requests in JSON format:

```json
{
  "operation": "search",
  "path": "/toolfs/rag/query?text=AI",
  "data": {
    "query": "AI agents",
    "top_k": 5
  }
}
```

## Plugin Response Format

Plugins must return responses in JSON format:

```json
{
  "success": true,
  "result": {
    // Plugin-specific result data
  },
  "error": "error message if failed"
}
```

## When to Use This Skill

Use Plugin skill when you need to:

- **Extend Functionality**: Use custom plugins for specialized operations
- **RAG Queries**: Execute semantic search via RAG plugins
- **Data Processing**: Run analytics or data transformation plugins
- **Custom Handlers**: Access custom business logic via plugins

Common use cases:
- "Execute the RAG plugin to search documents"
- "Run the analytics plugin on this data"
- "Call the plugin at /toolfs/custom-handler"
- "List all available plugins"

## Plugin Types

Plugins can be:

- **WASM Plugins**: Compiled WebAssembly modules for sandboxed execution
- **Injected Plugins**: Native Go plugins injected at runtime
- **Mounted Plugins**: Plugins mounted to specific virtual paths

## Output Format

Plugin operations return standardized result structures:

```json
{
  "type": "plugin",
  "source": "/toolfs/<plugin_path>",
  "content": "plugin result data",
  "metadata": {
    "plugin_name": "...",
    "plugin_version": "..."
  },
  "success": true,
  "plugin": {
    "name": "rag-plugin",
    "version": "1.0.0",
    "output": {}
  },
  "error": "error message if failed"
}
```

## Present Results to User

When presenting plugin execution results:

```
✓ Plugin executed successfully

Plugin: rag-plugin v1.0.0
Path: /toolfs/rag/plugin/search

Results:
- Found 1 document matching "AI agents"
- Score: 0.92

Document: doc-1
Content: AI agents use tools to interact with their environment...
```

## Troubleshooting

### Plugin Not Found

If a plugin execution fails:

1. Verify the plugin is mounted at the specified path
2. Check plugin metadata via `GET /toolfs/plugins`
3. Ensure the plugin is properly loaded and initialized
4. Verify the plugin path is correct

### Plugin Execution Error

If plugin execution fails:

1. Check plugin logs for detailed error messages
2. Verify input parameters are correct
3. Ensure plugin has necessary resources (memory, time)
4. Check if plugin is compatible with current ToolFS version

### WASM Sandbox Limits

If execution hits resource limits:

1. Check plugin resource configuration
2. Verify plugin doesn't exceed memory/time limits
3. Optimize plugin code for resource efficiency
4. Adjust sandbox limits if appropriate

## Best Practices

1. **Verify Plugin Availability**: Check plugin list before execution
2. **Handle Errors**: Always check `success` field in plugin responses
3. **Use Metadata**: Leverage plugin metadata for version compatibility
4. **Resource Monitoring**: Monitor plugin resource usage
5. **Caching**: Cache plugin results when appropriate to reduce load

---

*This skill is part of ToolFS. See [main SKILL.md](../SKILL.md) for overview.*

