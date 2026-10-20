package sip

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"math/rand"
	"sync"
	"time"
)

// placeholderH264 es un fotograma IDR negro con la MISMA cabecera que la placa
// (Constrained Baseline 3.0, 688x480 progresivo, medido en pcaps de lo:5007):
//
//	ffmpeg -f lavfi -i color=c=black:s=688x480:r=1 -frames:v 1 -pix_fmt yuv420p \
//	       -c:v libx264 -profile:v baseline -level 3.0 -x264-params keyint=1:repeat-headers=1 -f h264
//
//go:embed placeholder_688x480.h264
var placeholderH264 []byte

const (
	vfillIdle     = 1500 * time.Millisecond // sin vídeo real durante esto => fondo de reposo
	vfillInterval = time.Second             // cadencia del fondo (1 fps)
	vfillTick     = 100 * time.Millisecond
	vfillMTU      = 1200 // payload RTP máximo antes de fragmentar en FU-A
	videoClock    = 90000
)

// videoFill es el modo "RTSP de vídeo nunca mudo" de un RTPRelay.
//
// Por qué: el stream `doorbell` de go2rtc es un exec de ffmpeg que, sin vídeo, muere
// por timeout a los 30 s y go2rtc lo relanza cada ~90 s. Si el ojo (30 s) cae en ese
// hueco, la tarjeta HA no ve nada; si cae dentro, la imagen llega tarde. Con un fondo
// negro a 1 fps en reposo, ffmpeg sigue conectado y el vídeo real pasa al instante.
// Todo sale con UN SSRC y secuencia continua; el vídeo real conserva su marker y la
// separación de timestamps (desplazada para continuar la línea de tiempo).
type videoFill struct {
	pt     byte
	ssrc   uint32
	start  time.Time
	frame  [][]byte // payloads RTP del fondo (SPS, PPS, IDR fragmentado)
	seq    uint16
	mu     sync.Mutex
	srcID  uint32 // SSRC de la fuente real en curso
	inReal bool
	offset uint32 // desplazamiento de timestamps de la fuente real

	lastReal, lastFill time.Time
}

// EnableVideoFill activa el fondo de reposo con el PT de vídeo pt. Llamar antes de Start.
func (r *RTPRelay) EnableVideoFill(pt byte) {
	r.vfill = &videoFill{pt: pt, ssrc: rand.Uint32(), frame: packetizeH264(annexBNALs(placeholderH264), vfillMTU)}
}

func (v *videoFill) wallTS(now time.Time) uint32 {
	return uint32(now.Sub(v.start).Nanoseconds() * videoClock / int64(time.Second))
}

// vfillMain reescribe un paquete de vídeo real.
func (r *RTPRelay) vfillMain(packet []byte) {
	v := r.vfill
	payload := rtpPayload(packet)
	if payload == nil {
		return
	}
	marker := packet[1]&0x80 != 0
	srcTS := binary.BigEndian.Uint32(packet[4:8])
	srcID := binary.BigEndian.Uint32(packet[8:12])
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	if !v.inReal || srcID != v.srcID {
		// Nueva fuente (o vuelta del fondo): continuar la línea de tiempo de salida.
		v.offset = v.wallTS(now) - srcTS
		v.srcID, v.inReal = srcID, true
	}
	v.lastReal = now
	r.vemitLocked(payload, marker, srcTS+v.offset)
}

// vfillTickLoop emite el fondo cuando no llega vídeo real y hay consumidores.
func (r *RTPRelay) vfillTickLoop() {
	t := time.NewTicker(vfillTick)
	defer t.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-t.C:
			if r.ConsumerCount() == 0 {
				continue
			}
			v := r.vfill
			v.mu.Lock()
			now := time.Now()
			if now.Sub(v.lastReal) >= vfillIdle && now.Sub(v.lastFill) >= vfillInterval {
				v.inReal = false
				ts := v.wallTS(now)
				for i, p := range v.frame {
					r.vemitLocked(p, i == len(v.frame)-1, ts)
				}
				v.lastFill = now
			}
			v.mu.Unlock()
		}
	}
}

// vemitLocked construye el RTP de salida y lo reparte. Asume v.mu tomado.
func (r *RTPRelay) vemitLocked(payload []byte, marker bool, ts uint32) {
	v := r.vfill
	pkt := make([]byte, 12+len(payload))
	pkt[0], pkt[1] = 0x80, v.pt
	if marker {
		pkt[1] |= 0x80
	}
	binary.BigEndian.PutUint16(pkt[2:4], v.seq)
	binary.BigEndian.PutUint32(pkt[4:8], ts)
	binary.BigEndian.PutUint32(pkt[8:12], v.ssrc)
	copy(pkt[12:], payload)
	v.seq++
	r.fanOut(pkt)
}

// annexBNALs parte un flujo Annex-B en NALs, descartando SEI (tipo 6) y AUD (9).
func annexBNALs(b []byte) [][]byte {
	var out [][]byte
	for _, n := range bytes.Split(b, []byte{0, 0, 1}) {
		n = bytes.TrimRight(n, "\x00") // ceros de un start code de 4 bytes
		if len(n) == 0 {
			continue
		}
		if t := n[0] & 31; t == 6 || t == 9 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// packetizeH264 empaqueta NALs en payloads RTP (RFC 6184): NAL único si cabe,
// si no fragmentación FU-A.
func packetizeH264(nals [][]byte, mtu int) [][]byte {
	var out [][]byte
	for _, n := range nals {
		if len(n) <= mtu {
			out = append(out, n)
			continue
		}
		hdr, body := n[0], n[1:]
		for first := true; len(body) > 0; first = false {
			chunk := mtu - 2
			if chunk > len(body) {
				chunk = len(body)
			}
			fuh := hdr & 31
			if first {
				fuh |= 0x80
			}
			if chunk == len(body) {
				fuh |= 0x40
			}
			p := append([]byte{hdr&0xe0 | 28, fuh}, body[:chunk]...)
			out = append(out, p)
			body = body[chunk:]
		}
	}
	return out
}
