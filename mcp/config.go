package mcp

import (
	"encoding/json"
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
)

// rawServerConfig is used for initial JSON unmarshalling.
// We use this intermediate step to determine the type before creating
// the concrete ServerConfig implementation (ProcessServerConfig or SSEServerConfig).
type rawServerConfig struct {
	// Common fields used for type determination or present in all types
	Command   string `json:"command,omitempty"`
	URL       string `json:"url,omitempty"`
	Transport string `json:"transport,omitempty"`

	// Capture the rest of the fields to unmarshal into the specific type later
	raw json.RawMessage
}

// UnmarshalJSON custom unmarshaler for rawServerConfig to capture the raw JSON.
func (r *rawServerConfig) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get known fields
	type KnownFields struct {
		Command   string `json:"command,omitempty"`
		URL       string `json:"url,omitempty"`
		Transport string `json:"transport,omitempty"`
	}
	var known KnownFields
	if err := json.Unmarshal(data, &known); err != nil {
		return fmt.Errorf("failed to unmarshal known fields: %w", err)
	}
	r.Command = known.Command
	r.URL = known.URL
	r.Transport = known.Transport

	// Store the original raw JSON
	r.raw = data
	return nil
}

// configFile structure mirrors the expected JSON input (file or string).
type configFile struct {
	// Using map[string]json.RawMessage allows flexible parsing based on type
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// ParseMCPServersJSON parses MCP server configurations from a JSON string.
// It determines the server type (process/stdio or sse) and creates the appropriate
// ServerConfig struct (ProcessServerConfig or SSEServerConfig).
// Returns a map of server names to their ServerConfig interface implementations.
func ParseMCPServersJSON(configJSON string) (map[string]ServerConfig, error) {
	var cfgFile configFile
	if err := json.Unmarshal([]byte(configJSON), &cfgFile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MCP servers JSON config: %w", err)
	}

	if cfgFile.MCPServers == nil {
		log.Warn("No 'mcpServers' key found or it is null in the provided configuration JSON.")
		return make(map[string]ServerConfig), nil // Return empty map if none defined
	}

	engineServers := make(map[string]ServerConfig)
	for name, rawCfg := range cfgFile.MCPServers {
		// Determine the transport type explicitly or implicitly
		var knownFields struct {
			Command   string `json:"command,omitempty"`
			URL       string `json:"url,omitempty"`
			Transport string `json:"transport,omitempty"`
		}
		if err := json.Unmarshal(rawCfg, &knownFields); err != nil {
			log.Warnf("Skipping MCP server '%s': Failed to unmarshal basic fields: %v", name, err)
			continue
		}

		transportType := knownFields.Transport
		if transportType == "" {
			if knownFields.Command != "" {
				transportType = "process" // Default to process if command exists
				log.Debugf("MCP server '%s' has no explicit transport, defaulting to 'process' due to 'command' field.", name)
			} else if knownFields.URL != "" {
				transportType = "sse" // Default to sse if url exists and command doesn't
				log.Debugf("MCP server '%s' has no explicit transport, defaulting to 'sse' due to 'url' field.", name)
			} else {
				log.Warnf("Skipping MCP server '%s': Cannot determine transport type (missing 'transport', 'command', or 'url').", name)
				continue
			}
		}

		// Unmarshal into the specific config type based on transport
		var serverConf ServerConfig
		var unmarshalErr error
		switch transportType {
		case "stdio", "process":
			var procConf ProcessServerConfig
			unmarshalErr = json.Unmarshal(rawCfg, &procConf)
			if unmarshalErr == nil {
				if procConf.Command == "" {
					log.Warnf("Skipping MCP server '%s': 'command' is required for stdio/process transport but is empty.", name)
					continue
				}
				procConf.Name = name      // Inject the logical name
				procConf.Type = "process" // Normalize type
				serverConf = &procConf
				log.Debugf("Parsed stdio/process server config for '%s'", name)
			}

		case "sse":
			var sseConf SSEServerConfig
			unmarshalErr = json.Unmarshal(rawCfg, &sseConf)
			if unmarshalErr == nil {
				if sseConf.URL == "" {
					log.Warnf("Skipping MCP server '%s': 'url' is required for sse transport but is empty.", name)
					continue
				}
				sseConf.Name = name  // Inject the logical name
				sseConf.Type = "sse" // Normalize type
				serverConf = &sseConf
				log.Debugf("Parsed sse server config for '%s'", name)
			}

		default:
			log.Warnf("Skipping MCP server '%s': Unsupported transport type '%s'.", name, transportType)
			continue // Skip unsupported types
		}

		// Check for unmarshalling errors after the switch
		if unmarshalErr != nil {
			log.Warnf("Skipping MCP server '%s': Failed to unmarshal config for transport '%s': %v", name, transportType, unmarshalErr)
			continue
		}

		// Add successfully parsed config to the map
		if serverConf != nil {
			engineServers[name] = serverConf
		}
	}

	return engineServers, nil
}

// LoadConfigFromFile reads a JSON file from the given path and parses it.
func LoadConfigFromFile(filePath string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP config file '%s': %w", filePath, err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("MCP config file '%s' is empty", filePath)
	}

	return ParseMCPServersJSON(string(data))
}
