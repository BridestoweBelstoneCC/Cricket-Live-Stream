package server

// The phone page's certificate. A phone won't hand a page its microphone without HTTPS,
// and nothing at a cricket ground can get a real certificate for a private address, so
// this one signs itself and the phone shows a warning once. It's kept between matches, so
// a regular commentator's phone can remember having accepted it; a new one is made only
// when the laptop's address isn't on it any more, or it's near its end.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// quietLog is for the HTTPS port: every phone showing its warning is a failed handshake.
var quietLog = log.New(io.Discard, "", 0)

func talkCertFile() string { return filepath.Join(appDir, "talk_tls.pem") }

// talkLocalIPs is every address a phone might reach this laptop on.
func talkLocalIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ips = append(ips, n.IP)
		}
	}
	return ips
}

func talkTLSConfig() (*tls.Config, error) {
	cert, err := talkLoadCert()
	if err != nil {
		if cert, err = talkMakeCert(); err != nil {
			return nil, err
		}
	}
	// Chosen per connection, not fixed, because the address can move while it's on, and an
	// iPhone refuses a certificate without the address in it. No Certificates here: Go only
	// asks GetCertificate about a connection to a bare address when that list is empty.
	c := &talkCertPicker{}
	if err := c.add(cert); err != nil {
		return nil, err
	}
	return &tls.Config{GetCertificate: c.pick, MinVersion: tls.VersionTLS12}, nil
}

// talkCertPicker holds every certificate made since the port opened. Older ones are kept
// because a phone that accepted one is only trusted with that one: a new certificate for a
// new address mustn't lock out a phone that's on air on the old address.
type talkCertPicker struct {
	mu    sync.Mutex
	certs []tls.Certificate // newest last
	leafs []*x509.Certificate
}

func (c *talkCertPicker) add(cert tls.Certificate) error {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	c.certs, c.leafs = append(c.certs, cert), append(c.leafs, leaf)
	return nil
}

func (c *talkCertPicker) pick(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newest := &c.certs[len(c.certs)-1]
	if hello.Conn == nil {
		return newest, nil
	}
	addr, ok := hello.Conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		return newest, nil
	}
	// Oldest first: a new certificate names every address the laptop has, the old ones too,
	// and a phone already on one of those has only accepted the one it was given.
	for i := range c.leafs {
		if c.leafs[i].VerifyHostname(addr.IP.String()) == nil {
			return &c.certs[i], nil
		}
	}
	cert, err := talkMakeCert(addr.IP)
	if err != nil {
		return newest, nil // the phone gets an error it can't click through; nothing better to offer
	}
	if err := c.add(cert); err != nil {
		return newest, nil
	}
	return &c.certs[len(c.certs)-1], nil
}

// talkLoadCert is the saved certificate, if it's still right for this network.
func talkLoadCert() (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(talkCertFile(), talkCertFile())
	if err != nil {
		return cert, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return cert, err
	}
	if time.Now().Add(30 * 24 * time.Hour).After(leaf.NotAfter) {
		return cert, os.ErrInvalid
	}
	want := talkWantIP()
	if want == "" {
		return cert, nil
	}
	if err := leaf.VerifyHostname(want); err != nil {
		return cert, err
	}
	return cert, nil
}

// talkMakeCert makes a certificate for every address the laptop has now, and any others given.
func talkMakeCert(extra ...net.IP) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	ips := append(talkLocalIPs(), extra...)
	if ip := net.ParseIP(talkWantIP()); ip != nil {
		ips = append(ips, ip)
	}
	name, _ := os.Hostname()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "CricketStream commentary", Organization: []string{"CricketStream"}},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		// Apple refuses a server certificate over 825 days, or one without the name in its
		// SANs or without serverAuth, outright: there'd be no warning to click through.
		NotAfter:    time.Now().Add(800 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: ips,
		DNSNames:    []string{"localhost"},
	}
	if name != "" {
		tpl.DNSNames = append(tpl.DNSNames, name)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	// Not fatal if it can't be saved: it works today, and the phone warns again next time.
	if err := os.WriteFile(talkCertFile()+".tmp", data, 0o600); err == nil {
		os.Rename(talkCertFile()+".tmp", talkCertFile())
	}
	return tls.X509KeyPair(data, data)
}
