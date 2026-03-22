package client

import (
	"context"
	"edge-agent/internal/config"
	"edge-agent/internal/filemanager"
	"edge-agent/internal/local"
	"edge-agent/internal/proxy"
	"edge-agent/internal/tcp"
	"edge-agent/internal/websocket"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/creack/pty"
)

type PTYSession struct {
	ID      string
	File    *os.File
	Command *exec.Cmd
}

type Command struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	ID      string      `json:"id"`
}

type CommandResponse struct {
	Data    interface{} `json:"data,omitempty"`
	ID      string      `json:"id"`
	Error   string      `json:"error,omitempty"`
	Success bool        `json:"success"`
}

type Client struct {
	config      *config.Config
	apiClient   *proxy.APIClient
	wsClient    *websocket.WSClient
	tcpClient   *tcp.TCPClient
	protocol    string // "websocket" or "tcp"
	runningMux  sync.Mutex
	running     bool
	ptySessions map[string]*PTYSession
	ptyMux      sync.Mutex
	fileMgr     filemanager.FileManager
}

func NewClient(cfg *config.Config) *Client {
	client := &Client{
		config:      cfg,
		apiClient:   proxy.NewAPIClient(cfg),
		protocol:    cfg.WebSocket.Protocol,
		ptySessions: make(map[string]*PTYSession),
	}

	// Initialize file manager if configured
	if cfg.FileManager.BasePath != "" {
		fm, err := filemanager.NewFileManager(filemanager.Config{BasePath: cfg.FileManager.BasePath})
		if err != nil {
			log.Printf("Warning: Failed to initialize file manager: %v", err)
		} else {
			client.fileMgr = fm
		}
	}

	// Initialize client if enabled
	if cfg.WebSocket.Enabled {
		if cfg.WebSocket.Protocol == "tcp" {
			client.tcpClient = tcp.NewTCPClient()
		} else {
			client.wsClient = websocket.NewWSClient()
		}
	}

	return client
}

func (c *Client) Start(ctx context.Context) error {
	c.runningMux.Lock()
	if c.running {
		c.runningMux.Unlock()
		return fmt.Errorf("client is already running")
	}
	c.running = true
	c.runningMux.Unlock()

	log.Println("Starting socket proxy client...")

	// Start client if enabled
	if c.config.WebSocket.Enabled {
		// Set command handler
		if c.protocol == "tcp" && c.tcpClient != nil {
			c.tcpClient.SetCommandHandler(c.handleTCPCommand)
		} else if c.wsClient != nil {
			c.wsClient.SetCommandHandler(c.handleWebSocketCommand)
		}
		log.Printf("Connecting using ClientID: %s", c.config.WebSocket.ClientID)
		go c.startConnectionClient(ctx)
	} else {
		log.Println("Warning: Connection client is disabled, running in standalone mode")
	}

	return nil
}

func (c *Client) Stop() error {
	c.runningMux.Lock()
	if !c.running {
		c.runningMux.Unlock()
		return nil
	}
	c.running = false
	c.runningMux.Unlock()

	if c.wsClient != nil {
		c.wsClient.Disconnect()
	}
	if c.tcpClient != nil {
		c.tcpClient.Disconnect()
	}

	log.Println("Socket proxy client stopped")
	return nil
}

func (c *Client) handleTCPCommand(message map[string]interface{}) map[string]interface{} {
	// Extract command type and ID
	cmdType, _ := message["type"].(string)
	cmdID, _ := message["id"].(string)

	// Convert to internal Command format
	payload, _ := message["payload"]
	command := Command{
		Type:    cmdType,
		Payload: payload,
		ID:      cmdID,
	}

	// Process the command
	ctx := context.Background()
	response := c.processCommand(ctx, command)

	fmt.Printf("tcp processCommand %+v\n", response)

	// Convert response back to map
	return map[string]interface{}{
		"type":    "command_response",
		"payload": response,
		"id":      response.ID,
		"success": response.Success,
	}
}

func (c *Client) handleWebSocketCommand(message websocket.WSMessage) websocket.WSMessage {
	// Convert WebSocket message to internal Command format
	command := Command{
		Type:    message.Type,
		Payload: message.Payload,
		ID:      message.ID,
	}

	// Process the command
	ctx := context.Background()
	response := c.processCommand(ctx, command)

	fmt.Printf("ws processCommand %+v\n", response)

	// Convert response back to WebSocket message
	return websocket.WSMessage{
		Type:    "command_response",
		Payload: response,
		ID:      message.ID,
		Success: response.Success,
	}
}

