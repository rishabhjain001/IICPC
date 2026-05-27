// Package hub implements the WebSocket fan-out hub for the Leaderboard Service.
// (Requirements 10.1, 10.2, 10.6)
package hub

import (
	"context"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

const (
	// clientSendBuffer is the per-client outbound queue depth. Messages for a
	// client whose queue is full are dropped and counted in DroppedCount.
	clientSendBuffer = 256
)

// Client represents a single connected WebSocket subscriber.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket client connections and broadcasts score updates.
// A single JSON serialisation is performed per update; all registered clients
// receive the same byte slice.
//
// Slow clients whose send channel is full have their message dropped; the drop
// is recorded in the dropped atomic counter.
type Hub struct {
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	clients    map[*Client]bool
	dropped    atomic.Int64
}

// NewHub creates a new Hub ready to run.
func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

// Run starts the Hub event loop. Call this in a dedicated goroutine.
// It returns when ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// Drain and close all clients.
			for c := range h.clients {
				close(c.send)
				delete(h.clients, c)
			}
			return

		case c := <-h.register:
			h.clients[c] = true

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}

		case msg := <-h.broadcast:
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// Client is too slow; drop the message and record it.
					h.dropped.Add(1)
				}
			}
		}
	}
}

// Broadcast sends msg to all connected clients.
// Slow clients (full send channel) have their message dropped.
func (h *Hub) Broadcast(msg []byte) {
	h.broadcast <- msg
}

// DroppedCount returns the total number of messages dropped to slow clients.
func (h *Hub) DroppedCount() int64 {
	return h.dropped.Load()
}

// Register adds a client to the hub. Used by tests and the WebSocket handler.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// NewClient creates a Client attached to the hub.
// The conn may be nil in tests (send channel access does not require a live socket).
func NewClient(h *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, clientSendBuffer),
	}
}

// Send returns the client's send channel (read-only view for consumers).
func (c *Client) Send() <-chan []byte {
	return c.send
}
