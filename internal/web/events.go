package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// event is what the page is told as it happens.
type event struct {
	Type    string `json:"type"` // status, changed or done
	Message string `json:"message,omitempty"`
	Rev     int    `json:"rev,omitempty"`
}

// broker hands events to every page that is listening.
type broker struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newBroker() *broker { return &broker{subs: map[chan event]struct{}{}} }

func (b *broker) subscribe() chan event {
	ch := make(chan event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribe(ch chan event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// publish never waits: a page that cannot keep up misses an event, and the
// next one it gets, or the notebook it asks for, is current.
func (b *broker) publish(e event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// serveEvents streams events to a page as server-sent events.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)
	fmt.Fprint(w, "retry: 2000\n\n")
	if rc.Flush() != nil {
		return
	}
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
		case <-beat.C:
			fmt.Fprint(w, ": still here\n\n")
		}
		if rc.Flush() != nil {
			return
		}
	}
}
