package events

import (
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestEventBus_BasicFunctionality(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	bus := NewEventBus(1, logger)
	defer bus.Close()

	// Test basic subscribe and publish. El handler corre en otra goroutine: el estado
	// compartido con el test va protegido por mutex (si no, -race lo marca).
	var mu sync.Mutex
	eventReceived := false
	var receivedData interface{}
	snapshot := func() (bool, interface{}) {
		mu.Lock()
		defer mu.Unlock()
		return eventReceived, receivedData
	}

	subscriptionID := bus.Subscribe("test.event", func(event *Event) {
		mu.Lock()
		defer mu.Unlock()
		eventReceived = true
		receivedData = event.Data
	})

	// Publish an event
	bus.Publish("test.event", "test data")

	// Give some time for async processing
	time.Sleep(100 * time.Millisecond)

	got, data := snapshot()
	if !got {
		t.Error("Event was not received")
	}

	if data != "test data" {
		t.Errorf("Expected 'test data', got %v", data)
	}

	// Test unsubscribe
	if !bus.Unsubscribe(subscriptionID) {
		t.Error("Failed to unsubscribe")
	}

	// Reset and publish again - should not receive
	mu.Lock()
	eventReceived = false
	mu.Unlock()
	bus.Publish("test.event", "test data 2")
	time.Sleep(100 * time.Millisecond)

	if got, _ := snapshot(); got {
		t.Error("Event was received after unsubscribe")
	}
}

func TestEventBus_PatternMatching(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	bus := NewEventBus(1, logger)
	defer bus.Close()

	// Los handlers pueden ejecutarse en goroutines concurrentes: append protegido por mutex.
	var mu sync.Mutex
	receivedEvents := make([]string, 0)

	// Subscribe to pattern
	bus.SubscribePattern("door.*", func(eventName string, event *Event) {
		mu.Lock()
		defer mu.Unlock()
		receivedEvents = append(receivedEvents, eventName)
	})

	// Publish matching events
	bus.Publish("door.opened", nil)
	bus.Publish("door.closed", nil)
	bus.Publish("window.opened", nil) // Should not match

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	receivedEvents = append([]string(nil), receivedEvents...) // copia estable para el resto del test
	mu.Unlock()

	if len(receivedEvents) != 2 {
		t.Errorf("Expected 2 events, got %d", len(receivedEvents))
	}

	// Check that we received the expected events (order may vary due to async processing)
	expectedEvents := map[string]bool{"door.opened": false, "door.closed": false}
	for _, event := range receivedEvents {
		if _, exists := expectedEvents[event]; exists {
			expectedEvents[event] = true
		} else {
			t.Errorf("Unexpected event: %s", event)
		}
	}

	for event, received := range expectedEvents {
		if !received {
			t.Errorf("Expected event %s was not received", event)
		}
	}
}

func TestEventBus_Stats(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	bus := NewEventBus(1, logger)
	defer bus.Close()

	bus.Subscribe("test.event", func(event *Event) {})

	bus.Publish("test.event", nil)
	bus.Publish("test.event", nil)
	bus.Publish("another.event", nil)

	time.Sleep(100 * time.Millisecond)

	stats := bus.GetStats()

	if stats.TotalEvents != 3 {
		t.Errorf("Expected 3 total events, got %d", stats.TotalEvents)
	}

	if stats.ActiveSubscribers != 1 {
		t.Errorf("Expected 1 active subscriber, got %d", stats.ActiveSubscribers)
	}

	if stats.EventCounts["test.event"] != 2 {
		t.Errorf("Expected 2 test.event counts, got %d", stats.EventCounts["test.event"])
	}

	if stats.EventCounts["another.event"] != 1 {
		t.Errorf("Expected 1 another.event counts, got %d", stats.EventCounts["another.event"])
	}
}
