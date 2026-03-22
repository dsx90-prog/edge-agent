package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		AgentPort string `yaml:"agent_port"`
		SSHPort   string `yaml:"ssh_port"`
		WebPort   string `yaml:"web_port"`
	} `yaml:"server"`
	Commands  map[string]CommandTemplate `yaml:"commands"`
	Templates map[string]CommandTemplate `yaml:"templates"`
}

type CommandTemplate struct {
	Type    string      `yaml:"type" json:"type"`
	Payload interface{} `yaml:"payload" json:"payload"`
}

// AgentManager manages connected edge agents
// Agents can be connected via raw TCP or WebSocket
type AgentManager struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	nextID int
}

type Agent struct {
	ID       string
	tcpConn  net.Conn        // for raw TCP connections
	wsConn   *websocket.Conn // for WebSocket connections
	sendMu   sync.Mutex
	mu       sync.Mutex // Add a mutex for protecting agent-specific fields like Metadata
	Metadata map[string]interface{}
}

// Write sends a JSON message to the agent (works for both TCP and WebSocket)
func (a *Agent) Write(data []byte) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if a.wsConn != nil {
		return a.wsConn.WriteMessage(websocket.TextMessage, data)
	}
	_, err := a.tcpConn.Write(append(data, '\n'))
	return err
}

func (a *Agent) Close() {
	if a.wsConn != nil {
		a.wsConn.Close()
	}
	if a.tcpConn != nil {
		a.tcpConn.Close()
	}
}

func NewAgentManager() *AgentManager {
	return &AgentManager{
		agents: make(map[string]*Agent),
		nextID: 1,
	}
}

func (am *AgentManager) AddTCPAgent(conn net.Conn) string {
	am.mu.Lock()
	defer am.mu.Unlock()
	id := fmt.Sprintf("agent-%d", am.nextID)
	am.nextID++
	am.agents[id] = &Agent{ID: id, tcpConn: conn}
	return id
}

func (am *AgentManager) AddWSAgent(conn *websocket.Conn) string {
	am.mu.Lock()
	defer am.mu.Unlock()
	id := fmt.Sprintf("agent-%d", am.nextID)
	am.nextID++
	am.agents[id] = &Agent{ID: id, wsConn: conn}
	return id
}

func (am *AgentManager) RemoveAgent(id string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	if a, ok := am.agents[id]; ok {
		a.Close()
		delete(am.agents, id)
	}
}

func (am *AgentManager) GetAgent(id string) (*Agent, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	a, ok := am.agents[id]
	return a, ok
}

func (am *AgentManager) ListAgents() []string {
	am.mu.RLock()
	defer am.mu.RUnlock()
	var ids []string
	for id := range am.agents {
		ids = append(ids, id)
	}
	return ids
}

var (
	configPath string
	appConfig  Config
	am         *AgentManager
)

func init() {
	flag.StringVar(&configPath, "config", "test-server/config.yml", "path to config file")
}

func loadConfig() error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, &appConfig)
}

func main() {
	flag.Parse()
	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	am = NewAgentManager()

	am = NewAgentManager()

	log.Printf("Loaded %d command templates from %s", len(appConfig.Commands), configPath)
	log.Printf("Agent server port: %s", appConfig.Server.AgentPort)
	log.Printf("SSH server port: %s", appConfig.Server.SSHPort)
	log.Printf("Web server port: %s", appConfig.Server.WebPort)

	// Start TCP server for agents
	go startTCPServer(appConfig.Server.AgentPort)

	// Start SSH server
	go startSSHServer(appConfig.Server.SSHPort)

	// Start Web server
	if appConfig.Server.WebPort != "" {
		go startWebServer(appConfig.Server.WebPort)
	} else {
		log.Println("Web server port not configured, skipping")
	}

	// Keep main process alive
	select {}
}

// Web Server and REST API Implementation