func (c *Client) startConnectionClient(ctx context.Context) {
	if c.config.WebSocket.URL == "" {
		log.Println("Connection URL not configured, skipping client")
		return
	}

	// Extract host:port from URL for TCP connections
	address := c.config.WebSocket.URL
	if c.protocol == "tcp" {
		// Remove ws:// or wss:// prefix for TCP
		if len(address) > 5 && address[:5] == "ws://" {
			address = address[5:]
		} else if len(address) > 6 && address[:6] == "wss://" {
			address = address[6:]
		}
	}

	log.Printf("Starting %s client to: %s", c.protocol, address)

	reconnectAttempts := 0
	maxReconnectAttempts := c.config.WebSocket.Reconnect.MaxAttempts
	if maxReconnectAttempts == 0 {
		maxReconnectAttempts = 5 // Default value
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			log.Printf("Attempting to connect to %s server (attempt %d/%d)...",
				c.protocol, reconnectAttempts+1, maxReconnectAttempts)

			metadata := c.getSystemMetadata()
			var err error
			if c.protocol == "tcp" {
				err = c.tcpClient.Connect(ctx, address, c.config.WebSocket.ClientID, metadata)
			} else {
				err = c.wsClient.Connect(ctx, c.config.WebSocket.URL, c.config.WebSocket.ClientID, metadata)
			}

			if err != nil {
				log.Printf("❌ Failed to connect %s: %v", c.protocol, err)

				if !c.config.WebSocket.Reconnect.Enabled {
					log.Printf("%s reconnection disabled, giving up", c.protocol)
					return
				}

				reconnectAttempts++
				if reconnectAttempts >= maxReconnectAttempts {
					log.Printf("❌ Maximum reconnection attempts (%d) reached, giving up", maxReconnectAttempts)
					return
				}

				// Calculate delay with exponential backoff
				delay := c.config.WebSocket.Reconnect.InitialDelay
				for i := 1; i < reconnectAttempts; i++ {
					delay *= time.Duration(c.config.WebSocket.Reconnect.BackoffMultiplier)
				}
				if delay > c.config.WebSocket.Reconnect.MaxDelay {
					delay = c.config.WebSocket.Reconnect.MaxDelay
				}

				log.Printf("⏳ Waiting %s before next reconnection attempt...", delay)
				time.Sleep(delay)
				continue
			}

			log.Printf("✅ %s client connected successfully to %s", c.protocol, address)
			reconnectAttempts = 0 // Reset counter on successful connection

			// Keep connection alive
			for {
				if c.protocol == "tcp" {
					if !c.tcpClient.IsConnected() {
						break
					}
				} else {
					if !c.wsClient.IsConnected() {
						break
					}
				}

				select {
				case <-ctx.Done():
					if c.protocol == "tcp" {
						c.tcpClient.Disconnect()
					} else {
						c.wsClient.Disconnect()
					}
					return
				case <-time.After(30 * time.Second):
					// Send periodic status
					if c.protocol == "tcp" {
						c.tcpClient.SendCommand(map[string]interface{}{
							"type": "heartbeat",
							"payload": map[string]interface{}{
								"status":       "active",
								"client_stats": c.GetStats(),
							},
							"id": "heartbeat",
						})
					} else {
						c.wsClient.SendCommand("heartbeat", map[string]interface{}{
							"status":       "active",
							"client_stats": c.GetStats(),
						}, "heartbeat")
					}
				}
			}

			log.Printf("❌ %s connection lost", c.protocol)

			// Reconnect if enabled
			if c.config.WebSocket.Reconnect.Enabled {
				log.Printf("🔄 %s disconnected, attempting to reconnect...", c.protocol)
				time.Sleep(c.config.WebSocket.Reconnect.InitialDelay)
			} else {
				log.Printf("%s reconnection disabled, not attempting to reconnect", c.protocol)
				return
			}
		}
	}
}

func (c *Client) GetStats() map[string]interface{} {
	connected := false
	if c.protocol == "tcp" {
		connected = c.tcpClient != nil && c.tcpClient.IsConnected()
	} else {
		connected = c.wsClient != nil && c.wsClient.IsConnected()
	}

	return map[string]interface{}{
		"running":   c.running,
		"url":       c.config.WebSocket.URL,
		"protocol":  c.protocol,
		"connected": connected,
		"enabled_commands": map[string]bool{
			"api_call":      c.config.EnabledCommands.APICall,
			"http_request":  c.config.EnabledCommands.HTTPRequest,
			"local_command": c.config.EnabledCommands.LocalCommand,
		},
	}
}

