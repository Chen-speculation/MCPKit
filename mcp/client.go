package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// ToolParameter defines a parameter for a tool.
// TODO: Define more detailed parameter types (string, number, boolean, object, array).
type ToolParameter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // e.g., "string", "number", "boolean"
	Required    bool   `json:"required"`
}

// ToolDefinition defines a tool that an MCP client can execute.
// This structure is inspired by OpenAI's function calling.
type ToolDefinition struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Parameters  map[string]ToolParameter `json:"parameters"` // Using map for potential schema generation
}

// ToolCall represents a request from the LLM/Agent to execute a tool.
type ToolCall struct {
	ID        string          `json:"id"` // Unique ID for the call
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // Keep as raw JSON initially
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	CallID  string      `json:"callId"`          // ID of the corresponding ToolCall
	Content interface{} `json:"content"`         // Result data
	Error   string      `json:"error,omitempty"` // Error message if execution failed
}

// MCPMessage represents a message exchanged over the MCP transport (e.g., stdio).
// Based on the spec: https://github.com/modelcontext/modelcontextprotocol
type MCPMessage struct {
	Type       string           `json:"type"` // e.g., "request_tools", "tool_call", "tool_result", "error"
	SessionID  string           `json:"sessionId,omitempty"`
	RequestID  string           `json:"requestId,omitempty"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolCall   *ToolCall        `json:"toolCall,omitempty"`
	ToolResult *ToolResult      `json:"toolResult,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// MCPClient defines the interface for interacting with a specific MCP server process.
type MCPClient interface {
	// Start initiates the client connection (e.g., starts the process for stdio).
	Start(ctx context.Context) error
	// Stop terminates the client connection.
	Stop() error
	// GetTools requests the list of available tools from the server.
	// It sends a 'request_tools' message and waits for a 'tools' response.
	GetTools(ctx context.Context, sessionID string) ([]ToolDefinition, error)
	// ExecuteTool sends a tool call request to the server and waits for the result.
	ExecuteTool(ctx context.Context, sessionID string, call ToolCall) (*ToolResult, error)
}

// stdioClient implements MCPClient using standard input/output to communicate with a subprocess.
type stdioClient struct {
	config MCPServerConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser // Capture stderr for logging/debugging
	reader *bufio.Reader
	writer *json.Encoder
	log    *slog.Logger

	mu         sync.Mutex    // Protects access to shared resources (stdin/stdout/reqCounter)
	requestMap sync.Map      // Stores pending requests: map[requestID]chan *MCPMessage
	reqCounter atomic.Uint64 // Simple counter for request IDs
}

// NewStdioClient creates a new client for stdio communication.
func NewStdioClient(config MCPServerConfig, logger *slog.Logger) MCPClient {
	if logger == nil {
		logger = slog.Default().With("clientType", "stdio", "command", config.Command)
	}
	return &stdioClient{
		config: config,
		log:    logger,
	}
}

// Start launches the subprocess and sets up stdio pipes.
func (c *stdioClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd != nil && c.cmd.Process != nil {
		return fmt.Errorf("stdio client for %s already started", c.config.Command)
	}

	c.log.InfoContext(ctx, "Starting MCP server process", "command", c.config.Command, "args", c.config.Args)
	c.cmd = exec.CommandContext(ctx, c.config.Command, c.config.Args...)
	c.cmd.Dir = c.config.WorkingDir
	c.cmd.Env = append(c.cmd.Environ(), c.config.Env...)

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe for %s: %w", c.config.Command, err)
	}
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe for %s: %w", c.config.Command, err)
	}
	c.stderr, err = c.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe for %s: %w", c.config.Command, err)
	}

	c.reader = bufio.NewReader(c.stdout)
	c.writer = json.NewEncoder(c.stdin)

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command %s: %w", c.config.Command, err)
	}

	c.log.InfoContext(ctx, "MCP server process started", "pid", c.cmd.Process.Pid)

	// Start goroutine to read stdout
	go c.readOutput(ctx)
	// Start goroutine to read stderr
	go c.readStdErr(ctx)
	// Start goroutine to wait for process exit
	go c.waitExit(ctx)

	return nil
}

// Stop terminates the subprocess.
func (c *stdioClient) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd == nil || c.cmd.Process == nil {
		c.log.Warn("Stop called but process not running or already stopped")
		return nil
	}

	c.log.Info("Stopping MCP server process", "pid", c.cmd.Process.Pid)

	// Close stdin first to signal the process
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}

	// Attempt graceful termination first
	err := c.cmd.Process.Signal(os.Interrupt) // or os.Kill on Windows? Need platform specifics.
	if err != nil {
		c.log.Warn("Failed to send interrupt signal, attempting kill", "error", err)
		err = c.cmd.Process.Kill()
	}

	// Wait should release resources even if signal fails
	_ = c.cmd.Wait() // Ensure process is reaped

	c.cmd = nil
	c.stdin = nil
	c.stdout = nil
	c.stderr = nil
	c.reader = nil
	c.writer = nil

	if err != nil {
		return fmt.Errorf("failed to kill process %s: %w", c.config.Command, err)
	}
	c.log.Info("MCP server process stopped")
	return nil
}

