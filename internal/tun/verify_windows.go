package tun

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// signer is the publisher whose Authenticode signature wintun.dll must
// carry before it is loaded.
const signer = "WireGuard LLC"

var procCryptMsgClose = windows.NewLazySystemDLL("crypt32.dll").NewProc("CryptMsgClose")

// VerifyDLL checks that the file has a valid Authenticode signature from
// the Wintun publisher.
func VerifyDLL(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fi := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: p}
	data := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&fi),
	}
	verr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	if verr != nil {
		return fmt.Errorf("%s 的数字签名无效：%w", filepath.Base(path), verr)
	}
	names, err := signerNames(path)
	if err != nil {
		return fmt.Errorf("无法读取 %s 的签名者：%w", filepath.Base(path), err)
	}
	for _, n := range names {
		if n == signer {
			return nil
		}
	}
	return fmt.Errorf("%s 的签名者不是 %s：%s", filepath.Base(path), signer, strings.Join(names, ", "))
}

// signerNames lists the subject names of the certificates in the file's
// embedded signature.
func signerNames(path string) ([]string, error) {
	p, _ := windows.UTF16PtrFromString(path)
	var store windows.Handle
	var msg windows.Handle
	err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(p),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY,
		0, nil, nil, nil, &store, &msg, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(msg))
	var names []string
	var c *windows.CertContext
	for {
		c, err = windows.CertEnumCertificatesInStore(store, c)
		if err != nil || c == nil {
			break
		}
		buf := make([]uint16, 256)
		n := windows.CertGetNameString(c, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &buf[0], uint32(len(buf)))
		if n > 1 {
			names = append(names, windows.UTF16ToString(buf[:n]))
		}
	}
	return names, nil
}
