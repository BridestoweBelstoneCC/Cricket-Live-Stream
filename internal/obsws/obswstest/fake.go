// Package obswstest is a fake OBS WebSocket server for tests: it does the real v5
// handshake (with the password challenge), pushes an unrelated event before every
// response the way OBS does, and answers requests from a handler.
package obswstest

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	"github.com/gorilla/websocket"

	"cricketstream/internal/obsws"
)

// Handler answers one request: ok, and the responseData.
type Handler func(requestType string, data map[string]any) (bool, map[string]any)

// Server is a running fake OBS.
type Server struct {
	srv      *httptest.Server
	Password string
	mu       sync.Mutex
	handler  Handler
	// Requests is every request received, in order.
	Requests []Recorded
	Conns    int
	open     []*websocket.Conn
}

// Recorded is one request the fake received.
type Recorded struct {
	Type string
	Data map[string]any
}

// New starts a fake OBS. An empty password means no authentication.
func New(password string, h Handler) *Server {
	s := &Server{Password: password, handler: h}
	up := websocket.Upgrader{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		s.mu.Lock()
		s.Conns++
		s.open = append(s.open, ws)
		s.mu.Unlock()
		hello := map[string]any{"obsWebSocketVersion": "5.5.0", "rpcVersion": 1}
		if s.Password != "" {
			hello["authentication"] = map[string]string{"challenge": "chal", "salt": "salt"}
		}
		ws.WriteJSON(map[string]any{"op": 0, "d": hello})
		var ident struct {
			Op int `json:"op"`
			D  struct {
				Authentication string `json:"authentication"`
			} `json:"d"`
		}
		if ws.ReadJSON(&ident) != nil || ident.Op != 1 {
			return
		}
		if s.Password != "" && ident.D.Authentication != obsws.AuthResponse(s.Password, "salt", "chal") {
			ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4009, "Authentication failed."))
			return
		}
		ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"negotiatedRpcVersion": 1}})
		for {
			var m struct {
				Op int `json:"op"`
				D  struct {
					RequestType string         `json:"requestType"`
					RequestID   string         `json:"requestId"`
					RequestData map[string]any `json:"requestData"`
				} `json:"d"`
			}
			if ws.ReadJSON(&m) != nil {
				return
			}
			s.mu.Lock()
			s.Requests = append(s.Requests, Recorded{m.D.RequestType, m.D.RequestData})
			h := s.handler
			s.mu.Unlock()
			ok, data := true, map[string]any(nil)
			if h != nil {
				ok, data = h(m.D.RequestType, m.D.RequestData)
			}
			// OBS pushes events down the same socket; clients must skip them.
			ws.WriteJSON(map[string]any{"op": 5, "d": map[string]any{"eventType": "CurrentProgramSceneChanged"}})
			status := map[string]any{"result": ok, "code": 100}
			if !ok {
				status["code"] = 600
				status["comment"] = "refused by fake"
			}
			resp := map[string]any{"requestType": m.D.RequestType, "requestId": m.D.RequestID, "requestStatus": status}
			if data != nil {
				resp["responseData"] = data
			}
			b, _ := json.Marshal(map[string]any{"op": 7, "d": resp})
			ws.WriteMessage(websocket.TextMessage, b)
		}
	}))
	return s
}

// Target points a client at the fake.
func (s *Server) Target() obsws.Target {
	host, port, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return obsws.Target{Host: host, Port: p, Password: s.Password}
}

// SetHandler swaps the request handler.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Types lists the request types received so far.
func (s *Server) Types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.Requests {
		out = append(out, r.Type)
	}
	return out
}

// Close stops the fake (every connection drops).
func (s *Server) Close() {
	s.mu.Lock()
	for _, ws := range s.open { // hijacked: httptest won't close these itself
		ws.Close()
	}
	s.open = nil
	s.mu.Unlock()
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// DropConnections closes every open connection but keeps accepting new ones, the way a
// client sees OBS being restarted.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ws := range s.open {
		ws.Close()
	}
	s.open = nil
}
