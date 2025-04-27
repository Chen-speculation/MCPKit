package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcplib "github.com/mark3labs/mcp-go/mcp" // Aliased to avoid collision with package name
	openai "github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"
)

// openaiProvider implements the Engine interface using OpenAI's API.
type openaiProvider struct {
	client     *openai.Client
	model      string
	apiKey     string
	baseURL    string
	mcpClients map[string]mcpclient.MCPClient // Map of server name to MCP client instance
	closeOnce  sync.Once
}

// newOpenaiProvider creates a new instance of the OpenAI provider.
// It initializes the OpenAI client but does not start MCP clients.
func newOpenaiProvider(config EngineConfig, mcpClients map[string]mcpclient.MCPClient) (*openaiProvider, error) {
	apiKey := config.OpenAIAPIKey
	baseURL := config.OpenAIBaseURL
	model := config.DefaultModel

	if apiKey == "" {
		return nil, fmt.Errorf("OpenAI API key (EngineConfig.OpenAIAPIKey) is required")
	}
	if model == "" {
		// You might want to default the model here or return an error
		log.Warnf("OpenAI model (EngineConfig.DefaultModel) not specified, defaulting might occur in OpenAI library or API.")
		// model = "gpt-3.5-turbo" // Example default
	}

	openaiConfig := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		openaiConfig.BaseURL = baseURL
		log.Infof("Using custom OpenAI base URL: %s", baseURL)
	}
	client := openai.NewClientWithConfig(openaiConfig)

	return &openaiProvider{
		client:     client,
		model:      model,
		apiKey:     apiKey,
		baseURL:    baseURL,
		mcpClients: mcpClients, // Store the map of pre-initialized MCP clients
	}, nil
}

// Name returns the name of the engine.
func (p *openaiProvider) Name() string {
	return "openai"
}

// Close shuts down the provider, primarily closing MCP clients.
func (p *openaiProvider) Close() error {
	log.Info("Closing OpenAI provider and associated MCP clients...")
	p.closeOnce.Do(func() {
		var closeErrors []string
		var wg sync.WaitGroup
		for name, client := range p.mcpClients {
			wg.Add(1)
			go func(n string, c mcpclient.MCPClient) {
				defer wg.Done()
				log.Debugf("Closing MCP client: %s", n)
				if err := c.Close(); err != nil {
					log.Warnf("Error closing MCP client '%s': %v", n, err)
					// Potential: Collect errors if needed
					closeErrors = append(closeErrors, fmt.Sprintf("%s: %v", n, err))
				}
			}(name, client)
		}
		wg.Wait()
		// Optionally return combined error
		if len(closeErrors) > 0 {
			// log.Errorf("Errors encountered while closing MCP clients: %s", strings.Join(closeErrors, "; "))
			// If returning error, decide how:
			// return fmt.Errorf("errors closing clients: %s", strings.Join(closeErrors, "; "))
		}
	})
	log.Info("OpenAI provider closed.")
	return nil
}

