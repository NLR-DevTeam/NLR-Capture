// Package certs creates the root certificate that HTTPS interception signs
// with, issues certificates for intercepted hosts and installs the root in
// the Windows certificate store.
package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CommonName names the root certificate.
const CommonName = "NLR Capture Root CA"

// Authority holds the root certificate and issues host certificates.
type Authority struct {
	Cert    *x509.Certificate
	Key     crypto.Signer
	CertPEM []byte
	path    string

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
	// leafKey is shared by every issued certificate: generating a key per
	// host costs time and gains nothing for interception.
	leafKey *ecdsa.PrivateKey
}

// Load reads the root from dir, creating one the first time.
func Load(dir string) (*Authority, error) {
	p := filepath.Join(dir, "rootCA.pem")
	if b, err := os.ReadFile(p); err == nil {
		a, err := parse(b)
		if err == nil && time.Now().Before(a.Cert.NotAfter.Add(-24*time.Hour)) {
			a.path = p
			return a, nil
		}
	}
	a, err := create()
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(a.Key)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, a.CertPEM...)
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, out, 0o600); err != nil {
		return nil, err
	}
	a.path = p
	return a, nil
}

func parse(b []byte) (*Authority, error) {
	a := &Authority{}
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				return nil, err
			}
			a.Cert = c
			a.CertPEM = pem.EncodeToMemory(blk)
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, errors.New("unsupported key")
			}
			a.Key = s
		}
	}
	if a.Cert == nil || a.Key == nil {
		return nil, errors.New("rootCA.pem is incomplete")
	}
	return a.init()
}

func (a *Authority) init() (*Authority, error) {
	a.leaves = map[string]*tls.Certificate{}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	a.leafKey = k
	return a, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func create() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	ski := sha1.Sum(pub)
	host, _ := os.Hostname()
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject: pkix.Name{
			CommonName:         CommonName,
			Organization:       []string{"NLR Capture"},
			OrganizationalUnit: []string{"Generated on " + host},
		},
		NotBefore:             time.Now().Add(-48 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski[:],
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	c, _ := x509.ParseCertificate(der)
	a := &Authority{Cert: c, Key: key, CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	return a.init()
}

// Regenerate replaces the root with a new one. Certificates issued so far
// are forgotten.
func (a *Authority) Regenerate() error {
	n, err := create()
	if err != nil {
		return err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(n.Key)
	out := append([]byte{}, n.CertPEM...)
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := os.WriteFile(a.path, out, 0o600); err != nil {
		return err
	}
	a.mu.Lock()
	a.Cert, a.Key, a.CertPEM = n.Cert, n.Key, n.CertPEM
	a.leaves = map[string]*tls.Certificate{}
	a.mu.Unlock()
	return nil
}

// Fingerprint returns the SHA-256 fingerprint of the root, in hex pairs.
func (a *Authority) Fingerprint() string {
	sum := sha256.Sum256(a.Cert.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Thumbprint returns the SHA-1 hash Windows identifies certificates by.
func (a *Authority) Thumbprint() []byte {
	sum := sha1.Sum(a.Cert.Raw)
	return sum[:]
}

// DER returns the root certificate in DER form.
func (a *Authority) DER() []byte { return a.Cert.Raw }

// Leaf returns a certificate for host (a DNS name or an IP address),
// issuing it on first use.
func (a *Authority) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		host = "localhost"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.leaves[host]; ok && time.Now().Before(c.Leaf.NotAfter) {
		return c, nil
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: host, Organization: []string{"NLR Capture"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, 390),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		AuthorityKeyId:        a.Cert.SubjectKeyId,
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
		// A wildcard sibling helps clients that reuse the connection for
		// other subdomains.
		if parts := strings.SplitN(host, ".", 2); len(parts) == 2 && strings.Contains(parts[1], ".") {
			tpl.DNSNames = append(tpl.DNSNames, "*."+parts[1])
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.Cert, &a.leafKey.PublicKey, a.Key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	c := &tls.Certificate{Certificate: [][]byte{der, a.Cert.Raw}, PrivateKey: a.leafKey, Leaf: leaf}
	if len(a.leaves) > 5000 {
		a.leaves = map[string]*tls.Certificate{}
	}
	a.leaves[host] = c
	return c, nil
}

// ExportPEM writes the root certificate (without its key) to path; a .cer
// or .der extension writes DER.
func (a *Authority) Export(path string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cer", ".der":
		return os.WriteFile(path, a.Cert.Raw, 0o644)
	}
	return os.WriteFile(path, a.CertPEM, 0o644)
}
