package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/iicpc/dbhp/leaderboard-service/internal/hub"
)

// startHub runs the hub event loop in a goroutine and returns a cancel func.
func startHub(h *hub.Hub) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	return cancel
}

// waitForMessage reads one message from the client's send channel with a
// short timeout, returning the message or nil on timeout.
func waitForMessage(c *hub.Client, timeout time.Duration) []byte {
	select {
	case msg, ok := <-c.Send():
		if !ok {
			return nil
		}
		return msg
	case <-time.After(timeout):
		return nil
	}
}

func TestBroadcast_ReachesRegisteredClients(t *testing.T) {
	h := hub.NewHub()
	cancel := startHub(h)
	defer cancel()

	c1 := hub.NewClient(h, nil)
	c2 := hub.NewClient(h, nil)
	h.Register(c1)
	h.Register(c2)

	// Give the hub loop a moment to process the register commands.
	time.Sleep(20 * time.Millisecond)

	msg := []byte(`{"type":"SCORE_UPDATE"}`)
	h.Broadcast(msg)

	got1 := waitForMessage(c1, 200*time.Millisecond)
	got2 := waitForMessage(c2, 200*time.Millisecond)

	if string(got1) != string(msg) {
		t.Errorf("client1 got %q, want %q", got1, msg)
	}
	if string(got2) != string(msg) {
		t.Errorf("client2 got %q, want %q", got2, msg)
	}
}

func TestBroadcast_SlowClientDropsMessage(t *testing.T) {
	h := hub.NewHub()
	cancel := startHub(h)
	defer cancel()

	// Create a client but deliberately never read from its channel so it fills up.
	slowClient := hub.NewClient(h, nil)
	h.Register(slowClient)
	time.Sleep(20 * time.Millisecond)

	// Flood with enough messages to fill the send buffer (256 slots).
	const flood = 300
	msg := []byte(`{"x":1}`)
	for i := 0; i < flood; i++ {
		h.Broadcast(msg)
	}

	// Allow the hub event loop to process all broadcasts.
	time.Sleep(100 * time.Millisecond)

	dropped := h.DroppedCount()
	if dropped == 0 {
		t.Errorf("expected DroppedCount > 0 for slow client, got 0")
	}
}

func TestBroadcast_UnregisteredClientNoLongerReceives(t *testing.T) {
	h := hub.NewHub()
	cancel := startHub(h)
	defer cancel()

	c := hub.NewClient(h, nil)
	h.Register(c)
	time.Sleep(20 * time.Millisecond)

	// Unregister before sending.
	h.Unregister(c)
	time.Sleep(20 * time.Millisecond)

	msg := []byte(`{"type":"SCORE_UPDATE"}`)
	h.Broadcast(msg)

	got := waitForMessage(c, 100*time.Millisecond)
	if got != nil {
		t.Errorf("unregistered client received message %q, expected none", got)
	}
}

func TestDroppedCount_InitiallyZero(t *testing.T) {
	h := hub.NewHub()
	if got := h.DroppedCount(); got != 0 {
		t.Errorf("initial DroppedCount = %d, want 0", got)
	}
}
