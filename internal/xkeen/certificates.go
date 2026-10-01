package xkeen

import (
	"crypto/x509"
	"os"
)

// Go's Linux default paths do not include Entware's /opt certificate bundle.
// Keep certificate verification enabled and add those trusted local roots.
func entwareRoots() *x509.CertPool {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	for _, path := range []string{"/opt/etc/ssl/certs/ca-certificates.crt", "/opt/etc/ssl/cert.pem", "/opt/etc/ssl/certs.pem"} {
		if data, err := os.ReadFile(path); err == nil {
			roots.AppendCertsFromPEM(data)
		}
	}
	return roots
}
