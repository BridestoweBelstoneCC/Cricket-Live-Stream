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
	"time"

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
	// Meters, when set, is the sound levels sent (every 20ms, as OBS's InputVolumeMeters
	// event) to a connection that subscribed to them: input name -> one [magnitude, peak,
	// input peak] per channel. Asked afresh each time, so a test can change the sound.
	Meters func() map[string][][]float64
	// Subscriptions is the eventSubscriptions each connection identified with (-1: none named).
	Subscriptions []int
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
				Authentication     string `json:"authentication"`
				EventSubscriptions *int   `json:"eventSubscriptions"`
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
		subs := -1
		if ident.D.EventSubscriptions != nil {
			subs = *ident.D.EventSubscriptions
		}
		s.mu.Lock()
		s.Subscriptions = append(s.Subscriptions, subs)
		meters := s.Meters
		s.mu.Unlock()
		if subs >= 0 && subs&(1<<16) != 0 && meters != nil {
			// A listener: levels until it hangs up (it sends no requests).
			for {
				var inputs []map[string]any
				for name, levels := range meters() {
					inputs = append(inputs, map[string]any{"inputName": name, "inputLevelsMul": levels})
				}
				if ws.WriteJSON(map[string]any{"op": 5, "d": map[string]any{"eventType": "InputVolumeMeters",
					"eventIntent": 1 << 16, "eventData": map[string]any{"inputs": inputs}}}) != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
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

// SetMeters sets Meters while the fake is running.
func (s *Server) SetMeters(fn func() map[string][][]float64) {
	s.mu.Lock()
	s.Meters = fn
	s.mu.Unlock()
}

// Subs is a copy of Subscriptions, safe to read while connections are open.
func (s *Server) Subs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.Subscriptions...)
}

// Target points a client at the fake.
func (s *Server) Target() obsws.Target {
	host, port, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return obsws.Target{Host: host, Port: p, Password: s.Password}
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
