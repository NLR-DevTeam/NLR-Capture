package certs

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	certFindSHA1Hash       = 1 << 16
	certFindSubjectStr     = 0x00080007
	certStoreAddReplace    = 3
	encodingX509ASNAndPKCS = windows.X509_ASN_ENCODING | windows.PKCS_7_ASN_ENCODING
)

func openRoot() (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("ROOT")
	return windows.CertOpenSystemStore(0, name)
}

// Installed reports whether the root is trusted by the current user.
func (a *Authority) Installed() bool {
	st, err := openRoot()
	if err != nil {
		return false
	}
	defer windows.CertCloseStore(st, 0)
	tp := a.Thumbprint()
	blob := windows.CryptDataBlob{Size: uint32(len(tp)), Data: &tp[0]}
	c, err := windows.CertFindCertificateInStore(st, encodingX509ASNAndPKCS, 0, certFindSHA1Hash, unsafe.Pointer(&blob), nil)
	if err != nil || c == nil {
		return false
	}
	windows.CertFreeCertificateContext(c)
	return true
}

// Install adds the root to the current user's trusted roots. Windows asks
// the user to confirm, so the call blocks until they answer.
func (a *Authority) Install() error {
	st, err := openRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(st, 0)
	der := a.Cert.Raw
	ctx, err := windows.CertCreateCertificateContext(encodingX509ASNAndPKCS, &der[0], uint32(len(der)))
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(ctx)
	return windows.CertAddCertificateContextToStore(st, ctx, certStoreAddReplace, nil)
}

// Uninstall removes every NLR Capture root, of this and of earlier
// generations, from the current user's trusted roots. It reports how many
// were removed.
func (a *Authority) Uninstall() (int, error) {
	st, err := openRoot()
	if err != nil {
		return 0, err
	}
	defer windows.CertCloseStore(st, 0)
	subj, _ := windows.UTF16PtrFromString(CommonName)
	var prev *windows.CertContext
	var found []*windows.CertContext
	for {
		c, err := windows.CertFindCertificateInStore(st, encodingX509ASNAndPKCS, 0, certFindSubjectStr, unsafe.Pointer(subj), prev)
		if err != nil || c == nil {
			break
		}
		found = append(found, windows.CertDuplicateCertificateContext(c))
		prev = c
	}
	n := 0
	var lastErr error
	for _, c := range found {
		// Only remove certificates that really are ours: issued to and by
		// our name.
		raw := unsafe.Slice(c.EncodedCert, c.Length)
		if !bytes.Contains(raw, []byte(CommonName)) {
			windows.CertFreeCertificateContext(c)
			continue
		}
		// CertDeleteCertificateFromStore frees the context.
		if err := windows.CertDeleteCertificateFromStore(c); err != nil {
			lastErr = err
			continue
		}
		n++
	}
	return n, lastErr
}
