package sip

import (
	"bufio"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// TCPTransport es el transporte concreto del contestador: una conexión TCP persistente contra el
// Flexisip local (implementa AnswererTransport). Send serializa escrituras con un mutex; Close es
// idempotente (sync.Once) porque Answerer.Stop() puede invocarlo más de una vez.
type TCPTransport struct {
	conn      net.Conn
	reader    *bufio.Reader
	logger    *logrus.Logger
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
	lhost     string
	lport     int
}

// NewTCPTransport abre la conexión TCP contra serverAddr (host:puerto) con timeout de conexión de
// 10s, activa TCP_NODELAY (los frames SIP son pequeños y latencia-sensibles) y envuelve la lectura
// en un bufio.Reader para ReadSIPFrame.
func NewTCPTransport(serverAddr string, logger *logrus.Logger) (*TCPTransport, error) {
	if logger == nil {
		logger = logrus.New()
	}
	conn, err := net.DialTimeout("tcp", serverAddr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	host, port := splitHostPort(conn.LocalAddr().String())
	return &TCPTransport{
		conn:   conn,
		reader: bufio.NewReader(conn),
		logger: logger,
		lhost:  host,
		lport:  port,
	}, nil
}

// Send escribe un frame SIP crudo en la conexión. Serializado con writeMu: run() (lectura) y las
// llamadas desde Answer/Hangup/registerOnce (bajo a.mu) pueden invocar Send concurrentemente.
func (t *TCPTransport) Send(raw string) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err := t.conn.Write([]byte(raw))
	return err
}

// Recv bloquea hasta leer el siguiente frame SIP completo (o error/EOF si se cierra la conexión).
func (t *TCPTransport) Recv() (*SIPMessage, error) {
	return ReadSIPFrame(t.reader)
}

// LocalHost devuelve la IP local usada para el REGISTER/Contact (la de la conexión saliente).
func (t *TCPTransport) LocalHost() string { return t.lhost }

// LocalPort devuelve el puerto local efímero de la conexión saliente.
func (t *TCPTransport) LocalPort() int { return t.lport }

// Close cierra la conexión. Idempotente: una segunda llamada es un no-op seguro (Answerer.Stop()
// puede invocarlo más de una vez y no debe entrar en pánico sobre una conn ya cerrada).
func (t *TCPTransport) Close() error {
	t.closeOnce.Do(func() {
		t.closeErr = t.conn.Close()
	})
	return t.closeErr
}

// splitHostPort separa "127.0.0.1:41000" en ("127.0.0.1", 41000). Ante un fallo de parseo (no
// debería ocurrir sobre conn.LocalAddr()) devuelve el string completo y puerto 0.
func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}
	return host, port
}
