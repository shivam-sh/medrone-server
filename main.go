package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
    ClientTypeRelay = iota
    ClientTypeDashboard
)

var upgrader = websocket.Upgrader{
    // Allow all origins for testing
    CheckOrigin: func(r *http.Request) bool {
        return true
    },
    // Add reasonable buffer sizes
    ReadBufferSize:  1024,
    WriteBufferSize: 1024,
}

// Hub maintains the set of active clients and broadcast channels
type Hub struct {
    // Mutex to protect concurrent access to maps
    mu       sync.RWMutex
    
    // Map of clients to their active status
    Clients map[*Client]bool
    
    // Map of channel IDs to their connected clients
    Channels map[string]map[*Client]bool
}

func newHub() *Hub {
    return &Hub{
        Clients:  make(map[*Client]bool),
        Channels: make(map[string]map[*Client]bool),
    }
}

// registerClient adds a client to a specific channel
func (h *Hub) registerClient(client *Client) {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // Add client to the general clients map
    h.Clients[client] = true
    
    // Create the channel if it doesn't exist
    if _, exists := h.Channels[client.ChannelID]; !exists {
        h.Channels[client.ChannelID] = make(map[*Client]bool)
    }
    
    // Add client to the specific channel
    h.Channels[client.ChannelID][client] = true
}

// unregisterClient removes a client from its channel
func (h *Hub) unregisterClient(client *Client) {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // Remove from general clients map
    delete(h.Clients, client)
    
    // Remove from channel-specific map
    if clients, exists := h.Channels[client.ChannelID]; exists {
        delete(clients, client)
        
        // Clean up empty channels
        if len(clients) == 0 {
            delete(h.Channels, client.ChannelID)
        }
    }
}

// broadcastToChannel sends a message to all clients in a specific channel
func (h *Hub) broadcastToChannel(channelID string, messageType int, message []byte, senderType int) {
    // Get a snapshot of clients to avoid holding the lock during sends
    h.mu.RLock()
    if clients, exists := h.Channels[channelID]; exists && len(clients) > 0 {
        // Create a copy of clients to safely iterate over
        clientsCopy := make([]*Client, 0, len(clients))
        for client := range clients {
            if client.ClientType != senderType {
                clientsCopy = append(clientsCopy, client)
            }
        }
        h.mu.RUnlock()

        // Process all messages sequentially for simplicity (small number of clients)
        for _, client := range clientsCopy {
            if err := client.writeMessage(messageType, message); err != nil {
                log.Printf("Error broadcasting to client: %v", err)
                client.Conn.Close()
                h.removeClient(client) // Direct call instead of goroutine
            }
        }
    } else {
        h.mu.RUnlock()
    }
}

// removeClient safely removes a client after a connection error
func (h *Hub) removeClient(client *Client) {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // Check if client still exists before attempting to delete
    if _, exists := h.Clients[client]; exists {
        delete(h.Clients, client)
        
        if clients, exists := h.Channels[client.ChannelID]; exists {
            delete(clients, client)
            
            // Clean up empty channels
            if len(clients) == 0 {
                delete(h.Channels, client.ChannelID)
            }
        }
    }
}

// Client represents a connected WebSocket client
type Client struct {
    Conn       *websocket.Conn
    ClientType int
    ChannelID  string
    mu         sync.Mutex // Protect the connection from concurrent writes
}

// writeMessage thread-safe wrapper for writing to the websocket
func (c *Client) writeMessage(messageType int, message []byte) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.Conn.WriteMessage(messageType, message)
}

// Simple status message
type StatusMessage struct {
    Status  string `json:"status"`
    Message string `json:"message"`
}

