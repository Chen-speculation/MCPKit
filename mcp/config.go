package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// MCPServerConfig holds the configuration for a single MCP server instance.
// This dictates how the Host should start and communicate with the server process.
type MCPServerConfig struct {
	// Command is the executable to run for the MCP server.
	Command string `json:"command"`
	// Args are the arguments to pass to the command.
	Args []string `json:"args"`
	// Transport specifies the communication method (e.g., "stdio").
	// Currently, only "stdio" is planned.
	Transport string `json:"transport"`
	// WorkingDir specifies the working directory for the command. Optional.
	WorkingDir string `json:"workingDir,omitempty"`
	// Env specifies additional environment variables for the command. Optional.
	Env []string `json:"env,omitempty"`
	// TODO: Add other potential fields like startup timeout, health checks?
}

// HostConfig is the top-level configuration structure for the MCP Host.
// It contains a map of named MCP server configurations.
type HostConfig struct {
	// Servers maps a unique logical name (e.g., "filesystem", "database")
	// to its specific configuration details.
	Servers map[string]MCPServerConfig `json:"servers"`
}

// LoadConfig parses the MCP Host configuration from a JSON file.
func LoadConfig(filePath string) (*HostConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP config file '%s': %w", filePath, err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("MCP config file '%s' is empty", filePath)
	}

	var config HostConfig
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MCP config JSON from '%s': %w", filePath, err)
	}

	// Basic validation
	if len(config.Servers) == 0 {
		return nil, fmt.Errorf("no servers defined in MCP config file '%s'", filePath)
	}

	for name, serverConf := range config.Servers {
		if serverConf.Command == "" {
			return nil, fmt.Errorf("server '%s' is missing 'command' in MCP config", name)
		}
		if serverConf.Transport == "" {
			// Defaulting or erroring? Let's error for now for explicitness.
			return nil, fmt.Errorf("server '%s' is missing 'transport' in MCP config", name)
		}
		if serverConf.Transport != "stdio" {
			// Only support stdio initially
			return nil, fmt.Errorf("server '%s' has unsupported transport '%s'; only 'stdio' is supported", name, serverConf.Transport)
		}
	}

	return &config, nil
}
