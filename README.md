**English**

*   **Project Goal**: Implement an "MCP Engine" library in Go. This library allows Go applications to interface with Large Language Models (LLMs) and dynamically utilize tools exposed by external MCP Servers (processes or services adhering to the Model Context Protocol).
*   **MCP Introduction**: The Model Context Protocol (MCP) aims to standardize how applications provide context (like tools and data sources) to Large Language Models (LLMs). It acts like a common interface, enabling LLMs to discover and utilize capabilities provided by different external systems. Read more at [https://github.com/modelcontext/modelcontextprotocol](https://github.com/modelcontext/modelcontextprotocol).
*   **Configuration**: MCP Servers are configured via a JSON file (e.g., `mcp_servers.json`). The file specifies a map of server names to their configuration details. The library supports servers communicating via `stdio` (`process`) or `sse`.
    *   `command`: The executable to run (for `process` type).
    *   `args`: Arguments for the command (for `process` type).
    *   `url`: The endpoint URL (for `sse` type).
    *   `transport`: Communication method (`"process"` or `"sse"`). Can often be inferred.
    *   `workingDir`: (Optional) Working directory for the server process (`process` type).
    *   `headers`: (Optional) Headers for the connection (`sse` type).

    See `example/mcp_servers.json` for a sample (create this file based on your needs):
    ```json
    {
      "mcpServers": {
        "filesystem": {
          "command": "npx",
          "args": [
            "-y",
            "@modelcontextprotocol/server-filesystem",
            "./fs_data" 
          ],
          "transport": "process", 
          "workingDir": "."
        },
         "kubernetes": {
           "command": "npx",
           "args": [
             "-y",
             "kubernetes-mcp-server@latest"
           ],
           "transport": "process"
         }
        // Example SSE server config (if you have one running)
        // "sse_example": {
        //   "url": "http://localhost:8080/sse",
        //   "transport": "sse",
        //   "headers": {
        //     "Authorization": "Bearer your_token"
        //   }
        // }
      }
    }
    ```
*   **Usage (LLM Interaction with MCP Tools)**: 
    1.  Import the library: `import "github.com/Chen-speculation/MCPKit/mcp"` (using the correct module path from your `go.mod`).
    2.  Create an MCP server configuration file (e.g., `mcp_servers.json`).
    3.  Load the MCP configuration: `mcpServers, err := mcp.LoadConfigFromFile("mcp_servers.json")`.
    4.  Prepare the engine configuration, including LLM details (API key, model) and the loaded MCP servers: `engineConfig := mcp.EngineConfig{...}`.
    5.  Initialize the engine: `engine, err := mcp.NewEngine(engineConfig)`. This starts MCP clients.
    6.  Ensure clean shutdown: `defer engine.Close()`.
    7.  Start a chat stream: `go engine.ChatStream(ctx, sessionID, prompt, history, outputChan)`.
    8.  Process events (`mcp.ChatEvent`) from `outputChan`: Handle text chunks, tool call requests from the LLM, tool results, session ID, and errors.

    **Note:** The current implementation is focused on **OpenAI-compatible LLMs** for the chat and tool-calling logic.

    See `example/example.go` for a runnable demonstration. Run it with flags, e.g.:
    ```bash
    go run example/example.go \
        -openai-key="YOUR_OPENAI_API_KEY" \
        -mcp-config="./example/mcp_servers.json" \
        -prompt="List the files in the root directory using the filesystem tool, then tell me about kubernetes pods."
    ```
*   **How it Works**: The `mcp.Engine` reads the MCP server configuration and creates corresponding MCP clients (using `github.com/mark3labs/mcp-go/client`). When `ChatStream` is called, the engine (currently the `openaiProvider`):
    1.  Fetches available tools from all connected MCP clients using `ListTools`.
    2.  Formats the user prompt, history, and available tools for the configured LLM (OpenAI API).
    3.  Sends the request to the LLM.
    4.  Processes the LLM's response stream:
        *   Yields text chunks via the output channel.
        *   If the LLM requests a tool call (using OpenAI's function calling format), the engine identifies the target MCP client based on the tool name prefix (e.g., `filesystem__list_files`).
        *   Sends a `tool_call` event.
        *   Executes the tool call via the corresponding MCP client using `CallTool`.
        *   Sends a `tool_result` event.
        *   Sends the tool result back to the LLM to continue the conversation.
    5.  Repeats the process if further tool calls are needed, until the LLM finishes.

---

**中文**

*   **项目目标**: 实现一个 Go 语言的 "MCP 引擎" 库。该库允许 Go 应用程序与大型语言模型 (LLM) 对接，并动态利用外部 MCP 服务器（遵循模型上下文协议的进程或服务）提供的工具。
*   **MCP 简介**: 模型上下文协议 (MCP) 旨在标准化应用程序向大型语言模型 (LLM) 提供上下文（如工具和数据源）的方式。它像一个通用接口，使 LLM 能够发现并利用不同外部系统提供的能力。更多信息请访问 [https://github.com/modelcontext/modelcontextprotocol](https://github.com/modelcontext/modelcontextprotocol)。
*   **配置**: MCP 服务器通过 JSON 文件（例如 `mcp_servers.json`）进行配置。该文件指定了一个从服务器名称到其配置细节的映射。该库支持通过 `stdio` (`process`) 或 `sse` 进行通信的服务器。
    *   `command`: 要运行的可执行文件（用于 `process` 类型）。
    *   `args`: 传递给命令的参数（用于 `process` 类型）。
    *   `url`: 端点 URL（用于 `sse` 类型）。
    *   `transport`: 通信方法（`"process"` 或 `"sse"`）。通常可以推断出来。
    *   `workingDir`: （可选）服务器进程的工作目录（`process` 类型）。
    *   `headers`: （可选）连接的 Headers（`sse` 类型）。

    请参阅 `example/mcp_servers.json` 中的示例（请根据您的需求创建此文件）:
    ```json
    {
      "mcpServers": {
        "filesystem": {
          "command": "npx",
          "args": [
            "-y",
            "@modelcontextprotocol/server-filesystem",
            "./fs_data" 
          ],
          "transport": "process", 
          "workingDir": "."
        },
         "kubernetes": {
           "command": "npx",
           "args": [
             "-y",
             "kubernetes-mcp-server@latest"
           ],
           "transport": "process"
         }
        // SSE 服务器配置示例 (如果您运行了一个)
        // "sse_example": {
        //   "url": "http://localhost:8080/sse",
        //   "transport": "sse",
        //   "headers": {
        //     "Authorization": "Bearer your_token"
        //   }
        // }
      }
    }
    ```
*   **使用方法 (LLM 交互与 MCP 工具)**: 
    1.  导入库: `import "github.com/Chen-speculation/MCPKit/mcp"` (使用您 `go.mod` 中正确的模块路径)。
    2.  创建一个 MCP 服务器配置文件 (例如, `mcp_servers.json`)。
    3.  加载 MCP 配置: `mcpServers, err := mcp.LoadConfigFromFile("mcp_servers.json")`。
    4.  准备引擎配置，包括 LLM 详细信息（API 密钥、模型）和加载的 MCP 服务器: `engineConfig := mcp.EngineConfig{...}`。
    5.  初始化引擎: `engine, err := mcp.NewEngine(engineConfig)`。这将启动 MCP 客户端。
    6.  确保干净地关闭: `defer engine.Close()`。
    7.  启动聊天流: `go engine.ChatStream(ctx, sessionID, prompt, history, outputChan)`。
    8.  处理来自 `outputChan` 的事件 (`mcp.ChatEvent`)：处理文本块、来自 LLM 的工具调用请求、工具结果、会话 ID 和错误。

    **注意:** 当前实现专注于**与 OpenAI 兼容的 LLM** 的聊天和工具调用逻辑。

    请参阅 `example/example.go` 获取可运行的演示。使用标志运行它，例如:
    ```bash
    go run example/example.go \
        -openai-key="您的_OPENAI_API_密钥" \
        -mcp-config="./example/mcp_servers.json" \
        -prompt="使用 filesystem 工具列出根目录中的文件，然后告诉我有关 kubernetes pod 的信息。"
    ```
*   **工作原理**: `mcp.Engine` 读取 MCP 服务器配置并创建相应的 MCP 客户端 (使用 `github.com/mark3labs/mcp-go/client`)。当调用 `ChatStream` 时，引擎（当前是 `openaiProvider`）：
    1.  使用 `ListTools` 从所有连接的 MCP 客户端获取可用工具。
    2.  为配置的 LLM (OpenAI API) 格式化用户提示、历史记录和可用工具。
    3.  将请求发送到 LLM。
    4.  处理 LLM 的响应流：
        *   通过输出通道产生文本块。
        *   如果 LLM 请求工具调用（使用 OpenAI 的 function calling 格式），引擎会根据工具名称前缀（例如 `filesystem__list_files`）识别目标 MCP 客户端。
        *   发送一个 `tool_call` 事件。
        *   通过相应的 MCP 客户端使用 `CallTool` 执行工具调用。
        *   发送一个 `tool_result` 事件。
        *   将工具结果发送回 LLM 以继续对话。
    5.  如果需要进一步的工具调用，则重复该过程，直到 LLM 完成。 