func (c *Client) processCommand(ctx context.Context, command Command) CommandResponse {
	log.Printf("Processing command: %s with ID: %s", command.Type, command.ID)

	// Handle different command types
	switch command.Type {
	case "api_call":
		if !c.config.EnabledCommands.APICall {
			return CommandResponse{
				ID:      command.ID,
				Success: false,
				Error:   "api_call commands are disabled",
			}
		}
		return c.handleAPICall(ctx, command)
	case "http_request":
		if !c.config.EnabledCommands.HTTPRequest {
			return CommandResponse{
				ID:      command.ID,
				Success: false,
				Error:   "http_request commands are disabled",
			}
		}
		return c.handleHTTPRequest(ctx, command)
	case "local_command":
		if !c.config.EnabledCommands.LocalCommand {
			return CommandResponse{
				ID:      command.ID,
				Success: false,
				Error:   "local_command commands are disabled",
			}
		}
		return c.handleLocalCommand(ctx, command)
	case "interactive_shell_start":
		if !c.config.EnabledCommands.LocalCommand {
			return CommandResponse{
				ID:      command.ID,
				Success: false,
				Error:   "interactive_shell is disabled (depends on local_command)",
			}
		}
		return c.handleInteractiveShellStart(ctx, command)
	case "shell_input":
		return c.handleShellInput(ctx, command)
	case "shell_resize":
		return c.handleShellResize(ctx, command)
	case "quick_command":
		return c.handleQuickCommand(ctx, command)
	case "custom":
		return c.handleCustom(ctx, command)
	case "file_list":
		return c.handleFileList(ctx, command)
	case "file_download":
		return c.handleFileDownload(ctx, command)
	case "file_upload":
		return c.handleFileUpload(ctx, command)
	default:
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Unknown command type: %s. Supported types: api_call, http_request, local_command, quick_command, open_cell, get_cell_status, add_key, delete_key, sync_keys, reboot, status, update, custom", command.Type),
		}
	}
}

func (c *Client) handleAPICall(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "Invalid payload format for api_call",
		}
	}

	url, _ := payload["url"].(string)
	method, _ := payload["method"].(string)
	if method == "" {
		method = "POST"
	}

	var headers map[string]string
	if headersRaw, exists := payload["headers"]; exists {
		if headersMap, ok := headersRaw.(map[string]interface{}); ok {
			headers = make(map[string]string)
			for k, v := range headersMap {
				if str, ok := v.(string); ok {
					headers[k] = str
				}
			}
		}
	}

	var body interface{}
	if bodyRaw, exists := payload["body"]; exists {
		body = bodyRaw
	}

	// Make API call
	result, executeErr := c.apiClient.ExecuteAPICall(ctx, url, method, headers, body)
	if executeErr != nil {
		log.Printf("API call failed: %v", executeErr)
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("API call failed: %v", executeErr),
		}
	}

	log.Printf("API call successful: %s %s", method, url)

	return CommandResponse{
		ID:      command.ID,
		Success: result.Success,
		Data:    result.Data,
		Error:   result.Error,
	}
}

func (c *Client) handleHTTPRequest(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "Invalid payload format for http_request",
		}
	}

	url, _ := payload["url"].(string)
	method, _ := payload["method"].(string)
	if method == "" {
		method = "GET"
	}

	var headers map[string]string
	if headersRaw, exists := payload["headers"]; exists {
		if headersMap, ok := headersRaw.(map[string]interface{}); ok {
			headers = make(map[string]string)
			for k, v := range headersMap {
				if str, ok := v.(string); ok {
					headers[k] = str
				}
			}
		}
	}

	var body interface{}
	if bodyRaw, exists := payload["body"]; exists {
		body = bodyRaw
	}

	// Make HTTP request
	result, err := c.apiClient.ExecuteHTTPRequest(ctx, url, method, headers, body)
	if err != nil {
		log.Printf("HTTP request failed: %v", err)
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("HTTP request failed: %v", err),
		}
	}

	log.Printf("HTTP request successful: %s %s", method, url)

	return CommandResponse{
		ID:      command.ID,
		Success: result.Success,
		Data:    result.Data,
		Error:   result.Error,
	}
}