func main() {
    r := gin.Default()

    // Basic REST endpoint
    r.GET("/health", func(c *gin.Context) {
        c.JSON(200, StatusMessage{
            Status:  "ok",
            Message: "Server is running",
        })
    })

    r.GET("/", func(c *gin.Context) {
        c.JSON(200, StatusMessage {
            Status: "ok",
            Message: "Hello World",
        })
    })

    hub := newHub()

    // WebSocket endpoint
    r.GET("/ws", func(c *gin.Context) {
        clientIP := c.ClientIP()
        clientType := c.Request.URL.Query().Get("type")
        channelID := c.Request.URL.Query().Get("channel")
        
        log.Printf("WebSocket connection attempt from %s - Type: %s, Channel: %s", 
            clientIP, clientType, channelID)
        
        // Upgrade HTTP connection to WebSocket
        ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
        if err != nil {
            log.Printf("Failed to upgrade connection: %v", err)
            return
        }

        // Get client type and channel ID from query parameters
        clientTypeInt := getClientType(clientType)
        
        // Validate channel ID (should be a 6-digit number)
        if !isValidChannelID(channelID) {
            log.Printf("Invalid channel ID: %s", channelID)
            ws.WriteMessage(websocket.TextMessage, []byte("Invalid channel ID. Must be a 6-digit number."))
            ws.Close()
            return
        }

        // Create a new Client
        client := &Client{
            Conn:       ws,
            ClientType: clientTypeInt,
            ChannelID:  channelID,
        }
        
        // Register client with the hub
        hub.registerClient(client)
        log.Printf("Client connected: IP=%s, Type=%s, Channel=%s", 
            clientIP, clientType, channelID)
        
        // Handle client messages
        handleClient(client, hub)
    })

    // Start server
    log.Println("Starting server on :4703")
    if err := r.Run(":4703"); err != nil {
        log.Fatal("Server failed to start:", err)
    }
}

func handleClient(client *Client, hub *Hub) {
    defer func() {
        client.Conn.Close()
        hub.unregisterClient(client)
        log.Printf("Client disconnected: Type=%d, Channel=%s", 
            client.ClientType, client.ChannelID)
    }()

    // Simple buffer for messages that need to be processed
    msgChan := make(chan struct {
        msgType int
        msg     []byte
    }, 5) // Small buffer is enough for a few clients

    // Start message processor goroutine
    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        for msgData := range msgChan {
            messageType := msgData.msgType
            message := msgData.msg

            // Process all messages sequentially for simplicity
            if len(message) > 0 && message[0] == 0x04 {
                // Just log video frames without the message content
                log.Printf("Message from %d to %s: Type=videoFrame (0x04), Size=%d bytes", 
                    client.ClientType, client.ChannelID, len(message))
                
                // Broadcast directly, no need for a separate goroutine with few clients
                hub.broadcastToChannel(client.ChannelID, messageType, message, client.ClientType)
            } else {
                // For other messages, process normally
                hub.broadcastToChannel(client.ChannelID, messageType, message, client.ClientType)
                
                // Skip logging heartbeat messages
                if string(message) == "heartbeat" {
                    continue
                }
                
                // Log message details
                if messageType == websocket.BinaryMessage && len(message) > 0 {
                    var msgTypeName string
                    
                    switch message[0] {
                    case 0x01:
                        msgTypeName = "controlData"
                    case 0x02:
                        msgTypeName = "flightState"
                    case 0x03:
                        msgTypeName = "batteryState"
                    default:
                        msgTypeName = "unknown"
                    }
                    
                    log.Printf("Message from %d to %s: Type=%s (0x%02x), Size=%d bytes", 
                        client.ClientType, client.ChannelID, msgTypeName, message[0], len(message))
                } else {
                    // For text messages, truncate long content
                    msgContent := string(message)
                    if len(msgContent) > 100 {
                        msgContent = msgContent[:100] + "... [truncated]"
                    }
                    log.Printf("Message from %d to %s: %s", 
                        client.ClientType, client.ChannelID, msgContent)
                }
            }
        }
    }()

    // Read messages from WebSocket
    for {
        messageType, message, err := client.Conn.ReadMessage()
        if err != nil {
            if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
                log.Printf("Error reading message: %v", err)
            }
            break
        }

        // Send to processing goroutine
        select {
        case msgChan <- struct {
            msgType int
            msg     []byte
        }{messageType, message}:
            // Message sent to channel successfully
        default:
            // Channel is full, log warning and drop message
            if len(message) > 0 && message[0] == 0x04 {
                log.Printf("Warning: Dropped video frame message due to processing backlog")
            } else {
                log.Printf("Warning: Message processing backlog, dropped message")
            }
        }
    }

    // Close the message channel and wait for processor to finish
    close(msgChan)
    wg.Wait()
}

func getClientType(typeStr string) int {
    switch typeStr {
    case "relay":
        return ClientTypeRelay
    case "dashboard":
        return ClientTypeDashboard
    default:
        return -1 // Invalid client type
    }
}

// isValidChannelID checks if the channel ID is a 6-digit number
func isValidChannelID(channelID string) bool {
    if len(channelID) != 6 {
        return false
    }
    
    for _, c := range channelID {
        if c < '0' || c > '9' {
            return false
        }
    }
    
    return true
}
