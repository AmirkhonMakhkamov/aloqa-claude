package ws

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHubEvictSessionDisconnectsMatchingClients(t *testing.T) {
	hub := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	clientA := &Client{ID: uuid.New(), UserID: uuid.New(), SessionID: "session-a", ResumeKey: "resume-a", Send: make(chan []byte, 1)}
	clientB := &Client{ID: uuid.New(), UserID: uuid.New(), SessionID: "session-b", ResumeKey: "resume-b", Send: make(chan []byte, 1)}

	hub.Register(clientA)
	hub.Register(clientB)
	waitForHub(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.clients) == 2
	})

	hub.EvictSession("session-a")
	waitForHub(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		_, existsA := hub.clients[clientA.ID]
		_, existsB := hub.clients[clientB.ID]
		return !existsA && existsB
	})

	select {
	case _, ok := <-clientA.Send:
		if ok {
			t.Fatalf("clientA send channel is still open")
		}
	default:
		t.Fatalf("clientA send channel was not closed")
	}
}

func TestHubConcurrentBroadcastAndSubscribe(t *testing.T) {
	hub := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	const numClients = 50
	const numRooms = 5
	clients := make([]*Client, numClients)
	for i := range clients {
		clients[i] = &Client{
			ID:        uuid.New(),
			UserID:    uuid.New(),
			SessionID: "session",
			ResumeKey: "resume",
			Send:      NewClientSendChan(),
		}
		hub.Register(clients[i])
	}
	waitForHub(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.clients) == numClients
	})

	// Subscribe all clients to rooms.
	for i, c := range clients {
		room := roomName(i % numRooms)
		hub.Subscribe(c.ID.String(), room)
	}

	// Concurrent broadcasts + subscribes.
	var wg sync.WaitGroup
	for i := 0; i < numRooms; i++ {
		wg.Add(1)
		go func(room string) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				hub.BroadcastToRoom(room, []byte(`{"type":"ping"}`))
			}
		}(roomName(i))
	}

	// Concurrent subscribes while broadcasting.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := clients[idx%numClients]
			for j := 0; j < numRooms; j++ {
				hub.Subscribe(c.ID.String(), roomName(j))
			}
		}(i)
	}

	wg.Wait()

	// Drain client channels to verify no panics occurred.
	for _, c := range clients {
		for {
			select {
			case _, ok := <-c.Send:
				if !ok {
					goto next
				}
			default:
				goto next
			}
		}
	next:
	}
}

func TestHubSlowClientEviction(t *testing.T) {
	hub := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	slowClient := &Client{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		SessionID: "slow",
		ResumeKey: "slow-resume",
		Send:      make(chan []byte, 1), // tiny buffer to trigger drops
	}
	hub.Register(slowClient)
	waitForHub(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.clients) == 1
	})

	room := "test-room"
	hub.Subscribe(slowClient.ID.String(), room)

	// Fill the buffer then broadcast enough to exceed maxSlowDrops.
	slowClient.Send <- []byte("fill")
	for i := 0; i < maxSlowDrops+2; i++ {
		hub.BroadcastToRoom(room, []byte(`{"type":"ping"}`))
	}

	// Client should be evicted.
	waitForHub(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.clients) == 0
	})

	// Send channel should be closed.
	select {
	case _, ok := <-slowClient.Send:
		// Drain remaining messages.
		if ok {
			for range slowClient.Send {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("slow client send channel was not closed")
	}
}

func roomName(i int) string {
	return "room-" + uuid.NewSHA1(uuid.Nil, []byte{byte(i)}).String()
}

func BenchmarkHubBroadcastToRoom(b *testing.B) {
	hub := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	const numClients = 100
	for i := 0; i < numClients; i++ {
		c := &Client{
			ID:        uuid.New(),
			UserID:    uuid.New(),
			SessionID: "bench",
			Send:      NewClientSendChan(),
		}
		hub.Register(c)
		// Wait for registration.
		time.Sleep(time.Millisecond)
		hub.Subscribe(c.ID.String(), "bench-room")
	}
	time.Sleep(50 * time.Millisecond)

	data := []byte(`{"type":"message","body":"hello"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.BroadcastToRoom("bench-room", data)
	}
}

func waitForHub(t *testing.T, predicate func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if predicate() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for hub state")
		case <-ticker.C:
		}
	}
}
