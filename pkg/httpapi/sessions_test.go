package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/session"
)

func newSessionsServer() (*httptest.Server, session.Store) {
	store := session.NewMemoryStore(session.MaxMessagesCap)
	srv := httptest.NewServer(SessionsHandler(store))
	return srv, store
}

func TestCreateSession_AutoID(t *testing.T) {
	srv, _ := newSessionsServer()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var s session.Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.ID == "" {
		t.Errorf("empty id")
	}
	if s.Version != 1 {
		t.Errorf("version=%d", s.Version)
	}
}

func TestCreateSession_WithSystemAndID(t *testing.T) {
	srv, _ := newSessionsServer()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json",
		bytes.NewReader([]byte(`{"id":"my-sess","system":"be terse"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s session.Session
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if s.ID != "my-sess" || s.System != "be terse" {
		t.Errorf("bad create: %+v", s)
	}
}

func TestAppendMessages_OptimisticConcurrency(t *testing.T) {
	srv, _ := newSessionsServer()
	defer srv.Close()

	// Create.
	http.Post(srv.URL+"/api/sessions", "application/json", bytes.NewReader([]byte(`{"id":"s1"}`)))

	// Append with correct version.
	body, _ := json.Marshal(appendMessagesBody{
		ExpectedVersion: 1,
		Messages:        []chat.Message{chat.UserText("hi")},
	})
	resp, _ := http.Post(srv.URL+"/api/sessions/s1/messages", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first append status=%d", resp.StatusCode)
	}

	// Append with stale version → 409.
	body, _ = json.Marshal(appendMessagesBody{
		ExpectedVersion: 1,
		Messages:        []chat.Message{chat.UserText("bye")},
	})
	resp, _ = http.Post(srv.URL+"/api/sessions/s1/messages", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

func TestGetSession_Missing404(t *testing.T) {
	srv, _ := newSessionsServer()
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/api/sessions/ghost")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestDeleteSession_204ThenGone(t *testing.T) {
	srv, _ := newSessionsServer()
	defer srv.Close()
	http.Post(srv.URL+"/api/sessions", "application/json", bytes.NewReader([]byte(`{"id":"s1"}`)))

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/s1", nil)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status=%d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/api/sessions/s1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("after delete GET = %d", resp.StatusCode)
	}
}
