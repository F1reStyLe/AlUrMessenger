package websocket

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var acceptOptions = &websocket.AcceptOptions{
	OriginPatterns:  []string{"*"}, // В продакшене замените на конкретные домены
	CompressionMode: websocket.CompressionDisabled,
}

type Client struct {
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

var hub = &Hub{
	broadcast:  make(chan []byte),
	register:   make(chan *Client),
	unregister: make(chan *Client),
	clients:    make(map[*Client]bool),
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("Client connected. Total: %d", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send) // Закрываем канал, что завершит for range в writePump
			}
			h.mu.Unlock()
			log.Printf("Client disconnected. Total: %d", len(h.clients))

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
					// Сообщение отправлено в канал
				default:
					// Канал полон, закрываем соединение
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		hub.unregister <- c
		c.conn.Close(websocket.StatusInternalError, "readPump exited")
	}()

	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute*10)

		messageType, message, err := c.conn.Read(ctx)
		cancel()

		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				log.Printf("Client disconnected normally")
			} else {
				log.Printf("Read error: %v", err)
			}
			break
		}

		if messageType == websocket.MessageText {
			log.Printf("Received message: %s", string(message))
			hub.broadcast <- message
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close(websocket.StatusInternalError, "writePump exited")

	// Используем for range для итерации по каналу
	for message := range c.send {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		err := c.conn.Write(ctx, websocket.MessageText, message)
		cancel()

		if err != nil {
			log.Printf("Write error: %v", err)
			return
		}
	}

	// Канал закрыт, отправляем сообщение о закрытии
	c.conn.Write(context.Background(), websocket.MessageText, []byte{})
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Используем websocket.Accept вместо Upgrader
	conn, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		log.Printf("WebSocket accept error: %v", err)
		return
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}

	hub.register <- client

	go client.writePump()
	go client.readPump()
}

func BroadcastMessage(message []byte) {
	hub.broadcast <- message
}

// New функция для инициализации WebSocket сервера
func New() *Hub {
	return hub
}

// Start запускает WebSocket хаб
func (h *Hub) Start() {
	go h.Run()
	log.Println("WebSocket hub started")
}

// GetClientCount возвращает количество подключенных клиентов
func (h *Hub) GetClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// CloseConnection закрывает соединение с клиентом
func (c *Client) CloseConnection(reason string) {
	c.conn.Close(websocket.StatusNormalClosure, reason)
}

// SendMessage отправляет сообщение конкретному клиенту
func (c *Client) SendMessage(message []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	return c.conn.Write(ctx, websocket.MessageText, message)
}

// GracefulShutdown плавное завершение работы WebSocket сервера
func (h *Hub) GracefulShutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for client := range h.clients {
		client.CloseConnection("Server shutting down")
		close(client.send)
		delete(h.clients, client)
	}

	log.Println("WebSocket hub gracefully shut down")
}

// Stats возвращает статистику по подключениям
func (h *Hub) Stats() map[string]interface{} {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return map[string]interface{}{
		"connected_clients": len(h.clients),
		"timestamp":         time.Now().Unix(),
	}
}
