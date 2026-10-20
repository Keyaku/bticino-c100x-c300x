package sip

import (
	"fmt"
	"strings"
)

// BuildBackchannelSDP describe (para ffmpeg como RECEPTOR) el RTP speex que el backchannel de
// go2rtc envía a 127.0.0.1:<port> con el payload type <payloadType>. ffmpeg abre ese puerto y
// reenvía lo que llegue; con el formato declarado en el SDP no necesita sondear el stream.
func BuildBackchannelSDP(port, payloadType int) string {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	b.WriteString("o=- 0 0 IN IP4 127.0.0.1\r\n")
	b.WriteString("s=talk\r\n")
	b.WriteString("c=IN IP4 127.0.0.1\r\n")
	b.WriteString("t=0 0\r\n")
	b.WriteString(fmt.Sprintf("m=audio %d RTP/AVP %d\r\n", port, payloadType))
	b.WriteString(fmt.Sprintf("a=rtpmap:%d speex/8000\r\n", payloadType))
	b.WriteString("a=recvonly\r\n")
	return b.String()
}
