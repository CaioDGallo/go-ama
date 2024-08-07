package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/CaioDGallo/go-ama/internal/store/pgstore"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type apiHandler struct {
	q           *pgstore.Queries
	r           *chi.Mux
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	mu          *sync.Mutex
	upgrader    websocket.Upgrader
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.r.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	a := apiHandler{
		q:           q,
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mu:          &sync.Mutex{},
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/subscribe/{room_id}", a.handleSubscribe)

	r.Route("/api", func(r chi.Router) {
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", a.handleCreateRoom)
			r.Get("/{room_id}", a.handleGetRoom)

			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Post("/", a.handleCreateRoomMessage)
				r.Get("/", a.handleGetRoomMessages)

				r.Route("/{message_id}", func(r chi.Router) {
					r.Get("/", a.handleGetRoomMessage)
					r.Patch("/react", a.handleReactToMessage)
					r.Delete("/react", a.handleRemoveReactionFromMessage)
					r.Patch("/answer", a.handleMarkMessageMessageAsAnswered)
				})
			})
		})
	})

	a.r = r

	return a
}

const (
	MessageKindMessageCreated = "message_created"
)

type MessageMessageCreated struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

func (h apiHandler) notifyClients(msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		err := conn.WriteJSON(msg)
		if err != nil {
			slog.Error("failed to write message to client", "error", err)
			cancel()
		}
	}
}

func (h apiHandler) extractUUID(r *http.Request, p string) (uuid.UUID, error) {
	rawUUID := chi.URLParam(r, p)
	UUID, err := uuid.Parse(rawUUID)
	if err != nil {
		slog.Error("failed to parse UUID", "error", err)
		return uuid.Nil, err
	}

	return UUID, nil
}

func (h apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type _body struct {
		Theme string `json:"theme"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	roomID, err := h.q.InsertRoom(r.Context(), body.Theme)
	if err != nil {
		slog.Error("failed to create room", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, _ := json.Marshal(response{ID: roomID.String()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	roomID, err := h.extractUUID(r, "room_id")
	if err != nil {
		http.Error(w, "invalid room ID", http.StatusBadRequest)
		return
	}

	room, err := h.q.GetRoom(r.Context(), roomID)
	if err != nil {
		slog.Error("failed to get room", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Theme string    `json:"theme"`
		ID    uuid.UUID `json:"id"`
	}

	data, _ := json.Marshal(response{ID: room.ID, Theme: room.Theme})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleCreateRoomMessage(w http.ResponseWriter, r *http.Request) {
	roomID, err := h.extractUUID(r, "room_id")
	if err != nil {
		http.Error(w, "invalid room ID", http.StatusBadRequest)
		return
	}

	type _body struct {
		Message string `json:"message"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	messageID, err := h.q.InsertMessage(r.Context(), pgstore.InsertMessageParams{RoomID: roomID, Message: body.Message})
	if err != nil {
		slog.Error("failed to create message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, _ := json.Marshal(response{ID: messageID.String()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)

	go h.notifyClients(Message{
		Kind:   MessageKindMessageCreated,
		Value:  MessageMessageCreated{ID: messageID.String(), Message: body.Message},
		RoomID: roomID.String(),
	})
}

func (h apiHandler) handleGetRoomMessages(w http.ResponseWriter, r *http.Request) {
	roomID, err := h.extractUUID(r, "room_id")
	if err != nil {
		http.Error(w, "invalid room ID", http.StatusBadRequest)
		return
	}

	messages, err := h.q.GetRoomMessages(r.Context(), roomID)
	if err != nil {
		slog.Error("failed to get room messages", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Messages []pgstore.Message `json:"messages"`
	}

	data, _ := json.Marshal(response{Messages: messages})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleGetRoomMessage(w http.ResponseWriter, r *http.Request) {
	messageID, err := h.extractUUID(r, "message_id")
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	message, err := h.q.GetMessage(r.Context(), messageID)
	if err != nil {
		slog.Error("failed to get message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Message pgstore.Message `json:"message"`
	}

	data, _ := json.Marshal(response{Message: message})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleReactToMessage(w http.ResponseWriter, r *http.Request) {
	messageID, err := h.extractUUID(r, "message_id")
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	reactionCount, err := h.q.ReactToMessage(r.Context(), messageID)
	if err != nil {
		slog.Error("failed to react to message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ReactionCount int64 `json:"reaction_count"`
	}

	data, _ := json.Marshal(response{ReactionCount: reactionCount})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleRemoveReactionFromMessage(w http.ResponseWriter, r *http.Request) {
	messageID, err := h.extractUUID(r, "message_id")
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	reactionCount, err := h.q.RemoveReactionFromMessage(r.Context(), messageID)
	if err != nil {
		slog.Error("failed to remove reaction from message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ReactionCount int64 `json:"reaction_count"`
	}

	data, _ := json.Marshal(response{ReactionCount: reactionCount})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleMarkMessageMessageAsAnswered(w http.ResponseWriter, r *http.Request) {
	messageID, err := h.extractUUID(r, "message_id")
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	_, err = h.q.MarkMessageAsAnswered(r.Context(), messageID)
	if err != nil {
		slog.Error("failed to mark message as answered", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, _ := json.Marshal(response{ID: messageID.String()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	roomID, err := h.extractUUID(r, "room_id")
	rawRoomID := roomID.String()

	if err != nil {
		http.Error(w, "invalid room ID", http.StatusBadRequest)
		return
	}

	c, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("failed to upgrade connection", "error", err)
		http.Error(w, "failed to upgrade to a websocket connection", http.StatusBadRequest)
		return
	}

	defer c.Close()

	ctx, cancel := context.WithCancel(r.Context())

	h.mu.Lock()
	if _, ok := h.subscribers[rawRoomID]; !ok {
		h.subscribers[rawRoomID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	slog.Info("new subscriber", "room_id", rawRoomID, "client_id", r.RemoteAddr)
	h.subscribers[rawRoomID][c] = cancel
	h.mu.Unlock()

	<-ctx.Done()

	h.mu.Lock()
	delete(h.subscribers[rawRoomID], c)
	h.mu.Unlock()
}