func (c *Client) handleLocalCommand(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "Invalid payload format for local_command",
		}
	}

	// Extract command
	commandStr, _ := payload["command"].(string)
	if commandStr == "" {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "command is required for local_command",
		}
	}

	// Extract optional parameters
	var env map[string]string
	if envData, exists := payload["env"]; exists {
		if envMap, ok := envData.(map[string]interface{}); ok {
			env = make(map[string]string)
			for k, v := range envMap {
				if str, ok := v.(string); ok {
					env[k] = str
				}
			}
		}
	}

	workDir, _ := payload["work_dir"].(string)
	var timeout time.Duration
	if timeoutStr, exists := payload["timeout"]; exists {
		if str, ok := timeoutStr.(string); ok {
			if parsed, err := time.ParseDuration(str); err == nil {
				timeout = parsed
			}
		}
	}

	// Create local client
	localClient := local.NewLocalClient()

	// Execute command locally
	localCmd := &local.LocalCommand{
		Command: commandStr,
		Env:     env,
		WorkDir: workDir,
		Timeout: timeout,
	}

	result, err := localClient.ExecuteCommand(ctx, localCmd)
	if err != nil {
		log.Printf("Failed to execute local command: %v", err)
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Command execution failed: %v", err),
		}
	}

	log.Printf("Local command executed successfully: %s", commandStr)

	return CommandResponse{
		ID:      command.ID,
		Success: result.ExitCode == 0,
		Data:    result,
		Error:   "",
	}
}

func (c *Client) handleQuickCommand(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "Invalid payload format for quick_command",
		}
	}

	// Get command name
	commandName, exists := payload["command"]
	if !exists {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "command name is required for quick_command",
		}
	}

	commandNameStr, ok := commandName.(string)
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "command name must be a string",
		}
	}

	// Get quick command from config
	quickCmd, exists := c.config.QuickCommands[commandNameStr]
	if !exists {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Quick command '%s' not found", commandNameStr),
		}
	}

	// Convert quick command to command map
	quickCmdMap, ok := quickCmd.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Invalid quick command format for '%s'", commandNameStr),
		}
	}

	// Get command type
	cmdType, exists := quickCmdMap["type"]
	if !exists {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Quick command '%s' missing type", commandNameStr),
		}
	}

	cmdTypeStr, ok := cmdType.(string)
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Quick command '%s' type must be a string", commandNameStr),
		}
	}

	// Get command payload
	cmdPayload, exists := quickCmdMap["payload"]
	if !exists {
		cmdPayload = map[string]interface{}{}
	}

	// Create new command with quick command payload
	newCommand := Command{
		Type:    cmdTypeStr,
		ID:      command.ID,
		Payload: cmdPayload,
	}

	log.Printf("Executing quick command '%s' as %s", commandNameStr, cmdTypeStr)

	// Execute the actual command
	switch cmdTypeStr {
	case "api_call":
		return c.handleAPICall(ctx, newCommand)
	case "http_request":
		return c.handleHTTPRequest(ctx, newCommand)
	case "local_command":
		return c.handleLocalCommand(ctx, newCommand)
	default:
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Unsupported quick command type: %s", cmdTypeStr),
		}
	}
}

func (c *Client) handleCustom(ctx context.Context, command Command) CommandResponse {
	log.Printf("Custom command received: %+v", command.Payload)

	return CommandResponse{
		ID:      command.ID,
		Success: true,
		Data: map[string]interface{}{
			"message":          "Custom command executed",
			"received_payload": command.Payload,
		},
	}
}

func (c *Client) handleInteractiveShellStart(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, _ := command.Payload.(map[string]interface{})
	cols := 80
	rows := 24
	if c, ok := payload["cols"].(float64); ok {
		cols = int(c)
	}
	if r, ok := payload["rows"].(float64); ok {
		rows = int(r)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Failed to start PTY: %v", err),
		}
	}

	sessionID := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	session := &PTYSession{
		ID:      sessionID,
		File:    f,
		Command: cmd,
	}

	c.ptyMux.Lock()
	c.ptySessions[sessionID] = session
	c.ptyMux.Unlock()

	// Read from PTY and send to server
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				output := string(buf[:n])
				if c.protocol == "tcp" {
					c.tcpClient.SendCommand(map[string]interface{}{
						"type": "shell_output",
						"payload": map[string]interface{}{
							"session_id": sessionID,
							"output":     output,
						},
						"id": "shell_output_" + sessionID,
					})
				} else {
					c.wsClient.SendCommand("shell_output", map[string]interface{}{
						"session_id": sessionID,
						"output":     output,
					}, "shell_output_"+sessionID)
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("PTY read error for session %s: %v", sessionID, err)
				}
				break
			}
		}
		c.cleanupPTYSession(sessionID)
	}()

	return CommandResponse{
		ID:      command.ID,
		Success: true,
		Data: map[string]interface{}{
			"session_id": sessionID,
		},
	}
}

