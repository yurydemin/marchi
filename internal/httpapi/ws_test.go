package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWSHub_BroadcastDeliversToRegisteredClient(t *testing.T) {
	hub := newWSHub()
	client := hub.register()

	hub.broadcast(wsEvent{Type: "sync", JobID: "job-1", Message: "hello"})

	select {
	case msg := <-client.send:
		var ev wsEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			t.Fatalf("unmarshaling broadcast message: %v", err)
		}
		if ev.JobID != "job-1" || ev.Message != "hello" {
			t.Errorf("got %+v, want JobID=job-1 Message=hello", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("client never received the broadcast message")
	}
}

func TestWSHub_BroadcastReachesEveryClient(t *testing.T) {
	hub := newWSHub()
	a := hub.register()
	b := hub.register()

	hub.broadcast(wsEvent{Type: "sync", JobID: "job-1"})

	for name, c := range map[string]*wsClient{"a": a, "b": b} {
		select {
		case <-c.send:
		case <-time.After(time.Second):
			t.Errorf("client %s never received the broadcast message", name)
		}
	}
}

func TestWSHub_UnregisterStopsFurtherDelivery(t *testing.T) {
	hub := newWSHub()
	client := hub.register()
	hub.unregister(client)

	// Broadcasting after unregister must not panic (sending on the closed
	// channel would) and must not deliver anything.
	hub.broadcast(wsEvent{Type: "sync", JobID: "job-1"})

	if _, ok := <-client.send; ok {
		t.Error("expected client.send to be closed after unregister")
	}
}

func TestWSHub_UnregisterIsIdempotent(t *testing.T) {
	hub := newWSHub()
	client := hub.register()
	hub.unregister(client)
	hub.unregister(client) // must not panic (double-close)
}

func TestWSHub_SlowClientDoesNotBlockBroadcast(t *testing.T) {
	hub := newWSHub()
	slow := hub.register()
	fast := hub.register()

	// Fill the slow client's buffer without draining it, while keeping the
	// fast client drained so its buffer never fills.
	for i := 0; i < 64; i++ {
		hub.broadcast(wsEvent{Type: "sync", JobID: "flood"})
		select {
		case <-fast.send:
		default:
		}
	}

	// This must return promptly rather than blocking on the full channel.
	done := make(chan struct{})
	go func() {
		hub.broadcast(wsEvent{Type: "sync", JobID: "final"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on a slow client instead of dropping the message")
	}

	// The fast client (drained as we go) should still see the final message.
	var lastJobID string
	for {
		select {
		case msg := <-fast.send:
			var ev wsEvent
			_ = json.Unmarshal(msg, &ev)
			lastJobID = ev.JobID
		default:
			if lastJobID != "final" {
				t.Errorf("fast client's last message had JobID %q, want final", lastJobID)
			}
			_ = slow
			return
		}
	}
}
