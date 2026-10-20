package sip

import (
	"fmt"
	"strings"
)

// AnswererDialog captura la transacción de un INVITE recibido + el to-tag propio del contestador.
type AnswererDialog struct {
	CallID      string
	FromHdr     string   // el From original del INVITE (incluye su tag)
	ToHdr       string   // el To original (sin nuestro tag)
	CSeq        string   // p.ej. "20 INVITE"
	Via         []string // TODAS las Via, en orden
	RecordRoute []string
	ToTag       string // tag propio que añadimos al To
}

// BuildAnswererRegister construye el REGISTER TCP del AOR del contestador contra el Flexisip local.
func BuildAnswererRegister(user, domain, localHost string, localPort, expires int, branch, callID, fromTag string, cseq int) string {
	aor := fmt.Sprintf("sip:%s@%s", user, domain)
	lines := []string{
		fmt.Sprintf("REGISTER sip:%s SIP/2.0", domain),
		fmt.Sprintf("Via: SIP/2.0/TCP %s:%d;alias;branch=%s;rport", localHost, localPort, branch),
		"Max-Forwards: 70",
		fmt.Sprintf("From: <%s>;tag=%s", aor, fromTag),
		fmt.Sprintf("To: <%s>", aor),
		fmt.Sprintf("Call-ID: %s", callID),
		fmt.Sprintf("CSeq: %d REGISTER", cseq),
		fmt.Sprintf("Contact: <sip:%s@%s:%d;transport=tcp>;+sip.instance=\"<urn:uuid:%s>\"", user, localHost, localPort, callID),
		fmt.Sprintf("Expires: %d", expires),
		"User-Agent: bticino-bridge-answerer",
		"Content-Length: 0",
		"", "",
	}
	return strings.Join(lines, "\r\n")
}

// BuildAnswererResponse construye una respuesta a un INVITE (100/180/200/486/487). Copia todas
// las Via y Record-Route del INVITE, añade nuestro to-tag, Contact (si contact != ""), y SDP (si
// sdp != "").
func BuildAnswererResponse(status, reason string, d AnswererDialog, sdp, contact string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %s %s\r\n", status, reason)
	for _, v := range d.Via {
		fmt.Fprintf(&b, "Via: %s\r\n", v)
	}
	for _, rr := range d.RecordRoute {
		fmt.Fprintf(&b, "Record-Route: %s\r\n", rr)
	}
	fmt.Fprintf(&b, "From: %s\r\n", d.FromHdr)
	fmt.Fprintf(&b, "To: %s;tag=%s\r\n", d.ToHdr, d.ToTag)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", d.CallID)
	fmt.Fprintf(&b, "CSeq: %s\r\n", d.CSeq)
	if contact != "" {
		fmt.Fprintf(&b, "Contact: %s\r\n", contact)
	}
	if sdp != "" {
		b.WriteString("Content-Type: application/sdp\r\n")
		fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(sdp))
		b.WriteString(sdp)
	} else {
		b.WriteString("Content-Length: 0\r\n\r\n")
	}
	return b.String()
}

// BuildAnswererBYE construye el BYE del contestador hacia la placa (in-dialog).
func BuildAnswererBYE(d AnswererDialog, user, domain, localHost string, localPort, cseq int, branch string) string {
	target := d.FromHdr // el remitente del INVITE es el destino del BYE
	if i := strings.Index(target, ";"); i >= 0 {
		target = target[:i]
	}
	target = strings.Trim(target, "<>")
	var b strings.Builder
	fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", target)
	fmt.Fprintf(&b, "Via: SIP/2.0/TCP %s:%d;alias;branch=%s;rport\r\n", localHost, localPort, branch)
	for _, rr := range d.RecordRoute { // Route inverso si hubo Record-Route
		fmt.Fprintf(&b, "Route: %s\r\n", rr)
	}
	b.WriteString("Max-Forwards: 70\r\n")
	// From/To se invierten respecto al INVITE entrante: nuestro tag va en From.
	fmt.Fprintf(&b, "From: %s;tag=%s\r\n", d.ToHdr, d.ToTag)
	fmt.Fprintf(&b, "To: %s\r\n", d.FromHdr)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", d.CallID)
	fmt.Fprintf(&b, "CSeq: %d BYE\r\n", cseq)
	b.WriteString("Content-Length: 0\r\n\r\n")
	return b.String()
}
