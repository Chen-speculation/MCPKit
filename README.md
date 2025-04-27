# AwesomeProj

**English**

*   **Project Goal**: Implement an "MCP Host" library in Go. This library allows Go applications to manage and interact with external processes (MCP Servers) that expose tools according to the Model Context Protocol (MCP).
*   **MCP Introduction**: The Model Context Protocol (MCP) aims to standardize how applications provide context (like tools and data sources) to Large Language Models (LLMs). It acts like a common interface, enabling LLMs to discover and utilize capabilities provided by different external systems. Read more at [https://github.com/modelcontext/modelcontextprotocol](https://github.com/modelcontext/modelcontextprotocol).
*   **Configuration**: MCP Servers are configured via a JSON file (e.g., `config.json`). The file specifies a map of server names to their configuration details.
    *   `command`: The executable to run.
    *   `args`: Arguments for the command.
    *   `transport`: Communication method (currently only `"stdio"` is supported).
    *   `workingDir`: (Optional) Working directory for the server process.
    *   `env`: (Optional) Additional environment variables for the server process.

    See `example/config.json` for a sample:
    ```json
    {
      "servers": {
        "echo_server": {
          "command": "go",
          "args": [
            "run",
            "./mcp_echo_server/main.go" 
          ],
          "transport": "stdio",
          "workingDir": "."
        },
        "filesystem_server": {
           "command": "npx",
           "args": ["-y", "@modelcontextprotocol/server-filesystem", "./example_fs_data"],
           "transport": "stdio",
           "workingDir": "."
         }
      }
    }
    ```
*   **Usage**: 
    1.  Import the library: `import "github.com/your-username/awesomeproj/mcp"` (adjust path).
    2.  Create a configuration file (`config.json`).
    3.  Initialize the host: `host, err := mcp.NewHost(ctx, "config.json", logger)`.
    4.  Ensure clean shutdown: `defer host.Shutdown()`.
    5.  Discover tools: `allTools, err := host.GetAvailableTools(ctx, sessionID)`.
    6.  Execute a tool: `result, err := host.ExecuteTool(ctx, sessionID, clientName, toolCall)`.

    See `example/example.go` for a runnable demonstration.
*   **How it Works**: The `mcp.Host` reads the configuration, starts each configured MCP server as a subprocess (using the specified `command` and `args`), and establishes communication over the specified `transport` (currently `stdio`). It provides methods (`GetAvailableTools`, `ExecuteTool`) to interact with these servers by sending/receiving JSON-based MCP messages according to the protocol specification.

---

**中文**

*   **项目目标**: 实现一个 Go 语言的 "MCP Host" 库。该库允许 Go 应用程序管理外部进程（MCP 服务器）并与之交互，这些外部进程根据模型上下文协议（MCP）暴露工具（Tools）。
*   **MCP 简介**: 模型上下文协议 (MCP) 旨在标准化应用程序向大型语言模型（LLM）提供上下文（如工具和数据源）的方式。它像一个通用接口，使 LLM 能够发现并利用不同外部系统提供的能力。更多信息请访问 [https://github.com/modelcontext/modelcontextprotocol](https://github.com/modelcontext/modelcontextprotocol)。
*   **配置**: MCP 服务器通过 JSON 文件（例如 `config.json`）进行配置。该文件指定了一个从服务器名称到其配置细节的映射。
    *   `command`: 要运行的可执行文件。
    *   `args`: 传递给命令的参数。
    *   `transport`: 通信方法（目前仅支持 `"stdio"`）。
    *   `workingDir`: （可选）服务器进程的工作目录。
    *   `env`: （可选）为服务器进程设置的额外环境变量。

    请参阅 `example/config.json` 中的示例:
    ```json
    {
      "servers": {
        "echo_server": {
          "command": "go",
          "args": [
            "run",
            "./mcp_echo_server/main.go" 
          ],
          "transport": "stdio",
          "workingDir": "."
        },
        "filesystem_server": {
           "command": "npx",
           "args": ["-y", "@modelcontextprotocol/server-filesystem", "./example_fs_data"],
           "transport": "stdio",
           "workingDir": "."
         }
      }
    }
    ```
*   **使用方法**: 
    1.  导入库: `import "github.com/your-username/awesomeproj/mcp"` (请调整路径)。
    2.  创建配置文件 (`config.json`)。
    3.  初始化 Host: `host, err := mcp.NewHost(ctx, "config.json", logger)`。
    4.  确保干净地关闭: `defer host.Shutdown()`。
    5.  发现工具: `allTools, err := host.GetAvailableTools(ctx, sessionID)`。
    6.  执行工具: `result, err := host.ExecuteTool(ctx, sessionID, clientName, toolCall)`。

    请参阅 `example/example.go` 获取可运行的演示。
*   **工作原理**: `mcp.Host` 读取配置，将每个配置的 MCP 服务器作为子进程启动（使用指定的 `command` 和 `args`），并通过指定的 `transport`（目前是 `stdio`）建立通信。它提供方法（`GetAvailableTools`, `ExecuteTool`）通过发送/接收基于 JSON 的 MCP 消息与这些服务器交互，遵循协议规范。 