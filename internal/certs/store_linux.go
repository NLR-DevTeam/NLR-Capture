package certs

import (
	"bytes"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// On Linux the root goes into the system trust store, which needs root and
// is therefore done through pkexec, so the desktop asks for authorization.
// Chrome and Firefox keep their own NSS databases in the user's home; the
// root is added to those too when certutil (libnss3-tools) is installed.

// anchorName is the file the root is stored under in the trust store.
const anchorName = "nlr-capture-root.crt"

// bundles are the trust bundles the distributions build from their stores.
var bundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Arch, Gentoo
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // Fedora, RHEL
	"/etc/ssl/ca-bundle.pem",                            // openSUSE
}

// installScript adds the root file $1 to the system trust store with the
// tool the distribution has.
const installScript = `set -e
f="$1"
if [ -d /usr/local/share/ca-certificates ] && command -v update-ca-certificates >/dev/null 2>&1; then
	install -m 0644 "$f" /usr/local/share/ca-certificates/` + anchorName + `
	update-ca-certificates >/dev/null
elif [ -d /etc/pki/trust/anchors ] && command -v update-ca-certificates >/dev/null 2>&1; then
	install -m 0644 "$f" /etc/pki/trust/anchors/` + anchorName + `
	update-ca-certificates >/dev/null
elif command -v update-ca-trust >/dev/null 2>&1; then
	for d in /etc/pki/ca-trust/source/anchors /etc/ca-certificates/trust-source/anchors; do
		if [ -d "$d" ]; then
			install -m 0644 "$f" "$d/` + anchorName + `"
			update-ca-trust extract
			exit 0
		fi
	done
	exit 3
elif command -v trust >/dev/null 2>&1; then
	trust anchor --store "$f"
else
	exit 3
fi
`

// uninstallScript removes the root from every place installScript may
// have put it, and rebuilds the bundles.
const uninstallScript = `n=0
for d in /usr/local/share/ca-certificates /etc/pki/trust/anchors /etc/pki/ca-trust/source/anchors /etc/ca-certificates/trust-source/anchors; do
	if [ -f "$d/` + anchorName + `" ]; then
		rm -f "$d/` + anchorName + `"
		n=$((n+1))
	fi
done
if command -v trust >/dev/null 2>&1 && [ -n "$1" ]; then
	trust anchor --remove "$1" >/dev/null 2>&1 && n=$((n+1))
fi
if command -v update-ca-certificates >/dev/null 2>&1; then
	update-ca-certificates --fresh >/dev/null 2>&1 || update-ca-certificates >/dev/null 2>&1
fi
if command -v update-ca-trust >/dev/null 2>&1; then
	update-ca-trust extract
fi
echo "$n"
`

// Installed reports whether the system trusts the root.
func (a *Authority) Installed() bool {
	for _, p := range bundles {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for {
			var blk *pem.Block
			blk, b = pem.Decode(b)
			if blk == nil {
				break
			}
			if bytes.Equal(blk.Bytes, a.Cert.Raw) {
				return true
			}
		}
	}
	return false
}

// Install adds the root to the system trust store and to the browsers'
// NSS databases. The desktop asks the user to authorize the change, so the
// call blocks until they answer.
func (a *Authority) Install() error {
	f, err := a.tempPEM()
	if err != nil {
		return err
	}
	defer os.Remove(f)
	if err := runPrivileged(installScript, f); err != nil {
		return err
	}
	for _, db := range nssDBs() {
		certutil("-d", "sql:"+db, "-D", "-n", CommonName)
		certutil("-d", "sql:"+db, "-A", "-t", "C,,", "-n", CommonName, "-i", f)
	}
	return nil
}

// Uninstall removes the root from the system trust store and the NSS
// databases, and reports how many copies were removed.
func (a *Authority) Uninstall() (int, error) {
	n := 0
	for _, db := range nssDBs() {
		if certutil("-d", "sql:"+db, "-D", "-n", CommonName) == nil {
			n++
		}
	}
	f, err := a.tempPEM()
	if err != nil {
		return n, err
	}
	defer os.Remove(f)
	out, err := privilegedOutput(uninstallScript, f)
	if err != nil {
		return n, err
	}
	var m int
	fmt.Sscan(strings.TrimSpace(out), &m)
	return n + m, nil
}

// tempPEM writes the root to a file the privileged script can read.
func (a *Authority) tempPEM() (string, error) {
	f, err := os.CreateTemp("", "nlr-capture-root-*.crt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(a.CertPEM); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Chmod(0o644); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// errCancelled reports that the user declined the authorization.
var errCancelled = errors.New("未获得授权")

func runPrivileged(script string, args ...string) error {
	_, err := privilegedOutput(script, args...)
	return err
}

// privilegedOutput runs a shell script as root through pkexec.
func privilegedOutput(script string, args ...string) (string, error) {
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return "", errors.New("需要 pkexec 来修改系统证书")
	}
	cmd := exec.Command(pkexec, append([]string{"/bin/sh", "-c", script, "nlr-capture"}, args...)...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && (ee.ExitCode() == 126 || ee.ExitCode() == 127):
		return "", errCancelled
	case errors.As(err, &ee) && ee.ExitCode() == 3:
		return "", errors.New("找不到系统证书工具")
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("%v %s", err, msg)
	}
	return out.String(), nil
}

// nssDBs returns the user's NSS databases: Chrome's and Firefox's
// profiles, including the snap and flatpak installs.
func nssDBs() []string {
	if _, err := exec.LookPath("certutil"); err != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dbs []string
	if _, err := os.Stat(filepath.Join(home, ".pki", "nssdb", "cert9.db")); err == nil {
		dbs = append(dbs, filepath.Join(home, ".pki", "nssdb"))
	}
	for _, root := range []string{
		".mozilla/firefox",
		"snap/firefox/common/.mozilla/firefox",
		".var/app/org.mozilla.firefox/.mozilla/firefox",
	} {
		ms, _ := filepath.Glob(filepath.Join(home, root, "*", "cert9.db"))
		for _, m := range ms {
			dbs = append(dbs, filepath.Dir(m))
		}
	}
	return dbs
}

func certutil(args ...string) error {
	return exec.Command("certutil", args...).Run()
}
