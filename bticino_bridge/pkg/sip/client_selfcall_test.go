package sip

import (
	"testing"
	"time"
)

func TestShouldAutoAnswerIgnoresOwnCall(t *testing.T) {
	c := &BTicinoSIPClient{originatedCalls: map[string]time.Time{}, nativeCalls: map[string]time.Time{}}
	c.originatedCalls["mi-call-id@127.0.0.1"] = time.Now()
	c.nativeCalls["mi-call-id@127.0.0.1"] = time.Now() // InviteWithSDP (ojo)
	if c.shouldAutoAnswer("mi-call-id@127.0.0.1") {
		t.Fatal("no debe auto-contestar un INVITE con Call-ID propio (loopback)")
	}
	if !c.shouldAutoAnswer("externo@otro") {
		t.Fatal("debe auto-contestar un INVITE externo")
	}
}