func startWebServer(port string) {
	mux := http.NewServeMux()

	// REST API Endpoints
	mux.HandleFunc("/api/agents", handleAPIAgents)
	mux.HandleFunc("/api/commands", handleAPICommands)
	mux.HandleFunc("/api/templates", handleAPITemplates)
	mux.HandleFunc("/api/send", handleAPISend)

	// WebSocket SSH proxy (browser terminal <-> SSH server)
	mux.HandleFunc("/terminal", handleTerminalWS)

	// WebSocket agent terminal — интерактивный терминал конкретного агента
	mux.HandleFunc("/agent-terminal", handleAgentTerminalWS)

	// File Manager Endpoints
	mux.HandleFunc("/api/files/list", handleFileList)
	mux.HandleFunc("/api/files/download", handleFileDownload)
	mux.HandleFunc("/api/files/upload", handleFileUpload)

	// Serve static files (SPA)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "test-server/index.html")
	})

	log.Printf("Web server listening on %s", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Failed to start Web server on %s: %v", port, err)
	}
}

// handleTerminalWS proxies browser WebSocket <-> local SSH server
func handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	wsConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Terminal WS upgrade error: %v", err)
		return
	}
	defer wsConn.Close()

	sshPort := appConfig.Server.SSHPort
	if sshPort == "" {
		sshPort = ":2222"
	}
	sshAddr := "127.0.0.1" + sshPort

	tcpConn, err := net.DialTimeout("tcp", sshAddr, 5*time.Second)
	if err != nil {
		log.Printf("Cannot connect to SSH server: %v", err)
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\nFailed to connect to SSH server: "+err.Error()+"\r\n"))
		return
	}
	defer tcpConn.Close()

	log.Printf("Browser terminal connected, proxying to SSH %s", sshAddr)

	// Browser -> SSH
	go func() {
		for {
			_, msg, err := wsConn.ReadMessage()
			if err != nil {
				tcpConn.Close()
				return
			}
			if _, err := tcpConn.Write(msg); err != nil {
				return
			}
		}
	}()

	// SSH -> Browser
	buf := make([]byte, 4096)
	for {
		n, err := tcpConn.Read(buf)
		if n > 0 {
			wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func handleAPIAgents(w http.ResponseWriter, r *http.Request) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	type AgentInfo struct {
		ID       string                 `json:"id"`
		Metadata map[string]interface{} `json:"metadata"`
	}

	var agentInfos []AgentInfo
	for id, a := range am.agents {
		a.mu.Lock() // Lock agent's mutex to safely read metadata
		metadataCopy := make(map[string]interface{})
		for k, v := range a.Metadata {
			metadataCopy[k] = v
		}
		a.mu.Unlock()

		agentInfos = append(agentInfos, AgentInfo{
			ID:       id,
			Metadata: metadataCopy,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agentInfos)
}

func handleAPICommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appConfig.Commands)
}

func handleAPITemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appConfig.Templates)
}

