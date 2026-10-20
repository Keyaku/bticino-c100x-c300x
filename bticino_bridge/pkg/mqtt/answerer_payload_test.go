package mqtt

import (
	"encoding/json"
	"testing"
)

// Los atributos del sensor "Contestador" llevan el tipo de la llamada entrante (call_type),
// tomado del payload de doorcall.incoming.
func TestAnswererIncomingAttrs(t *testing.T) {
	var m map[string]interface{}
	payload := answererIncomingAttrs(map[string]interface{}{"call_id": "c1", "call_type": "placa"})
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("no es JSON válido: %v", err)
	}
	if m["call_type"] != "placa" {
		t.Errorf("call_type=%v, esperaba placa", m["call_type"])
	}
}

// Si el evento no trae call_type (o no es un mapa), el atributo es "desconocido", nunca vacío.
func TestAnswererIncomingAttrs_SinTipo(t *testing.T) {
	for _, data := range []interface{}{nil, map[string]interface{}{"call_id": "c1"}, "basura"} {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(answererIncomingAttrs(data)), &m); err != nil {
			t.Fatalf("no es JSON válido: %v", err)
		}
		if m["call_type"] != "desconocido" {
			t.Errorf("data=%v: call_type=%v, esperaba desconocido", data, m["call_type"])
		}
	}
}