// ChatStream implements streaming chat using OpenAI's API.
// It handles text streaming and iterative tool calls via MCP clients.
func (p *openaiProvider) ChatStream(ctx context.Context, sessionID string, prompt string, history []Message, outputChan chan<- ChatEvent) error {
	currentMessages := convertToOpenAIMessages(history)

	// Add the new user prompt if provided
	if prompt != "" {
		currentMessages = append(currentMessages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: prompt,
		})
	}

	// List and convert available tools from MCP clients
	tools, err := p.ListTools() // Returns []ToolDefinition (our type)
	if err != nil {
		// Log warning but proceed without tools if listing fails
		log.Warnf("Failed to list tools from MCP servers: %v. Proceeding without tools.", err)
		tools = []ToolDefinition{}
	}
	openaiTools := convertToOpenAITools(tools)
	log.Infof("Prepared %d tools for OpenAI API call.", len(openaiTools))

	// Send session ID event
	sendEvent(ctx, outputChan, ChatEvent{Type: "session_id", SessionID: sessionID})

	// --- Main Loop for Tool Call Iterations ---
	const maxToolIterations = 5 // Safety limit for tool call loops
	iteration := 0
	completedToolCalls := make(map[string]bool) // Track completed tool calls within this ChatStream call

	for iteration < maxToolIterations {
		iteration++
		log.Infof("Starting LLM interaction cycle %d", iteration)

		// --- Create OpenAI Stream Request ---
		req := openai.ChatCompletionRequest{
			Model:       p.model,
			Messages:    currentMessages,
			MaxTokens:   4096, // Consider making configurable
			Temperature: 0.7,  // Consider making configurable
			Stream:      true,
		}
		if len(openaiTools) > 0 {
			req.Tools = openaiTools
			req.ToolChoice = "auto" // Let OpenAI decide
		}

		stream, err := p.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			err = fmt.Errorf("ChatCompletionStream request failed: %w", err)
			log.Error(err)
			sendEvent(ctx, outputChan, ChatEvent{Type: "error", Error: err.Error()})
			return err // Abort on stream creation failure
		}
		// Defer stream close inside the loop? If loop continues, need to ensure closed.
		// Let's close it explicitly before continuing or breaking.

		var fullAssistantMessageContent strings.Builder
		accumulatedToolCalls := make(map[int]openai.ToolCall) // Use map to handle potential index gaps
		var currentFinishReason openai.FinishReason
		var assistantResponseMessage openai.ChatCompletionMessage // To store the final message from this iteration

		// --- Process Stream for One LLM Turn ---
		streamProcessingError := func() error {
			defer stream.Close() // Ensure stream is closed after processing
			for {
				response, streamErr := stream.Recv()
				if errors.Is(streamErr, io.EOF) {
					log.Info("Stream finished (EOF).")
					break // End of this stream
				}
				if streamErr != nil {
					err := fmt.Errorf("stream processing error: %w", streamErr)
					log.Error(err)
					sendEvent(ctx, outputChan, ChatEvent{Type: "error", Error: err.Error()})
					return err // Return error to stop outer loop
				}

				if len(response.Choices) == 0 {
					continue // Skip empty choices
				}

				delta := response.Choices[0].Delta
				currentFinishReason = response.Choices[0].FinishReason

				// Append text content
				if delta.Content != "" {
					sendEvent(ctx, outputChan, ChatEvent{Type: "text", Content: delta.Content})
					fullAssistantMessageContent.WriteString(delta.Content)
				}

				// Accumulate tool calls
				if len(delta.ToolCalls) > 0 {
					for _, tcDelta := range delta.ToolCalls {
						if tcDelta.Index == nil {
							log.Warnf("Tool call delta missing index, cannot process reliably.")
							continue // Skip if index is missing
						}
						idx := *tcDelta.Index
						// Initialize map entry if needed
						if _, exists := accumulatedToolCalls[idx]; !exists {
							accumulatedToolCalls[idx] = openai.ToolCall{Function: openai.FunctionCall{}}
						}
						tc := accumulatedToolCalls[idx] // Get a copy to modify
						if tcDelta.ID != "" {
							tc.ID = tcDelta.ID
						}
						if tcDelta.Type != "" {
							tc.Type = tcDelta.Type
						}
						if tcDelta.Function.Name != "" {
							tc.Function.Name = tcDelta.Function.Name
						}
						if tcDelta.Function.Arguments != "" {
							tc.Function.Arguments += tcDelta.Function.Arguments
						}
						accumulatedToolCalls[idx] = tc // Put modified copy back
					}
				}

				// If a finish reason is received, stop processing this stream
				if currentFinishReason != "" {
					log.Infof("Stream processing finished with reason: %s", currentFinishReason)
					break
				}
			}
			return nil // Successful stream processing
		}()

		if streamProcessingError != nil {
			return streamProcessingError // Exit if stream processing failed
		}

		// --- Construct Assistant Message from Stream Output ---
		assistantResponseMessage = openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
		}
		finalContent := fullAssistantMessageContent.String()
		finalToolCalls := make([]openai.ToolCall, 0, len(accumulatedToolCalls))
		for _, tc := range accumulatedToolCalls {
			// Basic validation: Ensure ID, Type, Name, and Arguments are present
			if tc.ID != "" && tc.Type == openai.ToolTypeFunction && tc.Function.Name != "" && tc.Function.Arguments != "" {
				finalToolCalls = append(finalToolCalls, tc)
			} else {
				log.Warnf("Discarding incomplete tool call accumulated: ID=%s, Type=%s, Name=%s, ArgsLen=%d", tc.ID, tc.Type, tc.Function.Name, len(tc.Function.Arguments))
			}
		}

		if len(finalToolCalls) > 0 {
			assistantResponseMessage.ToolCalls = finalToolCalls
			// assistantResponseMessage.Content should remain empty or nil for tool calls
			log.Infof("Assistant message contains %d tool calls.", len(finalToolCalls))
		} else if finalContent != "" {
			assistantResponseMessage.Content = finalContent
			log.Infof("Assistant message contains text content (length: %d).", len(finalContent))
		} else {
			// Handle cases where the assistant response is empty (neither text nor tools)
			log.Warnf("Assistant response was empty (no text or valid tool calls). Finish reason: %s", currentFinishReason)
			// If finish reason was stop, this might be okay. If tool_calls, it's weird.
			// If we stop here, the history might be incomplete. Let's add an empty message
			// but this might need adjustment based on observed API behavior.
			assistantResponseMessage.Content = "" // Explicitly empty
		}

		// Append assistant's message to history for next potential iteration or final history
		currentMessages = append(currentMessages, assistantResponseMessage)

		// --- Handle Finish Reason ---
		if currentFinishReason == openai.FinishReasonToolCalls {
			log.Infof("Finish Reason: Tool Calls. Processing %d calls.", len(assistantResponseMessage.ToolCalls))
			toolResults := make([]openai.ChatCompletionMessage, 0, len(assistantResponseMessage.ToolCalls))
			var toolWg sync.WaitGroup
			var toolMutex sync.Mutex // Protects toolResults slice

			for _, toolCall := range assistantResponseMessage.ToolCalls {
				if completedToolCalls[toolCall.ID] {
					log.Warnf("Tool call ID '%s' (%s) was already completed in a previous iteration. Skipping.", toolCall.ID, toolCall.Function.Name)
					continue
				}

				toolWg.Add(1)
				go func(tc openai.ToolCall) {
					defer toolWg.Done()

					var args map[string]interface{}
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
						errMsg := fmt.Sprintf("Failed to parse JSON arguments for tool %s (ID: %s): %v", tc.Function.Name, tc.ID, err)
						log.Warnf(errMsg)
						sendEvent(ctx, outputChan, ChatEvent{Type: "error", Error: errMsg})
						// Add error result message for LLM
						toolMutex.Lock()
						toolResults = append(toolResults, openai.ChatCompletionMessage{
							Role:       openai.ChatMessageRoleTool,
							ToolCallID: tc.ID,
							Name:       tc.Function.Name, // Include name for context
							Content:    fmt.Sprintf("Error parsing arguments: %v", err),
						})
						completedToolCalls[tc.ID] = true // Mark as completed (due to error)
						toolMutex.Unlock()
						return
					}

					log.Infof("Requesting tool execution: ID=%s, Name=%s, Args=%v", tc.ID, tc.Function.Name, args)

					// Send tool_call event (using our ToolCall type)
					sendEvent(ctx, outputChan, ChatEvent{
						Type: "tool_call",
						ToolCall: &ToolCall{
							ID:        tc.ID,
							Name:      tc.Function.Name,
							Arguments: args,
						},
					})

					// Execute the tool via MCP
					// Use a derived context with timeout for the tool call itself
					toolCtx, toolCancel := context.WithTimeout(ctx, 60*time.Second) // Configurable timeout? 60s default
					toolResultContent, toolErr := executeMcpTool(toolCtx, p.mcpClients, tc.Function.Name, args)
					toolCancel() // Cancel context regardless of outcome

					// Send tool_result event (using our ToolResult type)
					sendEvent(ctx, outputChan, ChatEvent{
						Type: "tool_result",
						ToolResult: &ToolResult{
							CallID:  tc.ID,
							Name:    tc.Function.Name,
							Content: toolResultContent,
							IsError: toolErr != nil,
						},
					})

					// Add result message for the next LLM call
					toolMutex.Lock()
					toolResults = append(toolResults, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,  // Name might be useful for context
						Content:    toolResultContent, // Contains result or error message
					})
					completedToolCalls[tc.ID] = true // Mark as completed
					toolMutex.Unlock()
				}(toolCall)
			}
			toolWg.Wait() // Wait for all tool calls in this iteration to finish

			// Add all tool results to the history for the next LLM call
			currentMessages = append(currentMessages, toolResults...)

			// Continue the loop to call the LLM again with the tool results
			continue

		} else if currentFinishReason == openai.FinishReasonStop {
			log.Info("Finish Reason: Stop. Chat completed normally.")
			break // Exit the main loop, conversation is complete
		} else {
			// Handle other finish reasons (length, content_filter, null)
			errMsg := fmt.Sprintf("Chat finished with unexpected reason: %s", currentFinishReason)
			log.Warnf(errMsg)
			// Send an error event? Or just finish?
			// sendEvent(ctx, outputChan, ChatEvent{Type: "error", Error: errMsg})
			break // Exit loop on other/unexpected reasons
		}
	} // End of main for loop (tool call iterations)

	if iteration >= maxToolIterations {
		log.Warnf("Reached maximum tool iteration limit (%d). Finishing chat.", maxToolIterations)
		sendEvent(ctx, outputChan, ChatEvent{Type: "error", Error: fmt.Sprintf("reached maximum tool iterations (%d)", maxToolIterations)})
	}

	log.Info("ChatStream processing complete.")
	sendEvent(ctx, outputChan, ChatEvent{Type: "finish"}) // Signal completion
	return nil
}

