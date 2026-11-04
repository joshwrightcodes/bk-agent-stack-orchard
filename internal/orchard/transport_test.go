// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package orchard_test

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
)

// orchardWithItsOwnCertificate is an Orchard controller whose certificate no public authority
// signed, as the one that Orchard makes for itself.
func orchardWithItsOwnCertificate(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)

	certificate := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	})

	return server, string(certificate)
}

func client(t *testing.T, url, caPEM string) *orchard.Client {
	t.Helper()

	transport, err := orchard.Transport(caPEM)
	if err != nil {
		t.Fatalf("Transport() = %v, want a transport", err)
	}

	c, err := orchard.New(orchard.Options{
		URL: url, ServiceAccount: "bk-agent-stack-orchard", Token: "token",
		HTTPClient: &http.Client{Transport: transport}, Retry: retry.Policy{Attempts: 1},
	})
	if err != nil {
		t.Fatalf("New() = %v, want a client", err)
	}

	return c
}

// The operator gives the certificate of the Orchard controller, and the calls trust it.
func TestTheClientTrustsTheCertificateThatTheOperatorGives(t *testing.T) {
	t.Parallel()

	server, certificate := orchardWithItsOwnCertificate(t)

	if _, err := client(t, server.URL, certificate).ListWorkers(t.Context()); err != nil {
		t.Errorf("ListWorkers() = %v, want an answer from the Orchard of that certificate", err)
	}
}

// Without the certificate, a controller that no public authority signed is not trusted.
func TestTheClientRefusesACertificateThatNobodyGave(t *testing.T) {
	t.Parallel()

	server, _ := orchardWithItsOwnCertificate(t)

	_, err := client(t, server.URL, "").ListWorkers(t.Context())
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("ListWorkers() = %v, want a refusal of the certificate", err)
	}
}

// Text that holds no certificate is a mistake of the operator, which the start must report.
func TestTextThatIsNotAPEMCertificateIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := orchard.Transport("not a certificate"); err == nil {
		t.Error("Transport() with text that is not PEM = nil, want an error")
	}
}

// A public authority can issue a certificate for the name of Orchard. When the operator gives the
// authority of Orchard, the calls trust that authority only, so such a certificate cannot take
// the Orchard token.
func TestTheCertificateThatTheOperatorGivesIsTheOnlyTrust(t *testing.T) {
	t.Parallel()

	_, certificate := orchardWithItsOwnCertificate(t)

	transport, err := orchard.Transport(certificate)
	if err != nil {
		t.Fatalf("Transport() = %v, want a transport", err)
	}

	only := x509.NewCertPool()
	only.AppendCertsFromPEM([]byte(certificate))

	if roots := transport.TLSClientConfig.RootCAs; roots == nil || !roots.Equal(only) {
		t.Error("the calls to Orchard trust more than the certificate that the operator gave")
	}
}
