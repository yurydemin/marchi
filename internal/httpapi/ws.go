package httpapi

import (
	"encoding/json"
	"sync"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

// wsEvent is the message envelope broadcast over /ws (FR-API-03: "Формат
// сообщений — JSON с полями type, job_id, progress_percent, message").
// The fields beyond those four are additive detail for sync progress
// specifically (FR-SE-07: "текущий UID, всего писем, скорость, ошибки")
// — harmless extra fields for any client only reading the base envelope.
type wsEvent struct {
	Type            string  `json:"type"` // "sync" | "reindex"
	JobID           string  `json:"job_id"`
	ProgressPercent float64 `json:"progress_percent"`
	Message         string  `json:"message"`
	Done            bool    `json:"done,omitempty"`

	AccountEmail string `json:"account_email,omitempty"`
	FolderName   string `json:"folder_name,omitempty"`
	CurrentUID   uint32 `json:"current_uid,omitempty"`
	Total        int    `json:"total,omitempty"`
	Processed    int    `json:"processed,omitempty"`
	Archived     int    `json:"archived,omitempty"`
	Indexed      int    `json:"indexed,omitempty"`
	Errors       int    `json:"errors,omitempty"`
}

// wsHub fans a wsEvent out to every currently-connected /ws client.
// In-memory and process-local like the session store (step 2) — a
// reconnecting client just misses whatever happened while it was
// disconnected, which is fine for a progress feed (the REST endpoints
// underneath, e.g. GET /api/v1/accounts/{id}/sync-status, are still the
// durable record).
type wsHub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	send chan []byte
}

func newWSHub() *wsHub {
	return &wsHub{clients: make(map[*wsClient]struct{})}
}

func (h *wsHub) register() *wsClient {
	c := &wsClient{send: make(chan []byte, 32)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *wsHub) unregister(c *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// broadcast sends ev to every connected client. A client whose send
// buffer is full (32 messages behind) has this update dropped rather
// than blocking every other client's delivery — a progress feed cares
// about "roughly current", not "every single update, however slow the
// reader."
func (h *wsHub) broadcast(ev wsEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
		}
	}
}

// registerWS wires GET /ws (FR-API-03). It sits behind the same lock gate
// as every other route (only /unlock is exempt) — the browser's own
// WebSocket handshake is a normal HTTP request first, so the existing
// session cookie is sent and checked exactly like any other request.
func registerWS(app *fiber.App, hub *wsHub) {
	app.Get("/ws", websocket.New(func(conn *websocket.Conn) {
		client := hub.register()
		defer hub.unregister(client)

		go func() {
			for msg := range client.send {
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
		}()

		// Nothing meaningful is ever sent by the client — this is a
		// server-push progress feed — but reading is still how a closed
		// connection gets noticed, so the defer above actually runs.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
}