// --- Helper Functions ---

// convertToOpenAIMessages converts internal []Message history to []openai.ChatCompletionMessage.
func convertToOpenAIMessages(history []Message) []openai.ChatCompletionMessage {
	apiMessages := make([]openai.ChatCompletionMessage, 0, len(history))
	for _, msg := range history {
		apiMsg := openai.ChatCompletionMessage{
			Role: msg.Role,
		}

		switch msg.Role {
		case openai.ChatMessageRoleUser, openai.ChatMessageRoleSystem:
			apiMsg.Content = msg.Content
		case openai.ChatMessageRoleAssistant:
			if len(msg.ToolCalls) > 0 {
				apiMsg.ToolCalls = make([]openai.ToolCall, len(msg.ToolCalls))
				for i, engCall := range msg.ToolCalls {
					argsJSON, err := json.Marshal(engCall.Arguments)
					if err != nil {
						log.Errorf("Error marshalling history tool call arguments for call ID %s: %v. Using empty args.", engCall.ID, err)
						argsJSON = []byte("{}")
					}
					apiMsg.ToolCalls[i] = openai.ToolCall{
						ID:   engCall.ID,
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      engCall.Name,
							Arguments: string(argsJSON),
						},
					}
				}
				// Ensure Content is nil or empty string when ToolCalls are present
				// apiMsg.Content = "" // Let's rely on OpenAI library defaults / API behavior
			} else {
				// Regular assistant message content
				apiMsg.Content = msg.Content
			}
		case openai.ChatMessageRoleTool:
			apiMsg.Content = msg.Content // Content holds the result
			apiMsg.ToolCallID = msg.ToolCallID
			if msg.ToolCallID == "" {
				log.Warnf("History conversion: Tool message found with empty ToolCallID. Content: %s", msg.Content)
			}
		default:
			log.Warnf("History conversion: Unknown role '%s' encountered.", msg.Role)
			continue // Skip unknown roles
		}
		apiMessages = append(apiMessages, apiMsg)
	}
	return apiMessages
}

