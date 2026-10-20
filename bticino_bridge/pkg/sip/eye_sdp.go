package sip

import "fmt"

// BuildEyeOfferSDP construye el SDP del "ojo" (autoaccensione): vídeo recvonly +
// audio sendrecv + atributo de sesión DEVADDR (la placa a encender). El vídeo NO
// se consume in-band (lo da el tap pasivo lo:5007); videoPort solo existe para que
// bt_vct monte la sesión de placa. srtpKey es NUESTRA clave (cifra lo que enviamos).
// Formato igual que BuildAudioOfferSDP: crypto tag 2, AES_CM_128_HMAC_SHA1_80, speex PT 98.
func BuildEyeOfferSDP(localIP string, audioPort, videoPort int, srtpKey, devaddr string) string {
	return fmt.Sprintf(
		"v=0\r\n"+
			"o=bticino-bridge 0 0 IN IP4 %s\r\n"+
			"s=Eye\r\n"+
			"c=IN IP4 %s\r\n"+
			"t=0 0\r\n"+
			"a=DEVADDR:%s\r\n"+
			"m=video %d RTP/SAVP 96\r\n"+
			"a=rtpmap:96 H264/90000\r\n"+
			"a=recvonly\r\n"+
			"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:%s\r\n"+
			"m=audio %d RTP/SAVP 98\r\n"+
			"a=rtpmap:98 speex/8000\r\n"+
			"a=sendrecv\r\n"+
			"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:%s\r\n",
		localIP, localIP, devaddr, videoPort, srtpKey, audioPort, srtpKey,
	)
}
