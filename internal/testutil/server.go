// ABOUTME: Fake OX middleware HTTP server for tests, built on httptest.Server.
// ABOUTME: Lets tests register canned JSON responses per action and inspect received requests.
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Server is a fake OX middleware server.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	responses map[string]Response
	funcs     map[string]func(*http.Request) Response
	requests  []*http.Request
}

// Response is a canned response for one module+action pair.
type Response struct {
	Status  int
	Body    any // marshaled to JSON; if already []byte, sent as-is
	Cookies []*http.Cookie
}

// NewServer starts a fake OX middleware server. Register responses with Handle before
// issuing requests; unregistered module/action pairs return 404.
func NewServer() *Server {
	s := &Server{responses: make(map[string]Response), funcs: make(map[string]func(*http.Request) Response)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Handle registers the response for a given module (URL path, e.g. "/ajax/login") and
// action query parameter.
func (s *Server) Handle(module, action string, resp Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[module+"?action="+action] = resp
}

// HandleFunc registers a function that builds the response for a given module and action
// from the request, for example from its folder parameter. It takes precedence over Handle.
func (s *Server) HandleFunc(module, action string, fn func(*http.Request) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.funcs[module+"?action="+action] = fn
}

// Requests returns all requests received so far, in order.
func (s *Server) Requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*http.Request, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	r.ParseForm()

	stored := r.Clone(r.Context())
	stored.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	s.mu.Lock()
	s.requests = append(s.requests, stored)
	key := r.URL.Path + "?action=" + r.URL.Query().Get("action")
	resp, ok := s.responses[key]
	fn := s.funcs[key]
	s.mu.Unlock()
	if fn != nil {
		resp, ok = fn(r), true
	}

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	for _, c := range resp.Cookies {
		http.SetCookie(w, c)
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	switch body := resp.Body.(type) {
	case []byte:
		w.Write(body)
	case nil:
		// empty body
	default:
		json.NewEncoder(w).Encode(body)
	}
}
