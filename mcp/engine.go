package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	log "github.com/sirupsen/logrus"
)

// mcpEngine is the concrete implementation holding the configured provider.
type mcpEngine struct {
	provider Engine // Currently, this will be *openaiProvider
}

// NewEngine creates and initializes a new MCP engine based on the provided configuration.
// It sets up MCP clients for the configured servers and instantiates the LLM provider (currently OpenAI).
func NewEngine(config EngineConfig) (Engine, error) {
	// Initialize MCP Clients based on config.MCPServers
	mcpClients := make(map[string]mcpclient.MCPClient)
	var initWg sync.WaitGroup
	var initMutex sync.Mutex
	initErrs := make(map[string]error)

	log.Infof("Initializing %d MCP clients...", len(config.MCPServers))
	initCtx, cancelInit := context.WithTimeout(context.Background(), 90*time.Second) // Timeout for initializing all clients
	defer cancelInit()

	for name, serverConf := range config.MCPServers {
		initWg.Add(1)
		go func(sName string, sConf ServerConfig) {
			defer initWg.Done()
			var mcpClient mcpclient.MCPClient
			var clientErr error

			clientCtx, clientCancel := context.WithCancel(initCtx) // Context per client init
			defer clientCancel()

			switch conf := sConf.(type) {
			case *ProcessServerConfig:
				log.Infof("Initializing MCP process client: %s (Cmd: %s)", sName, conf.Command)
				// transportOpts := []mcptransport.StdioTransportOption{}
				// if len(conf.Env) > 0 { // Need to check how to pass env vars via library
				// 	log.Warnf("Passing environment variables to stdio transport not directly supported by current library version, use wrapper script if needed.")
				// }
				// if conf.WorkingDir != "" {
				// 	transportOpts = append(transportOpts, mcptransport.WithWorkDir(conf.WorkingDir))
				// }
				// Note: The current mcpclient.NewStdioMCPClient doesn't seem to accept env/workdir directly.
				// Consider using os/exec manually if these are critical, or a wrapper script.
				mcpClient, clientErr = mcpclient.NewStdioMCPClient(conf.Command, nil, conf.Args...)
				if clientErr != nil {
					clientErr = fmt.Errorf("failed to create stdio client for '%s': %w", sName, clientErr)
				}

			case *SSEServerConfig:
				log.Infof("Initializing MCP SSE client: %s (URL: %s)", sName, conf.URL)
				transportOpts := []mcptransport.ClientOption{}
				if len(conf.Headers) > 0 {
					transportOpts = append(transportOpts, mcpclient.WithHeaders(conf.Headers))
					log.Debugf("Added %d headers for SSE client %s", len(conf.Headers), sName)
				}
				mcpClient, clientErr = mcpclient.NewSSEMCPClient(conf.URL, transportOpts...)
				if clientErr != nil {
					clientErr = fmt.Errorf("failed to create sse client for '%s': %w", sName, clientErr)
				} else {
					// Start the SSE client's background processing if it has a Start method
					// Assuming the interface might expose Start or specific implementations do.
					if starter, ok := mcpClient.(interface{ Start(context.Context) error }); ok {
						startErr := starter.Start(clientCtx) // Start requires context
						if startErr != nil {
							clientErr = fmt.Errorf("failed to start sse client '%s': %w", sName, startErr)
						}
					} else {
						log.Debugf("Client type %T for %s does not implement Start(context.Context)", mcpClient, sName)
						// Assume non-SSE clients don't need explicit Start here
					}
				}

			default:
				clientErr = fmt.Errorf("unsupported server config type '%T' for server '%s'", sConf, sName)
			}

			// If client creation/start failed, record error and return
			if clientErr != nil {
				log.Warnf("Failed to create/start MCP client for '%s': %v", sName, clientErr)
				initMutex.Lock()
				initErrs[sName] = clientErr
				initMutex.Unlock()
				if mcpClient != nil { // Attempt cleanup if client was partially created (e.g., SSE)
					_ = mcpClient.Close()
				}
				return
			}

			// --- Initialize MCP Session ---
			log.Debugf("Sending MCP Initialize request to server: %s", sName)
			initReq := mcplib.InitializeRequest{
				Params: struct {
					ProtocolVersion string                    `json:"protocolVersion"`
					Capabilities    mcplib.ClientCapabilities `json:"capabilities"`
					ClientInfo      mcplib.Implementation     `json:"clientInfo"`
				}{
					ProtocolVersion: mcplib.LATEST_PROTOCOL_VERSION,
					ClientInfo: mcplib.Implementation{
						Name:    "awesomeproj-mcp-client", // Identify our client
						Version: "0.1.0",                  // TODO: Versioning?
					},
					Capabilities: mcplib.ClientCapabilities{ /* Define capabilities if needed */ },
				},
			}
			_, initSessionErr := mcpClient.Initialize(clientCtx, initReq)
			if initSessionErr != nil {
				initErr := fmt.Errorf("failed to initialize MCP session with server '%s': %w", sName, initSessionErr)
				log.Warnf(initErr.Error())
				initMutex.Lock()
				initErrs[sName] = initErr
				initMutex.Unlock()
				_ = mcpClient.Close() // Cleanup failed client
				return
			}

			// Store successfully initialized client
			initMutex.Lock()
			mcpClients[sName] = mcpClient
			initMutex.Unlock()
			log.Infof("Successfully initialized MCP client for server: %s", sName)

		}(name, serverConf)
	}

	initWg.Wait() // Wait for all initializations to complete or timeout

	// Check for overall timeout
	if initCtx.Err() == context.DeadlineExceeded {
		log.Errorf("MCP client initialization timed out after %v.", 90*time.Second)
		// Close any clients that might have been initialized before timeout
		closeAllClients(mcpClients) // Use helper to attempt cleanup
		return nil, fmt.Errorf("mcp client initialization timed out")
	}

	// Check if any clients failed to initialize
	if len(initErrs) > 0 {
		var errorMsgs []string
		for name, err := range initErrs {
			errorMsgs = append(errorMsgs, fmt.Sprintf(" '%s': %v", name, err))
		}
		log.Errorf("Failed to initialize %d MCP client(s):%s", len(initErrs), strings.Join(errorMsgs, ";"))
		// Close successfully initialized clients before returning error
		closeAllClients(mcpClients)
		return nil, fmt.Errorf("failed to initialize %d mcp client(s):%s", len(initErrs), strings.Join(errorMsgs, ";"))
	}

	// If no servers were configured, return an error or a limited engine?
	// For now, let's allow it but log a warning.
	if len(config.MCPServers) == 0 {
		log.Warn("No MCP servers configured in EngineConfig.MCPServers. Tool listing and execution will be unavailable.")
	} else if len(mcpClients) == 0 {
		// This case should be covered by initErrs check, but as a safeguard:
		log.Error("MCP servers were configured, but no clients were successfully initialized.")
		return nil, fmt.Errorf("no mcp clients initialized despite configuration")
	}

	log.Infof("Successfully initialized %d MCP clients.", len(mcpClients))

	// --- Create the LLM Provider ---
	// Currently hardcoded to OpenAI provider
	provider, err := newOpenaiProvider(config, mcpClients)
	if err != nil {
		log.Errorf("Failed to create OpenAI provider: %v", err)
		closeAllClients(mcpClients) // Cleanup clients if provider fails
		return nil, fmt.Errorf("failed to create engine provider: %w", err)
	}

	engine := &mcpEngine{
		provider: provider,
	}

	log.Info("MCP Engine initialized successfully.")
	return engine, nil
}

