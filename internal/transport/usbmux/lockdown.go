package usbmux

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"howett.net/plist"
)

const lockdownPort uint16 = 62078

func (m plistMap) bytes(k string) []byte {
	switch v := m[k].(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		return nil
	}
}

func iosKind(productID int) string {
	switch productID {
	case 0x1299, 0x129a, 0x129f, 0x12a2, 0x12a3, 0x12a4, 0x12a5, 0x12a6, 0x12ab:
		return "iPad"
	case 0x1291, 0x1293, 0x129e:
		return "iPod"
	default:
		return "iPhone"
	}
}

func enrichIOSName(deviceID, serial string, productID int) string {
	fallback := iosKind(productID)
	if serial == "" {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if name := lockdownDeviceName(ctx, deviceID, serial); name != "" {
		return name
	}
	return fallback
}

func lockdownDeviceName(ctx context.Context, deviceID, serial string) string {
	rec, err := readPairRecord(serial)
	if err != nil || rec == nil {
		slog.Debug("lockdown pair record", "serial", serial, "err", err)
		return ""
	}
	stream, err := New().Connect(ctx, deviceID, lockdownPort)
	if err != nil {
		slog.Debug("lockdown connect", "err", err)
		return ""
	}
	defer stream.Close()
	// streamConn's deadline methods cannot configure the Darwin raw stream.
	// Closing it on cancellation also bounds QueryType/GetValue reads, not only TLS.
	stopClose := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopClose()
	conn := &streamConn{ReadWriteCloser: stream}

	if err := lockdownSend(conn, map[string]any{
		"Label":   "revtether",
		"Request": "QueryType",
	}); err != nil {
		return ""
	}
	if _, err := lockdownRead(conn); err != nil {
		return ""
	}

	hostID := rec.str("HostID")
	buid := rec.str("SystemBUID")
	if hostID == "" {
		return ""
	}
	start := map[string]any{
		"Label":   "revtether",
		"Request": "StartSession",
		"HostID":  hostID,
	}
	if buid != "" {
		start["SystemBUID"] = buid
	}
	if err := lockdownSend(conn, start); err != nil {
		return ""
	}
	sess, err := lockdownRead(conn)
	if err != nil || sess.str("Error") != "" {
		slog.Debug("lockdown StartSession", "err", err, "error", sessErr(sess, err))
		return ""
	}

	enableSSL := false
	switch v := sess["EnableSessionSSL"].(type) {
	case bool:
		enableSSL = v
	}

	if enableSSL {
		cert, err := tlsCertFromPair(rec)
		if err != nil {
			slog.Debug("lockdown tls cert", "err", err)
			return ""
		}
		tlsConn := tls.Client(conn, &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			slog.Debug("lockdown tls handshake", "err", err)
			return ""
		}
		conn = &streamConn{ReadWriteCloser: tlsConn}
	}

	if err := lockdownSend(conn, map[string]any{
		"Label":   "revtether",
		"Request": "GetValue",
		"Key":     "DeviceName",
	}); err != nil {
		return ""
	}
	val, err := lockdownRead(conn)
	if err != nil || val.str("Error") != "" {
		return ""
	}
	return val.str("Value")
}

func sessErr(sess plistMap, err error) string {
	if err != nil {
		return err.Error()
	}
	if sess != nil {
		return sess.str("Error")
	}
	return ""
}

func readPairRecord(udid string) (plistMap, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if err := writePlist(c, 1, map[string]any{
		"MessageType":         "ReadPairRecord",
		"PairRecordID":        udid,
		"ClientVersionString": "revtether",
		"ProgName":            "revtether",
	}); err != nil {
		return nil, err
	}
	_, body, err := readPacket(c)
	if err != nil {
		return nil, err
	}
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	raw := m.bytes("PairRecordData")
	if len(raw) == 0 {
		return nil, fmt.Errorf("usbmux: empty pair record")
	}
	var v any
	if _, err := plist.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	pm, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("usbmux: pair record not a dict")
	}
	return pm, nil
}

func tlsCertFromPair(rec plistMap) (tls.Certificate, error) {
	certDER := rec.bytes("HostCertificate")
	keyDER := rec.bytes("HostPrivateKey")
	if len(certDER) == 0 || len(keyDER) == 0 {
		return tls.Certificate{}, fmt.Errorf("usbmux: pair record missing cert")
	}
	if b, _ := pem.Decode(certDER); b != nil {
		certDER = b.Bytes
	}
	key, err := parseHostKey(keyDER)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}, nil
}

func parseHostKey(der []byte) (any, error) {
	if b, _ := pem.Decode(der); b != nil {
		der = b.Bytes
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("usbmux: unknown host key")
}

func lockdownSend(w io.Writer, v any) error {
	body, err := plist.Marshal(v, plist.XMLFormat)
	if err != nil {
		return err
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(body)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func lockdownRead(r io.Reader) (plistMap, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size == 0 || size > 1<<20 {
		return nil, fmt.Errorf("lockdown: bad length %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return decodeMap(body)
}

type streamConn struct {
	io.ReadWriteCloser
}

func (c *streamConn) LocalAddr() net.Addr              { return streamAddr{} }
func (c *streamConn) RemoteAddr() net.Addr             { return streamAddr{} }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

type streamAddr struct{}

func (streamAddr) Network() string { return "usbmux" }
func (streamAddr) String() string  { return "lockdown" }
