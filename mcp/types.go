package mcp

import (
	"context"
)

// Engine defines the interface for interacting with a language model
// and managing MCP tool interactions.
type Engine interface {
	// ChatStream handles streaming chat, sending prompt and history to the LLM,
	// managing tool discovery via MCP, handling LLM tool calls, executing them via MCP,
	// and streaming back text, tool calls, tool results, and errors.
	ChatStream(ctx context.Context, sessionID string, prompt string, history []Message, outputChan chan<- ChatEvent) error

	// ListTools returns the combined list of tool definitions available from all configured MCP servers.
	ListTools() ([]ToolDefinition, error)

	// Name returns the identifier for the specific engine implementation (e.g., "openai").
	Name() string

	// Close gracefully shuts down the engine and its associated MCP clients.
	Close() error
}

// Message represents a single message in a chat conversation.
type Message struct {
	Role       string     `json:"role"`                   // Sender role: "user", "assistant", "system", "tool"
	Content    string     `json:"content"`                // Text content of the message
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // Tool calls requested by the assistant
	ToolCallID string     `json:"tool_call_id,omitempty"` // ID of the tool call this message is a result for (for role="tool")
}

// ToolCall represents a request by the LLM to call a specific tool.
// This structure mirrors the essential parts needed from the LLM's perspective.
type ToolCall struct {
	ID        string                 `json:"id"`        // Unique ID for this specific tool call instance
	Name      string                 `json:"name"`      // Name of the tool to be called (potentially prefixed with server name, e.g., "server__tool")
	Arguments map[string]interface{} `json:"arguments"` // Arguments for the tool, structured as a map
}

// ToolDefinition represents the structure defining a tool, understandable by the LLM.
// Based on OpenAI's function definition structure.
type ToolDefinition struct {
	Name        string `json:"name"`        // Tool name (potentially prefixed, e.g., "server__tool")
	Description string `json:"description"` // Description of what the tool does
	Schema      Schema `json:"schema"`      // Input parameter schema for the tool
}

// Schema defines the input parameters for a tool, following JSON Schema conventions.
type Schema struct {
	Type       string                 `json:"type"`                 // Typically "object"
	Properties map[string]interface{} `json:"properties,omitempty"` // Map of parameter names to their definitions
	Required   []string               `json:"required,omitempty"`   // List of required parameter names
}

// ChatEvent represents an event occurring during a chat stream.
type ChatEvent struct {
	Type       string      `json:"type"`                  // Type: "text", "tool_call", "tool_result", "session_id", "error", "finish"
	Content    string      `json:"content,omitempty"`     // Text chunk (for type="text")
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`   // Details of the tool call requested by LLM (for type="tool_call")
	ToolResult *ToolResult `json:"tool_result,omitempty"` // Details of the result from an executed tool (for type="tool_result")
	SessionID  string      `json:"session_id,omitempty"`  // Session ID for the chat (for type="session_id")
	Error      string      `json:"error,omitempty"`       // Error message (for type="error")
}

// ToolResult represents the outcome of an executed tool call.
type ToolResult struct {
	CallID  string `json:"callId"`  // ID of the corresponding ToolCall
	Name    string `json:"name"`    // Name of the tool that was called (e.g., "server__tool")
	Content string `json:"content"` // Result content (string representation)
	IsError bool   `json:"isError"` // Flag indicating if the content represents an error message
}

// --- Configuration Types ---

// EngineConfig holds the configuration for the MCP Engine.
type EngineConfig struct {
	DefaultModel  string                  // Default LLM model name (e.g., "gpt-4o")
	OpenAIAPIKey  string                  // API Key for OpenAI
	OpenAIBaseURL string                  // Optional: Base URL for OpenAI compatible API
	MCPServers    map[string]ServerConfig // Map of MCP server names to their configurations
}

// ServerConfig is an interface for different MCP server connection types.
type ServerConfig interface {
	GetType() string
	GetName() string // Get the logical name assigned to this server config
}

// BaseServerConfig provides common fields for server configurations.
type BaseServerConfig struct {
	Name string `json:"-"`              // Logical name, injected during parsing
	Type string `json:"type,omitempty"` // Server type (e.g., "process", "sse")
}

func (c *BaseServerConfig) GetName() string {
	return c.Name
}

// ProcessServerConfig defines configuration for an MCP server connected via stdio.
type ProcessServerConfig struct {
	BaseServerConfig
	Command    string   `json:"command"`              // Command to execute
	Args       []string `json:"args,omitempty"`       // Arguments for the command
	WorkingDir string   `json:"workingDir,omitempty"` // Working directory for the command
	Env        []string `json:"env,omitempty"`        // Additional environment variables
}

// GetType returns the server type ("process").
func (c *ProcessServerConfig) GetType() string {
	if c.Type == "" {
		return "process"
	}
	return c.Type
}

// SSEServerConfig defines configuration for an MCP server connected via Server-Sent Events (SSE).
type SSEServerConfig struct {
	BaseServerConfig
	URL     string            `json:"url"`               // URL of the SSE endpoint
	Headers map[string]string `json:"headers,omitempty"` // Optional headers (e.g., Authorization)
}

// GetType returns the server type ("sse").
func (c *SSEServerConfig) GetType() string {
	if c.Type == "" {
		return "sse"
	}
	return c.Type
}
