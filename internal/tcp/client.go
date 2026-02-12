package tcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type TCPClient struct {
	conn           net.Conn
	connected      bool
	mu             sync.RWMutex
	sendChan       chan []byte
	commandHandler func(message map[string]interface{}) map[string]interface{}
}

func NewTCPClient() *TCPClient {
	return &TCPClient{
		sendChan: make(chan []byte, 256),
	}
}

func (c *TCPClient) Connect(ctx context.Context, address, clientID string) error {
	log.Printf("Connecting to TCP server: %s (client: %s)", address, clientID)

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to connect to TCP server: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.mu.Unlock()

	log.Printf("TCP connected successfully")

	// Send identification message immediately after connection
	identification := map[string]interface{}{
		"type":      "identify",
		"client_id": clientID,
		"timestamp": time.Now().Unix(),
	}

	if err := c.SendCommand(identification); err != nil {
		log.Printf("Failed to send identification: %v", err)
	} else {
		//log.Printf("Identification message sent successfully")
	}

	// Start reader
	go c.readPump(ctx)

	// Start writer
	go c.writePump(ctx)

	return nil
}

func (c *TCPClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	c.connected = false

	if c.conn != nil {
		c.conn.Close()
	}

	log.Printf("TCP disconnected")
	return nil
}

func (c *TCPClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *TCPClient) SendCommand(payload map[string]interface{}) error {
	if !c.IsConnected() {
		return fmt.Errorf("TCP not connected")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	//log.Printf("Sending TCP message: %s", string(data))

	select {
	case c.sendChan <- data:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("send timeout")
	}
}

func (c *TCPClient) readPump(ctx context.Context) {
	defer c.Disconnect()

	buffer := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			n, err := c.conn.Read(buffer)
			if err != nil {
				log.Printf("TCP read error: %v", err)
				return
			}

			if n > 0 {
				data := buffer[:n]
				//log.Printf("Received raw TCP data: %s", string(data))
				c.handleMessage(data)
			}
		}
	}
}

func (c *TCPClient) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.sendChan:
			//log.Printf("Writing to TCP: %s", string(data))
			_, err := c.conn.Write(data)
			if err != nil {
				log.Printf("TCP write error: %v", err)
				return
			}
			//log.Printf("TCP message written successfully")
		}
	}
}

func (c *TCPClient) handleMessage(data []byte) {
	var message map[string]interface{}
	if err := json.Unmarshal(data, &message); err != nil {
		log.Printf("Invalid TCP message format: %v", err)
		log.Printf("Raw data that failed to parse: %s", string(data))
		return
	}

	//log.Printf("Received TCP command: %+v", message)

	// Сначала обрабатываем системные сообщения
	if msgType, ok := message["type"].(string); ok {
		switch msgType {
		case "identification_success":
			log.Printf("Identification successful: %+v", message)
			// Не отправляем ответ на identification_success
			return
		case "status_request":
			response := map[string]interface{}{
				"type":      "status_response",
				"connected": c.IsConnected(),
				"client_id": "tcp-client",
				"timestamp": time.Now().Unix(),
			}
			c.SendCommand(response)
			return
		case "ping":
			response := map[string]interface{}{
				"type":      "pong",
				"timestamp": time.Now().Unix(),
			}
			c.SendCommand(response)
			return
		}
	}

	// Если command handler установлен, используем его для остальных сообщений
	if c.commandHandler != nil {
		response := c.commandHandler(message)
		if response != nil {
			if err := c.SendCommand(response); err != nil {
				log.Printf("Failed to send response: %v", err)
			}
		}
		return
	}
}

func (c *TCPClient) SetCommandHandler(handler func(message map[string]interface{}) map[string]interface{}) {
	c.commandHandler = handler
}
