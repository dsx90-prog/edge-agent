package client

import (
	"context"
	"edge-agent/internal/config"
	"edge-agent/internal/local"
	"edge-agent/internal/proxy"
	"edge-agent/internal/tcp"
	"edge-agent/internal/websocket"
	"fmt"
	"log"
	"sync"
	"time"
)

type Command struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	ID      string      `json:"id"`
}

type CommandResponse struct {
	ID      string      `json:"id"`
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type Client struct {
	config     *config.Config
	apiClient  *proxy.APIClient
	running    bool
	runningMux sync.Mutex
	wsClient   *websocket.WSClient
	tcpClient  *tcp.TCPClient
	protocol   string // "websocket" or "tcp"
}

func NewClient(cfg *config.Config) *Client {
	client := &Client{
		config:    cfg,
		apiClient: proxy.NewAPIClient(cfg),
		protocol:  cfg.WebSocket.Protocol,
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

	// Convert response back to map
	return map[string]interface{}{
		"type":    "command_response",
		"payload": response,
		"id":      response.ID,
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

	// Convert response back to WebSocket message
	return websocket.WSMessage{
		Type:    "command_response",
		Payload: response,
		ID:      message.ID,
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

			var err error
			if c.protocol == "tcp" {
				err = c.tcpClient.Connect(ctx, address, c.config.WebSocket.ClientID)
			} else {
				err = c.wsClient.Connect(ctx, c.config.WebSocket.URL, c.config.WebSocket.ClientID)
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
			"api_call":     c.config.EnabledCommands.APICall,
			"http_request": c.config.EnabledCommands.HTTPRequest,
			"ssh_command":  c.config.EnabledCommands.SSHCommand,
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
	case "ssh_command":
		if !c.config.EnabledCommands.SSHCommand {
			return CommandResponse{
				ID:      command.ID,
				Success: false,
				Error:   "ssh_command commands are disabled",
			}
		}
		return c.handleSSHCommand(ctx, command)
	case "quick_command":
		return c.handleQuickCommand(ctx, command)
	case "custom":
		return c.handleCustom(ctx, command)
	default:
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Unknown command type: %s. Supported types: api_call, http_request, ssh_command, quick_command, open_cell, get_cell_status, add_key, delete_key, sync_keys, reboot, status, update, custom", command.Type),
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
	result, err := c.apiClient.ExecuteAPICall(ctx, url, method, headers, body)
	if err != nil {
		log.Printf("API call failed: %v", err)
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("API call failed 1: %v", err),
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

func (c *Client) handleSSHCommand(ctx context.Context, command Command) CommandResponse {
	// Extract payload parameters
	payload, ok := command.Payload.(map[string]interface{})
	if !ok {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "Invalid payload format for ssh_command",
		}
	}

	// Extract command
	commandStr, _ := payload["command"].(string)
	if commandStr == "" {
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   "command is required for ssh_command",
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
	case "ssh_command":
		return c.handleSSHCommand(ctx, newCommand)
	default:
		return CommandResponse{
			ID:      command.ID,
			Success: false,
			Error:   fmt.Sprintf("Unsupported quick command type: %s", cmdTypeStr),
		}
	}
}

// Point-app device command handlers

//func (c *Client) handleOpenCell(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for open_cell",
//		}
//	}
//
//	cellNumber, _ := payload["cell_number"].(float64)
//	reason, _ := payload["reason"].(string)
//
//	// Simulate opening cell
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"message":     fmt.Sprintf("Cell %d opened successfully", int(cellNumber)),
//			"cell_number": int(cellNumber),
//			"reason":      reason,
//			"opened_at":   time.Now().Format(time.RFC3339),
//		},
//	}
//}

//func (c *Client) handleGetCellStatus(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for get_cell_status",
//		}
//	}
//
//	cellNumber, _ := payload["cell_number"].(float64)
//
//	log.Printf("Getting status for cell %d", int(cellNumber))
//
//	// Simulate getting cell status
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"cell_number":        int(cellNumber),
//			"is_physically_open": false,
//			"status":             "closed",
//			"code":               12345,
//			"phone":              "+1234567890",
//			"provider_id":        "provider1",
//			"occupied_time":      "2024-01-01T10:00:00Z",
//			"last_sensor_read":   time.Now().Format(time.RFC3339),
//		},
//	}
//}

//func (c *Client) handleAddKey(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for add_key",
//		}
//	}
//
//	cellNumber, _ := payload["cell_number"].(float64)
//	pinCode, _ := payload["pin_code"].(float64)
//	description, _ := payload["description"].(string)
//
//	log.Printf("Adding key %d to cell %d with description: %s", int(pinCode), int(cellNumber), description)
//
//	// Simulate adding key
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"message":     fmt.Sprintf("Key %d added to cell %d", int(pinCode), int(cellNumber)),
//			"cell_number": int(cellNumber),
//			"pin_code":    int(pinCode),
//			"description": description,
//			"added_at":    time.Now().Format(time.RFC3339),
//		},
//	}
//}

//func (c *Client) handleDeleteKey(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for delete_key",
//		}
//	}
//
//	cellNumber, _ := payload["cell_number"].(float64)
//	keyId, _ := payload["key_id"].(string)
//
//	log.Printf("Deleting key %s from cell %d", keyId, int(cellNumber))
//
//	// Simulate deleting key
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"message":     fmt.Sprintf("Key %s deleted from cell %d", keyId, int(cellNumber)),
//			"cell_number": int(cellNumber),
//			"key_id":      keyId,
//			"deleted_at":  time.Now().Format(time.RFC3339),
//		},
//	}
//}

//func (c *Client) handleSyncKeys(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for sync_keys",
//		}
//	}
//
//	addedKeys, _ := payload["added_keys"].([]interface{})
//	deletedKeys, _ := payload["deleted_keys"].([]interface{})
//
//	addedCount := len(addedKeys)
//	deletedCount := len(deletedKeys)
//
//	log.Printf("Syncing keys: %d added, %d deleted", addedCount, deletedCount)
//
//	// Simulate key synchronization
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"message":        fmt.Sprintf("Synchronization completed: %d keys added, %d keys deleted", addedCount, deletedCount),
//			"added_count":    addedCount,
//			"deleted_count":  deletedCount,
//			"sync_timestamp": time.Now().Format(time.RFC3339),
//		},
//	}
//}

//func (c *Client) handleReboot(ctx context.Context, command Command) CommandResponse {
//	log.Printf("Reboot command received")
//
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"status":  "rebooting",
//			"message": "Device will reboot in 5 seconds",
//		},
//	}
//}

//func (c *Client) handleStatus(ctx context.Context, command Command) CommandResponse {
//	log.Printf("Status command received")
//
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"name":        "point-app-device",
//			"uptime":      "2h30m15s",
//			"memory":      "256MB",
//			"cpu":         "15%",
//			"temperature": "45°C",
//			"last_boot":   "2024-01-01T08:00:00Z",
//			"version":     "1.0.0",
//			"connected":   true,
//		},
//	}
//}

//func (c *Client) handleUpdate(ctx context.Context, command Command) CommandResponse {
//	payload, ok := command.Payload.(map[string]interface{})
//	if !ok {
//		return CommandResponse{
//			ID:      command.ID,
//			Success: false,
//			Error:   "Invalid payload format for update",
//		}
//	}
//
//	version, _ := payload["version"].(string)
//
//	log.Printf("Update command received for version: %s", version)
//
//	return CommandResponse{
//		ID:      command.ID,
//		Success: true,
//		Data: map[string]interface{}{
//			"status":  "updating",
//			"version": version,
//		},
//	}
//}

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