// convertToOpenAITools converts internal []ToolDefinition to []openai.Tool.
func convertToOpenAITools(tools []ToolDefinition) []openai.Tool {
	openaiTools := make([]openai.Tool, len(tools))
	for i, tool := range tools {
		// Convert our Schema struct to the JSON structure OpenAI expects (map[string]interface{})
		// by marshalling and unmarshalling.
		schemaBytes, err := json.Marshal(tool.Schema)
		if err != nil {
			log.Errorf("Failed to marshal internal schema for tool '%s': %v. Using empty parameters.", tool.Name, err)
			schemaBytes = []byte("{}") // Fallback to empty object
		}

		// We need json.RawMessage which is compatible with openai.FunctionDefinition.Parameters
		rawSchema := json.RawMessage(schemaBytes)

		openaiTools[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name, // Should be server__toolname format from ListTools
				Description: tool.Description,
				Parameters:  rawSchema,
			},
		}
	}
	return openaiTools
}

// ListTools fetches tool definitions from all connected MCP servers.
// It prepends the server name to the tool name (e.g., "filesystem__list_files").
func (p *openaiProvider) ListTools() ([]ToolDefinition, error) {
	allTools := make([]ToolDefinition, 0)
	var mu sync.Mutex // Protects allTools slice
	var wg sync.WaitGroup

	listToolsCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Timeout for listing across all clients
	defer cancel()

	log.Infof("Listing tools from %d MCP clients...", len(p.mcpClients))
	for serverName, client := range p.mcpClients {
		wg.Add(1)
		go func(sName string, c mcpclient.MCPClient) {
			defer wg.Done()
			log.Debugf("Requesting tools from MCP server: %s", sName)
			req := mcplib.ListToolsRequest{} // Empty request body
			resp, err := c.ListTools(listToolsCtx, req)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					log.Warnf("Timeout listing tools from server '%s'.", sName)
				} else {
					log.Warnf("Failed to list tools from server '%s': %v. Skipping server.", sName, err)
				}
				return // Skip this server on error
			}

			if resp == nil || len(resp.Tools) == 0 {
				log.Debugf("No tools reported by server '%s'", sName)
				return
			}

			log.Debugf("Received %d tools from server '%s'", len(resp.Tools), sName)
			serverTools := make([]ToolDefinition, 0, len(resp.Tools))
			for _, mcpTool := range resp.Tools {
				// Convert mcplib.ToolDefinition to our internal ToolDefinition
				engineSchema := Schema{
					Type:       "object", // Default type
					Properties: make(map[string]interface{}),
					Required:   []string{},
				}

				// Check Type field as indicator for non-empty struct
				if mcpTool.InputSchema.Type != "" || len(mcpTool.InputSchema.Properties) > 0 {
					inputSchema := mcpTool.InputSchema // mcplib.ToolInputSchema

					if inputSchema.Properties != nil {
						// Properties in mcplib.ToolInputSchema is map[string]*mcplib.SchemaDefinition
						// Convert to map[string]interface{} via JSON marshal/unmarshal
						tempProps := make(map[string]interface{})
						propBytes, err := json.Marshal(inputSchema.Properties)
						if err == nil {
							err = json.Unmarshal(propBytes, &tempProps)
						}
						if err != nil {
							log.Warnf("Failed to marshal/unmarshal properties for tool '%s' from server '%s': %v", mcpTool.Name, sName, err)
						} else {
							engineSchema.Properties = tempProps
						}
					} else {
						log.Debugf("Tool '%s' from server '%s' has nil InputSchema.Properties", mcpTool.Name, sName)
					}

					if inputSchema.Required != nil {
						engineSchema.Required = inputSchema.Required
					}

					if inputSchema.Type != "" {
						engineSchema.Type = inputSchema.Type
					}
				} else {
					log.Debugf("Tool '%s' from server '%s' has empty/default InputSchema definition.", mcpTool.Name, sName)
				}

				serverTools = append(serverTools, ToolDefinition{
					Name:        fmt.Sprintf("%s__%s", sName, mcpTool.Name), // Prepend server name
					Description: mcpTool.Description,
					Schema:      engineSchema,
				})
			}
			// Append server's tools to the main list safely
			mu.Lock()
			allTools = append(allTools, serverTools...)
			mu.Unlock()
		}(serverName, client)
	}

	wg.Wait() // Wait for all listing goroutines to complete

	log.Infof("Total tools listed from all MCP servers: %d", len(allTools))
	return allTools, nil // Return the combined list
}

