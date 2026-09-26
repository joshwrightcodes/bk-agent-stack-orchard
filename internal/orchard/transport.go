package orchard

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
)

// Transport gives the transport of the calls to Orchard. The Orchard controller makes its own
// certificate when nobody gives it one, and no public authority signed that certificate, so the
// controller of this project trusts the certificate or the authority that the operator gives, as
// PEM, beside the authorities of the system. Empty PEM trusts the authorities of the system only.
//
// Only the calls to Orchard trust it. The calls to Buildkite keep the authorities of the system.
func Transport(caPEM string) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("the default transport of net/http is not an *http.Transport")
	}

	transport := base.Clone()

	if caPEM == "" {
		return transport, nil
	}

	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}

	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("the Orchard certificate holds no PEM certificate")
	}

	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	transport.TLSClientConfig.RootCAs = roots

	return transport, nil
}
