package events

import (
	"sync"
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return Event{}
}

func TestPublishFanOut(t *testing.T) {
	b := New()
	ch1, unsub1 := b.Subscribe(4)
	defer unsub1()
	ch2, unsub2 := b.Subscribe(4)
	defer unsub2()

	want := Event{Name: NameDuplicate, Action: ActionUpdated, Resource: map[string]int{"id": 1}}
	b.Publish(want)

	for i, ch := range []<-chan Event{ch1, ch2} {
		got := recv(t, ch)
		if got.Name != want.Name || got.Action != want.Action {
			t.Errorf("subscriber %d got %+v, want %+v", i, got, want)
		}
	}
}

func TestPublishOrderPreserved(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe(10)
	defer unsub()
	for i := 0; i < 10; i++ {
		b.Publish(Event{Name: NameScan, Action: ActionProgress, Resource: i})
	}
	for i := 0; i < 10; i++ {
		if got := recv(t, ch); got.Resource != i {
			t.Fatalf("event %d: got resource %v", i, got.Resource)
		}
	}
}

func TestPublishNonBlockingDropsForSlowSubscriber(t *testing.T) {
	b := New()
	slow, unsubSlow := b.Subscribe(1)
	defer unsubSlow()
	fast, unsubFast := b.Subscribe(100)
	defer unsubFast()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			b.Publish(Event{Name: NameQueue, Action: ActionUpdated, Resource: i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}

	if n := len(slow); n != 1 {
		t.Errorf("slow subscriber buffered %d events, want 1 (rest dropped)", n)
	}
	if n := len(fast); n != 50 {
		t.Errorf("fast subscriber buffered %d events, want 50", n)
	}
	if got := recv(t, slow); got.Resource != 0 {
		t.Errorf("slow subscriber should keep the first event, got %v", got.Resource)
	}
}

func TestUnsubscribe(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe(1)
	if n := b.Subscribers(); n != 1 {
		t.Fatalf("Subscribers() = %d, want 1", n)
	}
	unsub()
	unsub() // idempotent

	if n := b.Subscribers(); n != 0 {
		t.Fatalf("Subscribers() after unsubscribe = %d, want 0", n)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
	// Publishing after unsubscribe must not panic (send on closed channel).
	b.Publish(Event{Name: NameHealth, Action: ActionUpdated})
}

func TestSubscribeDefaultBuffer(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe(0)
	defer unsub()
	if c := cap(ch); c != DefaultBuffer {
		t.Fatalf("cap = %d, want %d", c, DefaultBuffer)
	}
}

func TestNilBus(t *testing.T) {
	var b *Bus
	b.Publish(Event{Name: NameTask}) // must not panic
	ch, unsub := b.Subscribe(1)
	unsub()
	if _, ok := <-ch; ok {
		t.Fatal("nil bus should return a closed channel")
	}
	if b.Subscribers() != 0 {
		t.Fatal("nil bus has no subscribers")
	}
}

// TestConcurrentPublishSubscribe is meant to be run with -race.
func TestConcurrentPublishSubscribe(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Publish(Event{Name: NameCommand, Action: ActionUpdated})
				}
			}
		}()
	}
	for s := 0; s < 8; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ch, unsub := b.Subscribe(2)
				select {
				case <-ch:
				default:
				}
				unsub()
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	if n := b.Subscribers(); n != 0 {
		t.Fatalf("leaked %d subscribers", n)
	}
}