// readOutput continuously reads messages from the process's stdout.
func (c *stdioClient) readOutput(ctx context.Context) {
	decoder := json.NewDecoder(c.reader)
	for {
		var msg MCPMessage
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				c.log.InfoContext(ctx, "Stdout pipe closed for MCP server")
				// TODO: Maybe notify the host? Or handle reconnection if applicable?
				return
			}
			c.log.ErrorContext(ctx, "Failed to decode MCP message from stdout", "error", err)
			// If decoding fails consistently, the stream might be corrupted. Stop reading.
			// Consider how to handle non-JSON output or malformed messages.
			return
		}

		c.log.DebugContext(ctx, "Received MCP message", "type", msg.Type, "requestId", msg.RequestID)

		// Dispatch message to waiting request or handle unsolicited messages
		if msg.RequestID != "" {
			if ch, ok := c.requestMap.Load(msg.RequestID); ok {
				select {
				case ch.(chan *MCPMessage) <- &msg:
					c.log.DebugContext(ctx, "Dispatched message to waiting request", "requestId", msg.RequestID)
				case <-ctx.Done():
					return // Context cancelled
				}
			} else {
				c.log.WarnContext(ctx, "Received message for unknown or timed-out request", "requestId", msg.RequestID)
			}
		} else {
			// Handle messages without a request ID (e.g., notifications from server)
			c.log.InfoContext(ctx, "Received unsolicited MCP message", "type", msg.Type)
			// TODO: Implement handling for unsolicited messages if needed
		}
	}
}

// readStdErr continuously reads and logs stderr output from the process.
func (c *stdioClient) readStdErr(ctx context.Context) {
	reader := bufio.NewReader(c.stderr)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			c.log.WarnContext(ctx, "MCP server stderr", "output", line)
		}
		if err != nil {
			if err != io.EOF && err != io.ErrClosedPipe {
				c.log.ErrorContext(ctx, "Error reading MCP server stderr", "error", err)
			}
			return // Pipe closed or error
		}
	}
}

// waitExit waits for the process to exit and logs the event.
func (c *stdioClient) waitExit(ctx context.Context) {
	if c.cmd == nil {
		return
	}
	err := c.cmd.Wait()
	c.log.InfoContext(ctx, "MCP server process exited", "error", err)
	// TODO: Clean up resources or notify host upon exit?
	// Ensure Stop() is called or handles this state correctly.
	// Maybe trigger cleanup of pending requests.
}

// sendRequest sends an MCP message and waits for a reply with a matching RequestID.
func (c *stdioClient) sendRequest(ctx context.Context, msg *MCPMessage) (*MCPMessage, error) {
	c.mu.Lock()
	if c.writer == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("client not started or already stopped")
	}
	c.mu.Unlock()

	// Generate unique request ID using an atomic counter
	requestID := fmt.Sprintf("req-%d", c.reqCounter.Add(1))
	msg.RequestID = requestID

	// Create channel to receive the response
	replyChan := make(chan *MCPMessage, 1)
	c.requestMap.Store(requestID, replyChan)
	defer c.requestMap.Delete(requestID)

	c.log.DebugContext(ctx, "Sending MCP request", "type", msg.Type, "requestId", requestID)

	// Send the request
	c.mu.Lock()
	err := c.writer.Encode(msg)
	c.mu.Unlock() // Unlock immediately after write
	if err != nil {
		return nil, fmt.Errorf("failed to send MCP request (%s): %w", msg.Type, err)
	}

	// Wait for the response or timeout/cancellation
	select {
	case reply := <-replyChan:
		c.log.DebugContext(ctx, "Received MCP reply", "type", reply.Type, "requestId", reply.RequestID)
		if reply.Type == "error" {
			return nil, fmt.Errorf("MCP server returned error: %s", reply.Error)
		}
		return reply, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled while waiting for MCP reply (%s): %w", msg.Type, ctx.Err())
		// TODO: Add a reasonable timeout?
	}
}

// GetTools sends a 'request_tools' message and returns the list of tools.
func (c *stdioClient) GetTools(ctx context.Context, sessionID string) ([]ToolDefinition, error) {
	request := &MCPMessage{
		Type:      "request_tools",
		SessionID: sessionID,
	}

	reply, err := c.sendRequest(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}

	if reply.Type != "tools" {
		return nil, fmt.Errorf("unexpected reply type for request_tools: expected 'tools', got '%s'", reply.Type)
	}

	return reply.Tools, nil
}

// ExecuteTool sends a 'tool_call' message and returns the result.
func (c *stdioClient) ExecuteTool(ctx context.Context, sessionID string, call ToolCall) (*ToolResult, error) {
	request := &MCPMessage{
		Type:      "tool_call",
		SessionID: sessionID,
		ToolCall:  &call,
	}

	reply, err := c.sendRequest(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to execute tool '%s': %w", call.Name, err)
	}

	if reply.Type != "tool_result" {
		return nil, fmt.Errorf("unexpected reply type for tool_call: expected 'tool_result', got '%s'", reply.Type)
	}
	if reply.ToolResult == nil {
		return nil, fmt.Errorf("received 'tool_result' message but ToolResult field is nil")
	}

	return reply.ToolResult, nil
}