// ChatStream delegates to the configured provider.
func (e *mcpEngine) ChatStream(ctx context.Context, sessionID string, prompt string, history []Message, outputChan chan<- ChatEvent) error {
	if e.provider == nil {
		return fmt.Errorf("engine provider is not initialized")
	}
	return e.provider.ChatStream(ctx, sessionID, prompt, history, outputChan)
}

// ListTools delegates to the configured provider.
func (e *mcpEngine) ListTools() ([]ToolDefinition, error) {
	if e.provider == nil {
		return nil, fmt.Errorf("engine provider is not initialized")
	}
	return e.provider.ListTools()
}

// Name delegates to the configured provider.
func (e *mcpEngine) Name() string {
	if e.provider == nil {
		return "uninitialized"
	}
	return e.provider.Name()
}

// Close delegates to the configured provider to close itself and its clients.
func (e *mcpEngine) Close() error {
	log.Info("Closing MCP Engine...")
	if e.provider == nil {
		log.Warn("Engine provider already nil during Close.")
		return nil
	}
	err := e.provider.Close()
	e.provider = nil // Prevent further use
	log.Info("MCP Engine closed.")
	return err
}

// closeAllClients is a helper to attempt closing multiple MCP clients.
func closeAllClients(clients map[string]mcpclient.MCPClient) {
	log.Warnf("Closing %d MCP clients due to initialization failure or engine shutdown...", len(clients))
	var wg sync.WaitGroup
	for name, client := range clients {
		wg.Add(1)
		go func(n string, c mcpclient.MCPClient) {
			defer wg.Done()
			if err := c.Close(); err != nil {
				log.Warnf("Error closing MCP client '%s' during cleanup: %v", n, err)
			}
		}(name, client)
	}
	wg.Wait()
}
