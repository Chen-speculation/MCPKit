package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/Chen-speculation/MCPKit/mcp" // Import the new local MCP library using correct module path
)

func main() {
	// --- Configuration Flags ---
	openaiAPIKey := flag.String("openai-key", os.Getenv("OPENAI_API_KEY"), "OpenAI API Key (or env var OPENAI_API_KEY)")
	openaiBaseURL := flag.String("openai-url", os.Getenv("OPENAI_BASE_URL"), "Optional: OpenAI compatible Base URL (or env var OPENAI_BASE_URL)")
	modelName := flag.String("model", "gpt-4o", "LLM model name")
	mcpConfigPath := flag.String("mcp-config", "./mcp_servers.json", "Path to MCP servers JSON configuration file")
	userPrompt := flag.String("prompt", "", "User prompt for the LLM")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")

	flag.Parse()

	// --- Logging Setup ---
	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		log.SetLevel(log.InfoLevel)
		log.Warnf("Invalid log level '%s', defaulting to info. Error: %v", *logLevel, err)
	} else {
		log.SetLevel(level)
	}
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
	log.Info("Starting AwesomeProj MCP Example...")

	// --- Input Validation ---
	if *openaiAPIKey == "" {
		log.Fatal("OpenAI API Key is required. Set via -openai-key flag or OPENAI_API_KEY env var.")
		return
	}
	if *userPrompt == "" {
		log.Fatal("User prompt is required. Set via -prompt flag.")
		return
	}

	// --- Load MCP Server Configuration ---
	log.Infof("Loading MCP server configuration from: %s", *mcpConfigPath)
	mcpServers, err := mcp.LoadConfigFromFile(*mcpConfigPath)
	if err != nil {
		log.Fatalf("Failed to load MCP configuration: %v", err)
		return
	}
	if len(mcpServers) == 0 {
		log.Warn("No MCP servers loaded from the configuration file.")
	}

	// --- Setup Context ---
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop() // Ensure stop is called eventually

	// --- Initialize MCP Engine ---
	log.Info("Initializing MCP Engine...")
	engineConfig := mcp.EngineConfig{
		DefaultModel:  *modelName,
		OpenAIAPIKey:  *openaiAPIKey,
		OpenAIBaseURL: *openaiBaseURL,
		MCPServers:    mcpServers,
	}

	engine, err := mcp.NewEngine(engineConfig)
	if err != nil {
		log.Fatalf("Failed to initialize MCP Engine: %v", err)
		return
	}
	// Ensure engine resources (like MCP clients) are closed on exit
	defer func() {
		log.Info("Shutting down MCP Engine...")
		if err := engine.Close(); err != nil {
			log.Errorf("Error shutting down MCP Engine: %v", err)
		}
		log.Info("MCP Engine shut down complete.")
	}()

	// --- Prepare Chat ---
	sessionID := uuid.NewString()
	log.Infof("Starting chat stream with Session ID: %s", sessionID)
	// For this example, history is empty. A real app would manage history.
	history := []mcp.Message{}
	outputChan := make(chan mcp.ChatEvent, 10) // Buffered channel

	// --- Start Chat Stream (Goroutine) ---
	go func() {
		// Ensure channel is closed when goroutine finishes or errors
		defer close(outputChan)
		err := engine.ChatStream(ctx, sessionID, *userPrompt, history, outputChan)
		if err != nil {
			// Don't log fatal from goroutine, send error event instead
			log.Errorf("Chat stream error: %v", err)
			// Try sending error event if channel is still open and context not done
			select {
			case outputChan <- mcp.ChatEvent{Type: "error", Error: err.Error()}:
			case <-ctx.Done(): // Check context cancellation
				log.Warn("Context cancelled while trying to send chat stream error event.")
			default:
				// Channel likely closed already
			}
		}
	}()

	// --- Process Chat Events ---
	log.Info("Waiting for chat events... Press Ctrl+C to exit.")
	keepRunning := true
	for keepRunning {
		select {
		case event, ok := <-outputChan:
			if !ok {
				log.Info("Chat stream channel closed.")
				keepRunning = false
				break // Exit select when channel closes
			}

			// Process the received event
			switch event.Type {
			case "session_id":
				log.Infof("[🆔] Session ID: %s", event.SessionID)
			case "text":
				fmt.Print(event.Content) // Print text chunks directly for streaming effect
			case "tool_call":
				fmt.Println() // Newline before tool call info
				argsJSON, _ := json.Marshal(event.ToolCall.Arguments)
				log.Infof("[⚙️] Tool Call Requested: ID=%s, Name=%s, Args=%s",
					event.ToolCall.ID, event.ToolCall.Name, string(argsJSON))
			case "tool_result":
				status := "OK"
				if event.ToolResult.IsError {
					status = "ERROR"
				}
				// Truncate long results for cleaner logging
				maxLen := 200
				content := event.ToolResult.Content
				if len(content) > maxLen {
					content = content[:maxLen] + "..."
				}
				log.Infof("[🧰] Tool Result Received: ID=%s, Name=%s, Status=%s, Content=%s",
					event.ToolResult.CallID, event.ToolResult.Name, status, content)
			case "error":
				fmt.Println() // Ensure newline before error
				log.Errorf("[‼️] Error Event: %s", event.Error)
				// Potentially stop processing on critical errors? For example, keepRunning = false
			case "finish":
				fmt.Println() // Ensure newline after last text chunk
				log.Info("[✅] Chat stream finished.")
				// keepRunning = false // Often we want to exit after finish
			default:
				log.Warnf("Received unknown event type: %s", event.Type)
			}

		case <-ctx.Done():
			log.Info("Shutdown signal received, exiting event loop.")
			keepRunning = false
		}
	}

	// Final check for context error after loop
	if ctx.Err() != nil {
		log.Warnf("Context error after event loop: %v", ctx.Err())
	}

	log.Info("AwesomeProj MCP Example finished.")
}
