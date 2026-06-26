//go:build wasip1

// root_rusticated.go provides the wasip1 implementation of loadSystemRoots and
// systemVerify. Trust decisions are delegated to the host via the net_cert_verify
// ABI call (syscall.NetCertVerify); no root certificates are extracted or bundled.

package x509

import (
	"errors"
	"syscall"
)

func (c *Certificate) systemVerify(opts *VerifyOptions) (chains [][]*Certificate, err error) {
	// Call the host's platform verifier via the net_cert_verify ABI.
	// We need the full chain from this cert (leaf) up through any intermediates.

	name := ""
	if opts != nil {
		name = opts.DNSName
	}

	var chain [][]byte
	chain = append(chain, c.Raw)
	if opts != nil && opts.Intermediates != nil {
		for _, cert := range opts.Intermediates.Certs {
			if cert != nil {
				chain = append(chain, cert.Raw)
			}
		}
	}

	ok, err := syscall.NetCertVerify(name, chain)
	if err != nil {
		return nil, err
	}
	if ok == 0 {
		return nil, errors.New("x509: certificate verification failed (host verdict)")
	}

	// Host says it's ok. Return a dummy chain to satisfy Go's Verify() logic.
	return [][]*Certificate{{c}}, nil
}

// loadSystemRoots returns an empty pool with systemPool=true so that
// verify.go's GOOS check routes to c.systemVerify, which delegates
// chain verification to the host via net_cert_verify.
func loadSystemRoots() (*CertPool, error) {
	pool := NewCertPool()
	pool.systemPool = true
	return pool, nil
}
