package main

import (
   // "encoding/json"
    "log"
    "net/http"

    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
    // Allow all origins for testing
    CheckOrigin: func(r *http.Request) bool {
        return true
    },
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

    // WebSocket endpoint
    r.GET("/ws", func(c *gin.Context) {
        // Upgrade HTTP connection to WebSocket
        ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
        if err != nil {
            log.Printf("Failed to upgrade connection: %v", err)
            return
        }
        defer ws.Close()

        // Simple echo for testing
        for {
            // Read message
            messageType, message, err := ws.ReadMessage()
            if err != nil {
                log.Printf("Error reading message: %v", err)
                break
            }

            // Log received message
            log.Printf("Received: %s", message)

            // Echo it back
            if err := ws.WriteMessage(messageType, message); err != nil {
                log.Printf("Error writing message: %v", err)
                break
            }
        }
    })

    // Start server
    log.Println("Starting server on :8080")
    if err := r.Run(":4703"); err != nil {
        log.Fatal("Server failed to start:", err)
    }
}