func (c *Client) handleShellInput(ctx context.Context, command Command) CommandResponse {
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Invalid payload"}
	}

	sessionID, _ := payload["session_id"].(string)
	input, _ := payload["input"].(string)

	c.ptyMux.Lock()
	session, ok := c.ptySessions[sessionID]
	c.ptyMux.Unlock()

	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Session not found"}
	}

	_, err := session.File.WriteString(input)
	if err != nil {
		return CommandResponse{ID: command.ID, Success: false, Error: fmt.Sprintf("Write error: %v", err)}
	}

	return CommandResponse{ID: command.ID, Success: true}
}

func (c *Client) handleShellResize(ctx context.Context, command Command) CommandResponse {
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Invalid payload"}
	}

	sessionID, _ := payload["session_id"].(string)
	cols, _ := payload["cols"].(float64)
	rows, _ := payload["rows"].(float64)

	c.ptyMux.Lock()
	session, ok := c.ptySessions[sessionID]
	c.ptyMux.Unlock()

	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Session not found"}
	}

	err := pty.Setsize(session.File, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return CommandResponse{ID: command.ID, Success: false, Error: fmt.Sprintf("Resize error: %v", err)}
	}

	return CommandResponse{ID: command.ID, Success: true}
}

func (c *Client) handleFileList(ctx context.Context, command Command) CommandResponse {
	if c.fileMgr == nil {
		return CommandResponse{ID: command.ID, Success: false, Error: "File manager not initialized on agent"}
	}
	nodes, err := c.fileMgr.ListDevFolders()
	if err != nil {
		return CommandResponse{ID: command.ID, Success: false, Error: err.Error()}
	}
	absPath, _ := filepath.Abs(c.config.FileManager.BasePath)
	return CommandResponse{
		ID:      command.ID,
		Success: true,
		Data: map[string]interface{}{
			"nodes":     nodes,
			"base_path": absPath,
		},
	}
}

func (c *Client) handleFileDownload(ctx context.Context, command Command) CommandResponse {
	if c.fileMgr == nil {
		return CommandResponse{ID: command.ID, Success: false, Error: "File manager not initialized on agent"}
	}
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Invalid payload"}
	}
	path, _ := payload["path"].(string)
	data, err := c.fileMgr.DownloadFile(path)
	if err != nil {
		return CommandResponse{ID: command.ID, Success: false, Error: err.Error()}
	}
	return CommandResponse{ID: command.ID, Success: true, Data: data}
}

func (c *Client) handleFileUpload(ctx context.Context, command Command) CommandResponse {
	if c.fileMgr == nil {
		return CommandResponse{ID: command.ID, Success: false, Error: "File manager not initialized on agent"}
	}
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{ID: command.ID, Success: false, Error: "Invalid payload"}
	}
	path, _ := payload["path"].(string)

	// Data can be string (if base64 or text) or []uint8 (if binary)
	var data []byte
	if d, ok := payload["data"].([]byte); ok {
		data = d
	} else if s, ok := payload["data"].(string); ok {
		data = []byte(s)
	}

	err := c.fileMgr.UploadFile(path, data)
	if err != nil {
		return CommandResponse{ID: command.ID, Success: false, Error: err.Error()}
	}
	return CommandResponse{ID: command.ID, Success: true}
}

func (c *Client) cleanupPTYSession(sessionID string) {
	c.ptyMux.Lock()
	session, ok := c.ptySessions[sessionID]
	if ok {
		delete(c.ptySessions, sessionID)
	}
	c.ptyMux.Unlock()

	if ok {
		session.File.Close()
		session.Command.Process.Kill()
	}
}

func (c *Client) getSystemMetadata() map[string]interface{} {
	hostname, _ := os.Hostname()
	return map[string]interface{}{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"version":  "1.0.0",
	}
}
