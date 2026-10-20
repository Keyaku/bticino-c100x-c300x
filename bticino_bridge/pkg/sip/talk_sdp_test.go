package sip

import (
	"strings"
	"testing"
)

func TestBuildBackchannelSDP(t *testing.T) {
	sdp := BuildBackchannelSDP(40004, 97)
	for _, want := range []string{
		"v=0\r\n",
		"c=IN IP4 127.0.0.1\r\n",
		"m=audio 40004 RTP/AVP 97\r\n",
		"a=rtpmap:97 speex/8000\r\n",
	} {
		if !strings.Contains(sdp, want) {
			t.Errorf("SDP backchannel no contiene %q\n---\n%s", want, sdp)
		}
	}
}
