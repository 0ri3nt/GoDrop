// Package webui serves the local web GUI and wires it to discovery and
// transfer. All state lives in this process; the browser is just a
// thin client talking over HTTP + a WebSocket for live updates.
package webui

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gorilla/websocket"

	"godrop/internal/discovery"
	"godrop/internal/transfer"
)

//go:embed static
var staticFiles embed.FS

// Event types pushed to the browser over the WebSocket.
const (
	EventPeers          = "peers"
	EventIncomingOffer  = "incoming_offer"
	EventTransferUpdate = "transfer_update"
)

type wsEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type peerDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

type incomingOfferDTO struct {
	TransferID string `json:"transferId"`
	FileName   string `json:"fileName"`
	FileSize   int64  `json:"fileSize"`
	FromAddr   string `json:"fromAddr"`
}

type transferUpdateDTO struct {
	TransferID string `json:"transferId"`
	Direction  string `json:"direction"` // "send" | "receive"
	Status     string `json:"status"`    // "progress" | "done" | "error"
	Sent       int64  `json:"sent,omitempty"`
	Total      int64  `json:"total,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Server ties together the registry, transfer server, and browser clients.
type Server struct {
	registry   *discovery.Registry
	xferServer *transfer.Server
	cert       tls.Certificate
	downloadTo string

	mu      sync.Mutex
	clients map[*websocket.Conn]bool

	pendingMu sync.Mutex
	pending   map[string]*transfer.IncomingTransfer // transferID -> waiting for accept/reject
}

var upgrader = websocket.Upgrader{
	// Local-only tool: any origin on localhost is fine. Do not reuse this
	// CheckOrigin policy for anything exposed beyond the local machine.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewServer builds the web UI server. downloadTo is the directory
// incoming files are saved into.
func NewServer(registry *discovery.Registry, xferServer *transfer.Server, cert tls.Certificate, downloadTo string) *Server {
	s := &Server{
		registry:   registry,
		xferServer: xferServer,
		cert:       cert,
		downloadTo: downloadTo,
		clients:    make(map[*websocket.Conn]bool),
		pending:    make(map[string]*transfer.IncomingTransfer),
	}
	go s.watchIncoming()
	return s
}

// PublishPeers should be passed as the discovery.Registry onChange
// callback so peer updates are pushed to connected browsers.
func (s *Server) PublishPeers(peers []discovery.Peer) {
	dtos := make([]peerDTO, 0, len(peers))
	for _, p := range peers {
		dtos = append(dtos, peerDTO{ID: p.ID, Name: p.Name, Host: p.Host, Port: p.Port})
	}
	s.broadcast(wsEvent{Type: EventPeers, Data: dtos})
}

func (s *Server) watchIncoming() {
	for it := range s.xferServer.Incoming() {
		transferID := fmt.Sprintf("%p", it) // simple unique-enough ID for this process's lifetime
		s.pendingMu.Lock()
		s.pending[transferID] = it
		s.pendingMu.Unlock()

		s.broadcast(wsEvent{Type: EventIncomingOffer, Data: incomingOfferDTO{
			TransferID: transferID,
			FileName:   it.Offer.Name,
			FileSize:   it.Offer.Size,
			FromAddr:   it.RemoteAddr,
		}})
	}
}

// Routes returns the HTTP handler for the whole web UI.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("webui: embed static: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/respond", s.handleRespond)

	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("webui: ws upgrade: %v", err)
		return
	}
	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()

	// Send current peer snapshot immediately so the UI isn't empty until
	// the next discovery change event.
	s.PublishPeers(s.registry.Snapshot())

	// We don't expect messages from the client on this socket; just
	// block on reads so we notice disconnects and can clean up.
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.clients, conn)
			s.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

func (s *Server) broadcast(evt wsEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("webui: marshal event: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(s.clients, conn)
		}
	}
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	peers := s.registry.Snapshot()
	dtos := make([]peerDTO, 0, len(peers))
	for _, p := range peers {
		dtos = append(dtos, peerDTO{ID: p.ID, Name: p.Name, Host: p.Host, Port: p.Port})
	}
	writeJSON(w, dtos)
}

type sendRequest struct {
	PeerID   string `json:"peerId"`
	FilePath string `json:"filePath"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	peer, ok := s.registry.Get(req.PeerID)
	if !ok {
		http.Error(w, "unknown peer", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(req.FilePath); err != nil {
		http.Error(w, "cannot read file: "+err.Error(), http.StatusBadRequest)
		return
	}

	transferID := fmt.Sprintf("send-%s-%s", peer.ID, filepath.Base(req.FilePath))

	go func() {
		err := transfer.SendFile(peer.Host, peer.Port, req.FilePath, s.cert, func(sent, total int64) {
			s.broadcast(wsEvent{Type: EventTransferUpdate, Data: transferUpdateDTO{
				TransferID: transferID,
				Direction:  "send",
				Status:     "progress",
				Sent:       sent,
				Total:      total,
			}})
		})
		update := transferUpdateDTO{TransferID: transferID, Direction: "send", Status: "done"}
		if err != nil {
			update.Status = "error"
			update.Error = err.Error()
		}
		s.broadcast(wsEvent{Type: EventTransferUpdate, Data: update})
	}()

	writeJSON(w, map[string]string{"transferId": transferID, "status": "started"})
}

type respondRequest struct {
	TransferID string `json:"transferId"`
	Accept     bool   `json:"accept"`
}

func (s *Server) handleRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req respondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.pendingMu.Lock()
	it, ok := s.pending[req.TransferID]
	delete(s.pending, req.TransferID)
	s.pendingMu.Unlock()
	if !ok {
		http.Error(w, "unknown or already-resolved transfer", http.StatusNotFound)
		return
	}

	if !req.Accept {
		it.Reject()
		writeJSON(w, map[string]string{"status": "rejected"})
		return
	}

	go func() {
		err := it.Accept(s.downloadTo)
		update := transferUpdateDTO{TransferID: req.TransferID, Direction: "receive", Status: "done"}
		if err != nil {
			update.Status = "error"
			update.Error = err.Error()
		}
		s.broadcast(wsEvent{Type: EventTransferUpdate, Data: update})
	}()

	writeJSON(w, map[string]string{"status": "accepted"})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("webui: write json: %v", err)
	}
}