func handleAPISend(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AgentID string      `json:"agent_id"`
		Type    string      `json:"type"`
		Payload interface{} `json:"payload"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.AgentID == "" || req.Type == "" {
		http.Error(w, "agent_id and type are required", http.StatusBadRequest)
		return
	}

	// We can reuse the core logic but without a terminal output.
	// We'll write a helper function to avoid duplicating the send logic.
	resp, err := sendCommandToAgentCore(req.AgentID, req.Type, req.Payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Helper decoupled from the Terminal to return a response map directly
func sendCommandToAgentCore(agentID string, cmdType string, payload interface{}) (map[string]interface{}, error) {
	agent, ok := am.GetAgent(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %s is no longer connected", agentID)
	}

	cmdID := fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	msg := map[string]interface{}{
		"type":    cmdType,
		"payload": payload,
		"id":      cmdID,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode command: %v", err)
	}

	respChan := make(chan map[string]interface{}, 1)
	responseChansMu.Lock()
	responseChans[cmdID] = respChan
	responseChansMu.Unlock()

	defer func() {
		responseChansMu.Lock()
		delete(responseChans, cmdID)
		responseChansMu.Unlock()
	}()

	err = agent.Write(msgBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to send command to agent: %v", err)
	}

	select {
	case resp := <-respChan:
		return resp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response from agent")
	}
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func startTCPServer(port string) {
	mux := http.NewServeMux()

	// WebSocket endpoint — подключение агентов через WebSocket
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade error: %v", err)
			return
		}
		agentID := am.AddWSAgent(conn)
		log.Printf("New WebSocket agent connected: %s from %s", agentID, conn.RemoteAddr().String())
		go handleWSAgentConnection(agentID, conn)
	})

	// HTTP endpoint — raw TCP агенты подключаются через /tcp
	// Но основной путь — TCP-листенер ниже
	go func() {
		log.Printf("Agent server (HTTP+WS) listening on %s", port)
		if err := http.ListenAndServe(port, mux); err != nil {
			log.Fatalf("Agent HTTP server error: %v", err)
		}
	}()

	// Raw TCP listener для агентов с protocol: tcp
	tcpPort := ":8082"
	listener, err := net.Listen("tcp", tcpPort)
	if err != nil {
		log.Printf("Warning: Could not start raw TCP server on %s: %v", tcpPort, err)
		return
	}
	defer listener.Close()
	log.Printf("Raw TCP agent server also listening on %s", tcpPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Error accepting TCP connection: %v", err)
			continue
		}
		agentID := am.AddTCPAgent(conn)
		log.Printf("New raw TCP agent connected: %s from %s", agentID, conn.RemoteAddr().String())
		go handleRawTCPAgentConnection(agentID, conn)
	}
}

func handleWSAgentConnection(agentID string, wsConn *websocket.Conn) {
	defer func() {
		log.Printf("WebSocket agent disconnected: %s", agentID)
		am.RemoveAgent(agentID)
	}()

	for {
		_, msgBytes, err := wsConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("WebSocket read error from %s: %v", agentID, err)
			}
			return
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("Invalid WS message from %s: %v", agentID, err)
			continue
		}
		// Используем GetAgent для безопасного доступа с мьютексом
		agent, _ := am.GetAgent(agentID)
		handleAgentMessage(agentID, msg, agent)
	}
}

func handleRawTCPAgentConnection(agentID string, conn net.Conn) {
	defer func() {
		log.Printf("TCP agent disconnected: %s", agentID)
		am.RemoveAgent(agentID)
	}()
	handleAgentConnection(agentID, conn)
}

// Global response channel map to route responses to callers
var (
	responseChansMu sync.RWMutex
	responseChans   = make(map[string]chan map[string]interface{})

	ptyWSConnectionsMu sync.RWMutex
	ptyWSConnections   = make(map[string]*websocket.Conn)
)

func handleAgentConnection(agentID string, conn net.Conn) {
	decoder := json.NewDecoder(conn)
	for {
		var msg map[string]interface{}
		if err := decoder.Decode(&msg); err != nil {
			if err != io.EOF {
				log.Printf("Error reading from agent %s: %v", agentID, err)
			}
			return
		}
		agent, _ := am.GetAgent(agentID)
		handleAgentMessage(agentID, msg, agent)
	}
}

func handleAgentMessage(agentID string, msg map[string]interface{}, agent *Agent) {
	msgType, _ := msg["type"].(string)
	msgType = strings.ToLower(strings.TrimSpace(msgType))

	switch msgType {
	case "identify", "identification":
		// Metadata can be in the top level or inside payload
		var data map[string]interface{}
		if p, ok := msg["payload"].(map[string]interface{}); ok {
			data = p
		} else {
			data = msg
		}

		clientID, _ := data["client_id"].(string)
		metadata, _ := data["metadata"].(map[string]interface{})

		if (metadata == nil || len(metadata) == 0) && clientID != "" {
			log.Printf("Warning: Agent %s identified as %s but sent NO METADATA. Is it running old code?", agentID, clientID)
		}

		log.Printf("Agent identification: client_id=%s, metadata=%+v", clientID, metadata)

		if agent != nil {
			agent.mu.Lock()
			agent.Metadata = metadata
			agent.mu.Unlock()

			// Optionally update the ID in the manager if client_id is provided
			if clientID != "" && clientID != agentID {
				am.mu.Lock()
				delete(am.agents, agentID)
				agent.ID = clientID
				am.agents[clientID] = agent
				am.mu.Unlock()
				log.Printf("Agent ID updated from %s to %s", agentID, clientID)
			}
		}

		// Send success response
		agent.Write(encodeAgentCmd("identification_success", map[string]interface{}{
			"status": "ok",
		}))

	case "heartbeat":
		log.Printf("Heartbeat from %s", agentID)

	case "ping":
		resp := map[string]interface{}{
			"type":      "pong",
			"timestamp": time.Now().Unix(),
		}
		respBytes, _ := json.Marshal(resp)
		if agent != nil {
			agent.Write(respBytes)
		}

	case "command_response":
		id, _ := msg["id"].(string)
		responseChansMu.RLock()
		ch, exists := responseChans[id]
		responseChansMu.RUnlock()
		if exists {
			ch <- msg
		} else {
			log.Printf("Response for unknown cmd ID %s from %s", id, agentID)
		}

	case "shell_output":
		payload, _ := msg["payload"].(map[string]interface{})
		sessionID, _ := payload["session_id"].(string)
		output, _ := payload["output"].(string)
		ptyWSConnectionsMu.RLock()
		ws, ok := ptyWSConnections[sessionID]
		ptyWSConnectionsMu.RUnlock()
		if ok {
			ws.WriteMessage(websocket.TextMessage, []byte(output))
		}

	default:
		if msgType != "" {
			log.Printf("Agent %s: unknown message type: %s (Raw: %+v)", agentID, msgType, msg)
		}
	}
}

// SSH Server Implementation

func startSSHServer(port string) {
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "admin" && string(pass) == "admin" {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", c.User())
		},
	}

	// Generate a private key for the server
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("Failed to generate private key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		log.Fatalf("Failed to create signer: %v", err)
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen for SSH on %s: %v", port, err)
	}
	defer listener.Close()
	log.Printf("SSH server listening on %s", port)

	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept incoming SSH connection: %v", err)
			continue
		}
		go handleSSHConnection(nConn, config)
	}
}

func handleSSHConnection(nConn net.Conn, config *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		log.Printf("Failed to handshake SSH: %v", err)
		return
	}
	log.Printf("New SSH connection from %s (%s)", conn.RemoteAddr(), conn.ClientVersion())

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("Could not accept channel: %v", err)
			continue
		}

		go func(in <-chan *ssh.Request) {
			for req := range in {
				req.Reply(req.Type == "shell" || req.Type == "pty-req", nil)
			}
		}(requests)

		go handleSSHSession(channel)
	}
}

func handleSSHSession(channel ssh.Channel) {
	defer channel.Close()

	termInstance := term.NewTerminal(channel, "edge-agent-test> ")
	termInstance.Write([]byte("Welcome to Edge Agent Test Server CLI\r\n"))
	termInstance.Write([]byte("Type 'help' for available commands.\r\n\n"))

	selectedAgent := ""

	for {
		if selectedAgent != "" {
			termInstance.SetPrompt(fmt.Sprintf("[%s]> ", selectedAgent))
		} else {
			termInstance.SetPrompt("edge-agent-test> ")
		}

		line, err := termInstance.ReadLine()
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, " ")
		cmd := parts[0]

		switch cmd {
		case "help":
			termInstance.Write([]byte("Available commands:\r\n"))
			termInstance.Write([]byte("  list                                         - List connected agents\r\n"))
			termInstance.Write([]byte("  select <agent_id>                            - Select an agent for sending commands\r\n"))
			termInstance.Write([]byte("  unselect                                     - Unselect current agent\r\n"))
			termInstance.Write([]byte("  cmds                                         - List predefined commands from config\r\n"))
			termInstance.Write([]byte("  send <cmd_name>                              - Send predefined command to selected agent\r\n"))
			termInstance.Write([]byte("  custom <type> <json_payload>                 - Send arbitrary command to selected agent\r\n"))
			termInstance.Write([]byte("  exit                                         - Exit session\r\n"))

		case "list":
			agents := am.ListAgents()
			termInstance.Write([]byte(fmt.Sprintf("\r\nConnected agents (%d):\r\n", len(agents))))
			for _, a := range agents {
				termInstance.Write([]byte(fmt.Sprintf("  - %s\r\n", a)))
			}
			termInstance.Write([]byte("\r\n"))

		case "select":
			if len(parts) < 2 {
				termInstance.Write([]byte("Usage: select <agent_id>\r\n"))
				continue
			}
			agentID := parts[1]
			if _, ok := am.GetAgent(agentID); ok {
				selectedAgent = agentID
				termInstance.Write([]byte(fmt.Sprintf("Selected agent: %s\r\n", selectedAgent)))
			} else {
				termInstance.Write([]byte(fmt.Sprintf("Agent %s not found or disconnected.\r\n", agentID)))
			}

		case "unselect":
			selectedAgent = ""

		case "cmds":
			termInstance.Write([]byte("\r\nPredefined commands:\r\n"))
			for name, c := range appConfig.Commands {
				termInstance.Write([]byte(fmt.Sprintf("  - %s (type: %s)\r\n", name, c.Type)))
			}
			termInstance.Write([]byte("\r\n"))

		case "send":
			if selectedAgent == "" {
				termInstance.Write([]byte("No agent selected. Use 'select <agent_id>' first.\r\n"))
				continue
			}
			if len(parts) < 2 {
				termInstance.Write([]byte("Usage: send <cmd_name>\r\n"))
				continue
			}
			cmdName := parts[1]
			tpl, exists := appConfig.Commands[cmdName]
			if !exists {
				termInstance.Write([]byte(fmt.Sprintf("Command '%s' not found.\r\n", cmdName)))
				continue
			}

			// execute sending
			err := sendCommandToAgent(termInstance, selectedAgent, tpl.Type, tpl.Payload)
			if err != nil {
				termInstance.Write([]byte(fmt.Sprintf("Error: %v\r\n", err)))
			}

		case "custom":
			if selectedAgent == "" {
				termInstance.Write([]byte("No agent selected. Use 'select <agent_id>' first.\r\n"))
				continue
			}
			if len(parts) < 3 {
				termInstance.Write([]byte("Usage: custom <type> <json_payload>\r\n"))
				continue
			}
			cmdType := parts[1]
			payloadStr := strings.Join(parts[2:], " ")

			var payload interface{}
			if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
				termInstance.Write([]byte(fmt.Sprintf("Invalid JSON payload: %v\r\n", err)))
				continue
			}

			err := sendCommandToAgent(termInstance, selectedAgent, cmdType, payload)
			if err != nil {
				termInstance.Write([]byte(fmt.Sprintf("Error: %v\r\n", err)))
			}

		case "exit":
			termInstance.Write([]byte("Goodbye!\r\n"))
			return

		default:
			termInstance.Write([]byte(fmt.Sprintf("Unknown command: %s. Type 'help'.\r\n", cmd)))
		}
	}
}

func sendCommandToAgent(termInstance *term.Terminal, agentID string, cmdType string, payload interface{}) error {
	termInstance.Write([]byte(fmt.Sprintf("Sending command (type: %s)...\r\n", cmdType)))

	resp, err := sendCommandToAgentCore(agentID, cmdType, payload)
	if err != nil {
		return err
	}

	respBytes, _ := json.MarshalIndent(resp, "", "  ")
	termInstance.Write([]byte(fmt.Sprintf("<<< Response received:\r\n%s\r\n", string(respBytes))))
	return nil
}

// handleAgentTerminalWS implements interactive browser terminal for a specific agent using PTY.
func handleAgentTerminalWS(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		http.Error(w, "agent query param required", http.StatusBadRequest)
		return
	}

	agent, ok := am.GetAgent(agentID)
	if !ok {
		http.Error(w, "agent not found or disconnected", http.StatusNotFound)
		return
	}

	colsStr := r.URL.Query().Get("cols")
	rowsStr := r.URL.Query().Get("rows")
	cols, _ := strconv.Atoi(colsStr)
	rows, _ := strconv.Atoi(rowsStr)
	if cols <= 0 {
		cols = 100
	}
	if rows <= 0 {
		rows = 30
	}

	wsConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Agent terminal WS upgrade error: %v", err)
		return
	}
	defer wsConn.Close()

	// 1. Request interactive shell start
	resp, err := sendCommandToAgentCore(agentID, "interactive_shell_start", map[string]interface{}{
		"cols": cols,
		"rows": rows,
	})
	if err != nil {
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\n\033[31mError starting shell: "+err.Error()+"\033[0m\r\n"))
		return
	}

	payloadRaw, _ := resp["payload"].(map[string]interface{})
	data, _ := payloadRaw["data"].(map[string]interface{})
	sessionID, _ := data["session_id"].(string)
	if sessionID == "" {
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\n\033[31mError: No session_id received from agent\033[0m\r\n"))
		return
	}

	// 2. Register WS for shell_output routing
	ptyWSConnectionsMu.Lock()
	ptyWSConnections[sessionID] = wsConn
	ptyWSConnectionsMu.Unlock()

	defer func() {
		ptyWSConnectionsMu.Lock()
		delete(ptyWSConnections, sessionID)
		ptyWSConnectionsMu.Unlock()
		log.Printf("PTY session %s closed", sessionID)
	}()

	log.Printf("Interactive shell session %s started for agent %s", sessionID, agentID)

	// 3. Loop: Browser -> Agent
	for {
		_, msgBytes, err := wsConn.ReadMessage()
		if err != nil {
			break
		}

		// Try to parse as JSON for special events (e.g., resize)
		var customMsg struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}

		if err := json.Unmarshal(msgBytes, &customMsg); err == nil && customMsg.Type == "resize" {
			// Send shell_resize
			agent.Write(encodeAgentCmd("shell_resize", map[string]interface{}{
				"session_id": sessionID,
				"cols":       customMsg.Cols,
				"rows":       customMsg.Rows,
			}))
		} else {
			// Regular input
			input := string(msgBytes)
			agent.Write(encodeAgentCmd("shell_input", map[string]interface{}{
				"session_id": sessionID,
				"input":      input,
			}))
		}
	}
}

func encodeAgentCmd(cmdType string, payload interface{}) []byte {
	msg := map[string]interface{}{
		"type":    cmdType,
		"payload": payload,
		"id":      fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
	}
	b, _ := json.Marshal(msg)
	return b
}

func handleFileList(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		agents := am.ListAgents()
		if len(agents) == 0 {
			http.Error(w, "No agents connected", http.StatusServiceUnavailable)
			return
		}
		agentID = agents[0]
	}

	resp, err := sendCommandToAgentCore(agentID, "file_list", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !resp["success"].(bool) {
		payload, _ := resp["payload"].(map[string]interface{})
		errMsg := resp["error"]
		if payload != nil && payload["error"] != nil {
			errMsg = payload["error"]
		}
		http.Error(w, fmt.Sprintf("%v", errMsg), http.StatusInternalServerError)
		return
	}

	payload, _ := resp["payload"].(map[string]interface{})
	data := resp["data"]
	if payload != nil && payload["data"] != nil {
		data = payload["data"]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func handleFileDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if agentID == "" {
		agents := am.ListAgents()
		if len(agents) == 0 {
			http.Error(w, "No agents connected", http.StatusServiceUnavailable)
			return
		}
		agentID = agents[0]
	}

	resp, err := sendCommandToAgentCore(agentID, "file_download", map[string]interface{}{"path": relPath})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !resp["success"].(bool) {
		payload, _ := resp["payload"].(map[string]interface{})
		errMsg := resp["error"]
		if payload != nil && payload["error"] != nil {
			errMsg = payload["error"]
		}
		http.Error(w, fmt.Sprintf("%v", errMsg), http.StatusInternalServerError)
		return
	}

	payload, _ := resp["payload"].(map[string]interface{})
	var dataStr string
	if payload != nil && payload["data"] != nil {
		dataStr, _ = payload["data"].(string)
	} else {
		dataStr, _ = resp["data"].(string)
	}

	if dataStr == "" {
		http.Error(w, "No data received from agent", http.StatusInternalServerError)
		return
	}

	// data is base64 encoded string by json.Marshal in agent
	data, err := base64.StdEncoding.DecodeString(dataStr)
	if err != nil {
		// Try treating it as raw string if not base64
		data = []byte(dataStr)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(relPath)))
	w.Write(data)
}

func handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if agentID == "" {
		agents := am.ListAgents()
		if len(agents) == 0 {
			http.Error(w, "No agents connected", http.StatusServiceUnavailable)
			return
		}
		agentID = agents[0]
	}

	fileData, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Send as base64 to avoid issues with binary data in JSON string fields
	dataBase64 := base64.StdEncoding.EncodeToString(fileData)

	resp, err := sendCommandToAgentCore(agentID, "file_upload", map[string]interface{}{
		"path": relPath,
		"data": dataBase64,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !resp["success"].(bool) {
		payload, _ := resp["payload"].(map[string]interface{})
		errMsg := resp["error"]
		if payload != nil && payload["error"] != nil {
			errMsg = payload["error"]
		}
		http.Error(w, fmt.Sprintf("%v", errMsg), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, "File uploaded successfully to agent")
}
