package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSClient struct {
	conn           *websocket.Conn
	connected      bool
	mu             sync.RWMutex
	sendChan       chan []byte
	reconnect      bool
	pingInterval   time.Duration
	commandHandler func(message WSMessage) WSMessage
}

type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	ID      string      `json:"id"`
}

func NewWSClient() *WSClient {
	return &WSClient{
		sendChan:     make(chan []byte, 256),
		pingInterval: 30 * time.Second,
	}
}

func (c *WSClient) Connect(ctx context.Context, wsURL, clientID string) error {
	log.Printf("Connecting to WebSocket: %s (client: %s)", wsURL, clientID)

	// Set dial timeout
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	// Connect to WebSocket directly without JWT
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.mu.Unlock()

	log.Printf("WebSocket connected successfully")

	// Send identification message immediately after connection
	identification := map[string]interface{}{
		"type":      "identify",
		"client_id": clientID,
		"timestamp": time.Now().Unix(),
	}

	if err := c.SendCommand("identification", identification, "init"); err != nil {
		log.Printf("Failed to send identification: %v", err)
	} else {
		log.Printf("Identification message sent successfully")
	}

	// Start reader
	go c.readPump(ctx)

	// Start writer
	go c.writePump(ctx)

	// Start ping
	go c.pingPump(ctx)

	return nil
}

func (c *WSClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	c.connected = false
	c.reconnect = false

	if c.conn != nil {
		err := c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		if err != nil {
			log.Printf("Error sending close message: %v", err)
		}
		c.conn.Close()
	}

	log.Printf("WebSocket disconnected")
	return nil
}

func (c *WSClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *WSClient) SendCommand(cmdType string, payload interface{}, id string) error {
	if !c.IsConnected() {
		return fmt.Errorf("WebSocket not connected")
	}

	message := WSMessage{
		Type:    cmdType,
		Payload: payload,
		ID:      id,
	}

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	log.Printf("Sending message: %s", string(data))

	select {
	case c.sendChan <- data:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("send timeout")
	}
}

func (c *WSClient) readPump(ctx context.Context) {
	defer c.Disconnect()

	c.conn.SetReadLimit(512 * 1024 * 1024) // 512MB max message size

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err) || websocket.IsCloseError(err) {
					log.Printf("WebSocket connection closed: %v", err)
					return
				}
				log.Printf("WebSocket read error: %v", err)
				return
			}

			// Handle incoming message
			c.handleMessage(message)
		}
	}
}

func (c *WSClient) writePump(ctx context.Context) {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.sendChan:
			log.Printf("Writing to WebSocket: %s", string(data))
			err := c.conn.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
			log.Printf("Message written successfully")
		case <-ticker.C:
			// Ping handled by pingPump
		}
	}
}

func (c *WSClient) pingPump(ctx context.Context) {
	ticker := time.NewTicker(54 * time.Second) // Send ping every 54 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.IsConnected() {
				err := c.conn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					log.Printf("WebSocket ping error: %v", err)
					return
				}
			}
		}
	}
}

func (c *WSClient) handleMessage(data []byte) {
	log.Printf("Received raw message: %s", string(data))

	var message WSMessage
	if err := json.Unmarshal(data, &message); err != nil {
		log.Printf("Invalid WebSocket message format: %v", err)
		log.Printf("Raw data that failed to parse: %s", string(data))
		return
	}

	log.Printf("Received WebSocket command: %s (ID: %s)", message.Type, message.ID)

	// Сначала обрабатываем системные сообщения
	switch message.Type {
	case "identification_success":
		log.Printf("Identification successful: %+v", message.Payload)
		// Не отправляем ответ на identification_success
		return
	case "status_request":
		// Send status back
		status := map[string]interface{}{
			"connected": c.IsConnected(),
			"client_id": "websocket-client",
			"timestamp": time.Now().Unix(),
		}
		c.SendCommand("status_response", status, message.ID)
		return
	case "ping":
		c.SendCommand("pong", map[string]interface{}{
			"timestamp": time.Now().Unix(),
		}, message.ID)
		return
	}

	// Если command handler установлен, используем его для остальных сообщений
	if c.commandHandler != nil {
		response := c.commandHandler(message)
		if response.Type != "" {
			responseData, err := json.Marshal(response)
			if err != nil {
				log.Printf("Failed to marshal response: %v", err)
				return
			}

			select {
			case c.sendChan <- responseData:
			case <-time.After(5 * time.Second):
				log.Printf("Failed to send response: timeout")
			}
		}
		return
	}
}

func (c *WSClient) SetCommandHandler(handler func(message WSMessage) WSMessage) {
	c.commandHandler = handler
}
