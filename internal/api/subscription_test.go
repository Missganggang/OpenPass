package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestSubscriptionLifecycleNamesAndOwnsNodes(t *testing.T) {
	s, rt := selfFixture(t)
	feed := "socks5://user:pass@feed.example:1080#Feed Node\n"
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(feed))
	}))
	defer feedServer.Close()

	w := subscriptionRequest(s, http.MethodPost, "/api/subscriptions", `{"url":"`+feedServer.URL+`","name":"Hello","remark":"海外线路"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create=%d %s", w.Code, w.Body)
	}
	var created struct {
		Imported     int `json:"imported"`
		Subscription struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Remark    string `json:"remark"`
			NodeCount int    `json:"node_count"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Imported != 1 || created.Subscription.Name != "Hello" || created.Subscription.Remark != "海外线路" || created.Subscription.NodeCount != 1 {
		t.Fatalf("unexpected create response: %s", w.Body)
	}
	if len(s.Store.Subscriptions()) != 1 || len(s.Store.Nodes()) != 3 {
		t.Fatalf("subscription not persisted: subs=%d nodes=%d", len(s.Store.Subscriptions()), len(s.Store.Nodes()))
	}

	w = subscriptionRequest(s, http.MethodGet, "/api/subscriptions", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"Hello"`) || !strings.Contains(w.Body.String(), `"node_count":1`) {
		t.Fatalf("list=%d %s", w.Code, w.Body)
	}

	// The imported node remains a normal node for runtime purposes, but its
	// ownership is shown in the public node projection.
	if !strings.Contains(w.Body.String(), `"subscription_id":"`+created.Subscription.ID+`"`) {
		t.Fatalf("node ownership missing: %s", w.Body)
	}

	// A bound feed node prevents deleting the subscription and leaves runtime
	// untouched. Use the subscription's sole imported node ID.
	var imported model.Node
	for _, n := range s.Store.Nodes() {
		if n.Subscription == created.Subscription.ID {
			imported = n
		}
	}
	if imported.ID == "" {
		t.Fatal("missing imported node")
	}
	if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.Mode, d.NodeID = "proxy", imported.ID }); err != nil {
		t.Fatal(err)
	}
	w = subscriptionRequest(s, http.MethodDelete, "/api/subscriptions/"+created.Subscription.ID, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "请先解绑") {
		t.Fatalf("bound delete=%d %s", w.Code, w.Body)
	}
	if rt.applied.Nodes != nil {
		t.Fatalf("rejected delete applied runtime: %#v", rt.applied)
	}

	if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.Mode, d.NodeID = "direct", "" }); err != nil {
		t.Fatal(err)
	}
	w = subscriptionRequest(s, http.MethodDelete, "/api/subscriptions/"+created.Subscription.ID, "")
	if w.Code != http.StatusOK || len(s.Store.Subscriptions()) != 0 {
		t.Fatalf("delete=%d %s", w.Code, w.Body)
	}
	if len(s.Store.Nodes()) != 2 {
		t.Fatalf("owned nodes not removed: %d", len(s.Store.Nodes()))
	}
}

func subscriptionRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
