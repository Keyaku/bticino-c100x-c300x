package sip

import (
	"testing"
	"time"
)

func TestExecRunner_ArrancaYMata(t *testing.T) {
	r := NewExecRunner()
	// `sleep 30` como proceso inocuo de larga duración.
	p, err := r.Start("sleep", []string{"30"})
	if err != nil {
		t.Skipf("no hay 'sleep' en el entorno de test: %v", err)
	}
	// Matarlo y confirmar que Wait retorna en un tiempo razonable.
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait no retornó tras Kill")
	}
}

func TestExecRunner_BinarioInexistente(t *testing.T) {
	r := NewExecRunner()
	if _, err := r.Start("/no/existe/ffmpeg-xyz", []string{"-x"}); err == nil {
		t.Fatal("esperaba error al arrancar un binario inexistente")
	}
}
