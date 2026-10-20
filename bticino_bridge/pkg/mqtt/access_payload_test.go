package mqtt

import (
	"encoding/json"
	"testing"

	"bticino_bridge/pkg/access"
)

func TestAccessEventPayload(t *testing.T) {
	ev := access.AccessEvent{
		Type: access.TypeTelefonillo, Confidence: access.ConfAlta,
		Where: "20", HadCall: true, RingSource: "doorbell", DurationS: 2.5,
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(accessEventPayload(ev)), &m); err != nil {
		t.Fatalf("payload no es JSON válido: %v", err)
	}
	// HA exige el campo event_type dentro del payload del state_topic.
	if m["event_type"] != "telefonillo" {
		t.Errorf("event_type = %v, quiero telefonillo", m["event_type"])
	}
	if m["had_call"] != true {
		t.Errorf("had_call = %v, quiero true", m["had_call"])
	}
	if m["where"] != "20" {
		t.Errorf("where = %v, quiero 20", m["where"])
	}
}
