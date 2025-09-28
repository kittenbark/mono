package mono

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"golang.org/x/crypto/acme/autocert"
	"math/big"
	"net"
	"strings"
	"time"
)

var TLSOptions = TLSOptionsT{
	CacheDir:     "certs", // Domain name as a sub dir.
	Email:        "",
	Organization: "Dev",
	Country:      "US",
	ValidDays:    365,
	KeySize:      2048,
}

type TLSOptionsT struct {
	CacheDir     string // For ACME certificates
	Email        string // For ACME registration
	Organization string
	Country      string
	ValidDays    int
	KeySize      int
}

func TLS(domains ...string) (*tls.Config, error) {
	manager := &autocert.Manager{
		Cache:      autocert.DirCache(TLSOptions.CacheDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domainsWithWWW(domains)...),
		Email:      TLSOptions.Email,
	}

	return &tls.Config{
		GetCertificate: manager.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1", "acme-tls/1"},
		MinVersion:     tls.VersionTLS13,
		ServerName:     domains[0],
	}, &cursedTLSDataAsError{manager: manager}
}

func SelfSignedTLS(domains ...string) (*tls.Config, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, TLSOptions.KeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{TLSOptions.Organization},
			Country:       []string{TLSOptions.Country},
			Province:      []string{""},
			Locality:      []string{""},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Duration(TLSOptions.ValidDays) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, domain := range domains {
		if ip := net.ParseIP(domain); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, domain)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}, PrivateKey: privateKey}},
		ServerName:   domains[0],
	}, nil
}

type cursedTLSDataAsError struct {
	manager *autocert.Manager
}

func (data *cursedTLSDataAsError) Error() string { return "this not a real error (its cursed)" }

func domainsWithWWW(domains []string) []string {
	result := make([]string, 0, len(domains))
	for _, domain := range domains {
		result = append(result, domain)
		if !strings.HasPrefix(domain, "www.") {
			result = append(result, domain)
		}
	}
	return result
}