// executeMcpTool performs the actual MCP tool call via the appropriate client.
// Returns the result content (string) and any execution error.
func executeMcpTool(ctx context.Context, mcpClients map[string]mcpclient.MCPClient, fullToolName string, args map[string]interface{}) (resultContent string, err error) {
	parts := strings.SplitN(fullToolName, "__", 2) // Split only once
	if len(parts) != 2 {
		errMsg := fmt.Sprintf("invalid tool name format: expected 'server__toolname', got '%s'", fullToolName)
		log.Warnf(errMsg)
		return fmt.Sprintf("Error: %s", errMsg), errors.New(errMsg)
	}

	serverName, toolName := parts[0], parts[1]
	mcpClient, ok := mcpClients[serverName]
	if !ok {
		errMsg := fmt.Sprintf("MCP server '%s' not found for tool '%s'", serverName, fullToolName)
		log.Warnf(errMsg)
		return fmt.Sprintf("Error: %s", errMsg), errors.New(errMsg)
	}

	// Build request using mcplib types and the correct structure for Params
	toolRequest := mcplib.CallToolRequest{
		Params: struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments,omitempty"`
			Meta      *struct {
				ProgressToken mcplib.ProgressToken `json:"progressToken,omitempty"`
			} `json:"_meta,omitempty"`
		}{
			Name:      toolName,
			Arguments: args,
		},
	}

	// Execute tool call (using the context passed, which should have timeout)
	log.Debugf("Calling MCP tool '%s' on server '%s' with args: %v", toolName, serverName, args)
	toolResult, callErr := mcpClient.CallTool(ctx, toolRequest)

	if callErr != nil {
		// Check specifically for context deadline exceeded
		if errors.Is(callErr, context.DeadlineExceeded) {
			errMsg := fmt.Sprintf("timeout calling tool %s on server %s", fullToolName, serverName)
			log.Warnf(errMsg)
			return fmt.Sprintf("Error: %s", errMsg), callErr // Return specific error
		} else {
			errMsg := fmt.Sprintf("error calling tool %s on server %s: %v", fullToolName, serverName, callErr)
			log.Warnf(errMsg)
			return fmt.Sprintf("Error executing tool %s: %v", fullToolName, callErr), callErr
		}
	}

	// Process successful result
	if toolResult == nil {
		log.Infof("Tool %s executed successfully on server %s but returned a nil result.", fullToolName, serverName)
		return fmt.Sprintf("Tool %s executed successfully (no content).", fullToolName), nil
	}

	// Extract content from result.Result (which is []interface{})
	if len(toolResult.Content) > 0 {
		var resultTextBuilder strings.Builder
		for i, item := range toolResult.Content {
			// Attempt to handle known content types (like TextContent)
			if textContent, ok := item.(mcplib.TextContent); ok {
				resultTextBuilder.WriteString(textContent.Text)
			} else {
				// Fallback: Marshal unknown content types to JSON string
				log.Debugf("Marshalling unknown tool result content type (%T) to JSON for tool %s", item, fullToolName)
				unknownJSON, jsonErr := json.Marshal(item)
				if jsonErr != nil {
					log.Warnf("Failed to marshal unknown tool result item #%d (%T) to JSON for tool %s: %v", i, item, fullToolName, jsonErr)
					resultTextBuilder.WriteString(fmt.Sprintf("[Unmarshallable Content Type: %T]", item))
				} else {
					resultTextBuilder.WriteString(string(unknownJSON))
				}
			}
			// Add a space between items for readability, trim later
			if i < len(toolResult.Content)-1 {
				resultTextBuilder.WriteString(" ")
			}
		}
		resultContent = strings.TrimSpace(resultTextBuilder.String())
		log.Debugf("Tool %s result processed: %s", fullToolName, resultContent)
		return resultContent, nil // Success with content
	} else {
		// No content items in the result
		log.Infof("Tool %s executed successfully on server %s but returned no content items.", fullToolName, serverName)
		return fmt.Sprintf("Tool %s executed successfully (empty content).", fullToolName), nil // Success, no content
	}
}

// sendEvent safely sends a ChatEvent to the output channel, respecting context cancellation.
func sendEvent(ctx context.Context, outputChan chan<- ChatEvent, event ChatEvent) {
	select {
	case outputChan <- event:
		// Event sent successfully
	case <-ctx.Done():
		// Context cancelled, cannot send event
		log.Warnf("Context cancelled before sending event type '%s'.", event.Type)
	}
}
