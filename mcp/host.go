package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Host manages multiple MCPClient instances based on the provided configuration.
// It's responsible for starting, stopping, and routing requests to the correct client.
type Host struct {
	config *HostConfig
	// clients maps the configured server name to its running MCPClient instance.
	clients    map[string]MCPClient
	clientsMu  sync.RWMutex // Protects the clients map
	logger     *slog.Logger
	runningCtx context.Context    // Context for managing client lifecycles
	cancelFunc context.CancelFunc // Function to stop all clients
}

// NewHost creates and initializes a new MCP Host.
// It loads the configuration, creates client instances, and starts them.
func NewHost(ctx context.Context, configPath string, logger *slog.Logger) (*Host, error) {
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("Loading MCP Host configuration", "path", configPath)
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load MCP host config: %w", err)
	}

	runningCtx, cancelFunc := context.WithCancel(ctx) // Create a context for managing clients

	h := &Host{
		config:     config,
		clients:    make(map[string]MCPClient),
		logger:     logger.With("component", "MCPHost"),
		runningCtx: runningCtx,
		cancelFunc: cancelFunc,
	}

	logger.Info("Initializing MCP clients...")
	var startErr error
	for name, serverConf := range config.Servers {
		clientLogger := h.logger.With("clientName", name, "transport", serverConf.Transport)
		h.logger.Info("Creating client", "name", name, "transport", serverConf.Transport)
		var client MCPClient
		switch serverConf.Transport {
		case "stdio":
			client = NewStdioClient(serverConf, clientLogger)
		default:
			// Should have been caught by LoadConfig, but double-check
			h.Shutdown() // Stop any clients already started
			return nil, fmt.Errorf("unsupported transport '%s' for server '%s'", serverConf.Transport, name)
		}

		h.clients[name] = client
		err := client.Start(h.runningCtx) // Use the host's running context
		if err != nil {
			h.logger.Error("Failed to start client", "name", name, "error", err)
			startErr = fmt.Errorf("failed to start client '%s': %w", name, err) // Store first error
			break                                                               // Stop initializing if one client fails
		}
		h.logger.Info("Client started successfully", "name", name)
	}

	if startErr != nil {
		h.logger.Error("Host initialization failed, shutting down started clients...")
		h.Shutdown() // Clean up successfully started clients
		return nil, startErr
	}

	h.logger.Info("MCP Host initialized successfully", "clients", len(h.clients))
	return h, nil
}

// Shutdown stops all managed MCP clients gracefully.
func (h *Host) Shutdown() {
	h.logger.Info("Shutting down MCP Host and clients...")
	// Cancel the context to signal clients/processes to stop
	if h.cancelFunc != nil {
		h.cancelFunc()
	}

	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()

	var wg sync.WaitGroup
	for name, client := range h.clients {
		wg.Add(1)
		go func(n string, c MCPClient) {
			defer wg.Done()
			h.logger.Info("Stopping client", "name", n)
			if err := c.Stop(); err != nil {
				h.logger.Error("Error stopping client", "name", n, "error", err)
			}
		}(name, client)
	}
	wg.Wait()                              // Wait for all stops to complete
	h.clients = make(map[string]MCPClient) // Clear the map
	h.logger.Info("MCP Host shutdown complete")
}

// GetAvailableTools retrieves the list of tools advertised by each connected client.
// It returns a map where keys are client names and values are lists of tools.
func (h *Host) GetAvailableTools(ctx context.Context, sessionID string) (map[string][]ToolDefinition, error) {
	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()

	allTools := make(map[string][]ToolDefinition)
	var firstErr error
	var wg sync.WaitGroup
	var mu sync.Mutex // Protects allTools and firstErr

	h.logger.InfoContext(ctx, "Requesting tools from all clients", "sessionID", sessionID)

	for name, client := range h.clients {
		wg.Add(1)
		go func(n string, c MCPClient) {
			defer wg.Done()
			tools, err := c.GetTools(ctx, sessionID) // Use the passed-in context for the request
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				h.logger.ErrorContext(ctx, "Failed to get tools from client", "name", n, "error", err, "sessionID", sessionID)
				if firstErr == nil {
					firstErr = fmt.Errorf("client '%s': %w", n, err)
				}
			} else {
				h.logger.InfoContext(ctx, "Received tools from client", "name", n, "count", len(tools), "sessionID", sessionID)
				if len(tools) > 0 { // Only add if tools are reported
					allTools[n] = tools
				}
			}
		}(name, client)
	}

	wg.Wait() // Wait for all GetTools calls to complete

	if firstErr != nil {
		// Return partial results along with the first error encountered
		return allTools, fmt.Errorf("failed to get tools from one or more clients: %w", firstErr)
	}

	h.logger.InfoContext(ctx, "Successfully retrieved tools from clients", "sessionID", sessionID, "clientCount", len(allTools))
	return allTools, nil
}

// ExecuteTool routes a tool execution request to the specified client.
func (h *Host) ExecuteTool(ctx context.Context, sessionID string, clientName string, call ToolCall) (*ToolResult, error) {
	h.clientsMu.RLock()
	client, ok := h.clients[clientName]
	h.clientsMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no client found with name '%s'", clientName)
	}

	h.logger.InfoContext(ctx, "Executing tool via client",
		"sessionID", sessionID,
		"clientName", clientName,
		"toolName", call.Name,
		"toolCallID", call.ID)

	result, err := client.ExecuteTool(ctx, sessionID, call) // Use the passed-in context for the request
	if err != nil {
		h.logger.ErrorContext(ctx, "Tool execution failed",
			"sessionID", sessionID,
			"clientName", clientName,
			"toolName", call.Name,
			"toolCallID", call.ID,
			"error", err)
		return nil, fmt.Errorf("client '%s' failed to execute tool '%s': %w", clientName, call.Name, err)
	}

	h.logger.InfoContext(ctx, "Tool execution successful",
		"sessionID", sessionID,
		"clientName", clientName,
		"toolName", call.Name,
		"toolCallID", call.ID,
		"toolResultCallID", result.CallID)

	return result, nil
}
