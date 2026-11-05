package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// On Linux the Claude desktop app is Electron, which uses Chromium's os_crypt
// scheme. The value prefix tells us where the key comes from:
//   - "v10" uses a fixed password, "peanuts". Chromium picks this when the app
//     runs with --password-store=basic, which is what happens when no desktop
//     keyring is available.
//   - "v11" uses a password kept in the desktop keyring (GNOME Keyring,
//     KeePassXC or KWallet) under the name "Claude Safe Storage".
//
// Both use PBKDF2 with a single iteration, AES-128-CBC and an IV of spaces.

// keyringPassword reads the "Claude Safe Storage" password from the desktop
// keyring. It is a variable so tests can replace it without touching D-Bus.
var keyringPassword = readKeyringPassword

var (
	keyringMu       sync.Mutex
	keyringPW       string
	keyringPWLoaded bool
	keyringErr      error
	keyringErrAt    time.Time
)

// keyringRetry is how long a failed keyring read is remembered. Usage is
// checked every minute or two, and a locked keyring would otherwise ask to be
// unlocked on every check.
const keyringRetry = 15 * time.Minute

func decryptValue(data []byte) ([]byte, error) {
	if len(data) < 3+aes.BlockSize {
		return nil, errors.New("unexpected encrypted value format")
	}
	ct := data[3:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	switch string(data[:3]) {
	case "v10":
		return decryptWithPassword(ct, "peanuts")
	case "v11":
		return decryptWithKeyring(ct)
	default:
		return nil, errors.New("unexpected encrypted value format")
	}
}

// decryptWithKeyring decrypts a v11 value. It tries the keyring password first
// and then the empty password, because Chromium does the same when the keyring
// key does not decrypt the value.
func decryptWithKeyring(ct []byte) ([]byte, error) {
	password, err := cachedKeyringPassword()
	if err != nil {
		return nil, err
	}
	if plain, err := decryptWithPassword(ct, password); err == nil {
		return plain, nil
	}
	return decryptWithPassword(ct, "")
}

// cachedKeyringPassword reads the keyring once and remembers the result. The
// read can prompt the user to unlock the keyring, so repeating it for every
// value would be slow and annoying. A failed read is remembered for
// keyringRetry, then tried again in case the keyring was unlocked meanwhile.
func cachedKeyringPassword() (string, error) {
	keyringMu.Lock()
	defer keyringMu.Unlock()
	if keyringPWLoaded {
		return keyringPW, nil
	}
	if keyringErr != nil && time.Since(keyringErrAt) < keyringRetry {
		return "", keyringErr
	}
	password, err := keyringPassword()
	if err != nil {
		keyringErr, keyringErrAt = err, time.Now()
		return "", err
	}
	keyringPW, keyringPWLoaded, keyringErr = password, true, nil
	return keyringPW, nil
}

// forgetKeyringPassword drops the cached keyring password so the next read runs
// again. Tests use it when they swap keyringPassword between cases.
func forgetKeyringPassword() {
	keyringMu.Lock()
	defer keyringMu.Unlock()
	keyringPW, keyringPWLoaded, keyringErr = "", false, nil
}

// decryptWithPassword derives the AES key from password and decrypts ct, which
// is the value after its version prefix. The key derivation is one PBKDF2
// iteration, so it is cheap enough to repeat and is not cached here.
func decryptWithPassword(ct []byte, password string) ([]byte, error) {
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1, 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(plain, ct)
	return unpadPKCS7(plain)
}

// unpadPKCS7 removes PKCS#7 padding. Every padding byte must hold the pad length.
// Checking all of them makes a wrong key much less likely to pass by accident,
// which matters because decryptWithKeyring relies on this check to fall back.
func unpadPKCS7(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("bad padding")
	}
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return nil, errors.New("bad padding")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, errors.New("bad padding")
		}
	}
	return plain[:len(plain)-pad], nil
}

// readKeyringPassword tries GNOME Keyring (or any Secret Service provider such
// as KeePassXC) first, then KWallet for KDE users.
func readKeyringPassword() (string, error) {
	if password, err := secretServicePassword(); err == nil {
		return password, nil
	}
	if password, err := kwalletPassword(); err == nil {
		return password, nil
	}
	return "", errors.New("could not read \"Claude Safe Storage\" from the keyring (GNOME Keyring or KWallet)")
}

const (
	secretServiceName  = "org.freedesktop.secrets"
	secretServicePath  = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceIface = "org.freedesktop.Secret.Service"
	secretItemIface    = "org.freedesktop.Secret.Item"
	secretPromptIface  = "org.freedesktop.Secret.Prompt"
	secretSessionIface = "org.freedesktop.Secret.Session"
	claudeSecretLabel  = "Claude Safe Storage"
	unlockTimeout      = 60 * time.Second
)

// secretValue matches the Secret struct of the Secret Service API. Its fields
// are filled in order, so the names do not have to match the spec.
type secretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// secretServicePassword reads the password over the session bus using the
// freedesktop Secret Service API. It opens its own connection so it can close
// it without affecting the shared one.
func secretServicePassword() (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	svc := conn.Object(secretServiceName, secretServicePath)

	unlocked, locked, err := searchClaudeItems(conn, svc)
	if err != nil {
		return "", err
	}
	session, err := openPlainSession(svc)
	if err != nil {
		return "", err
	}
	defer conn.Object(secretServiceName, session).Call(secretSessionIface+".Close", 0)

	readItems := func(items []dbus.ObjectPath) string {
		for _, item := range items {
			value, err := itemSecret(conn, item, session)
			if err == nil && value != "" {
				return value
			}
		}
		return ""
	}
	if value := readItems(unlocked); value != "" {
		return value, nil
	}
	if len(locked) > 0 {
		more, unlockErr := unlockItems(conn, svc, locked)
		if value := readItems(more); value != "" {
			return value, nil
		}
		if unlockErr != nil {
			return "", unlockErr
		}
	}
	return "", errors.New("could not read the \"Claude Safe Storage\" secret")
}

// searchClaudeItems finds the Claude item and splits the results into unlocked
// and locked. Electron names the item after the app, so that search goes first.
// Older Chromium builds only set the libsecret schema, so those searches also
// have to be checked against the item's label and attributes.
func searchClaudeItems(conn *dbus.Conn, svc dbus.BusObject) (unlocked, locked []dbus.ObjectPath, err error) {
	searches := []map[string]string{
		{"application": "Claude"},
		{"xdg:schema": "chrome_libsecret_os_crypt_password_v2"},
		{"xdg:schema": "chrome_libsecret_os_crypt_password"},
	}
	for i, attrs := range searches {
		var u, l []dbus.ObjectPath
		if err := svc.Call(secretServiceIface+".SearchItems", 0, attrs).Store(&u, &l); err != nil {
			return nil, nil, err
		}
		if i > 0 {
			u = filterClaudeItems(conn, u)
			l = filterClaudeItems(conn, l)
		}
		if len(u)+len(l) > 0 {
			return u, l, nil
		}
	}
	return nil, nil, errors.New("no \"Claude Safe Storage\" item in the keyring")
}

// filterClaudeItems keeps only the items whose label or application attribute
// identifies them as Claude's. A property that cannot be read simply does not
// match.
func filterClaudeItems(conn *dbus.Conn, items []dbus.ObjectPath) []dbus.ObjectPath {
	var keep []dbus.ObjectPath
	for _, item := range items {
		obj := conn.Object(secretServiceName, item)
		var label string
		var attrs map[string]string
		_ = obj.StoreProperty(secretItemIface+".Label", &label)
		_ = obj.StoreProperty(secretItemIface+".Attributes", &attrs)
		if label == claudeSecretLabel || attrs["application"] == "Claude" {
			keep = append(keep, item)
		}
	}
	return keep
}

// unlockItems asks the keyring to unlock the locked items. If it needs the user
// (for example, to type a password), it shows the prompt and waits for it.
func unlockItems(conn *dbus.Conn, svc dbus.BusObject, locked []dbus.ObjectPath) ([]dbus.ObjectPath, error) {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := svc.Call(secretServiceIface+".Unlock", 0, locked).Store(&unlocked, &prompt); err != nil {
		return nil, err
	}
	if prompt == "/" {
		return unlocked, nil
	}
	more, err := runUnlockPrompt(conn, prompt)
	return append(unlocked, more...), err
}

// runUnlockPrompt runs a Secret Service prompt and returns the items it unlocked.
// It subscribes to the Completed signal before starting the prompt, so the
// signal cannot arrive before we are listening.
func runUnlockPrompt(conn *dbus.Conn, prompt dbus.ObjectPath) ([]dbus.ObjectPath, error) {
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(prompt),
		dbus.WithMatchInterface(secretPromptIface),
		dbus.WithMatchMember("Completed"),
	); err != nil {
		return nil, err
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	if err := conn.Object(secretServiceName, prompt).Call(secretPromptIface+".Prompt", 0, "").Err; err != nil {
		return nil, err
	}
	timeout := time.After(unlockTimeout)
	for {
		select {
		case sig := <-signals:
			if sig.Path != prompt || sig.Name != secretPromptIface+".Completed" || len(sig.Body) < 2 {
				continue
			}
			if dismissed, _ := sig.Body[0].(bool); dismissed {
				return nil, errors.New("keyring unlock was dismissed")
			}
			result, ok := sig.Body[1].(dbus.Variant)
			if !ok {
				return nil, errors.New("unexpected keyring unlock result")
			}
			var paths []dbus.ObjectPath
			if err := result.Store(&paths); err != nil {
				return nil, err
			}
			return paths, nil
		case <-timeout:
			return nil, errors.New("timed out waiting for the keyring unlock prompt")
		}
	}
}

// openPlainSession opens a Secret Service session that sends secrets unencrypted.
// That is safe here because the bus is local to this user's session.
func openPlainSession(svc dbus.BusObject) (dbus.ObjectPath, error) {
	var output dbus.Variant
	var session dbus.ObjectPath
	err := svc.Call(secretServiceIface+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session)
	return session, err
}

// itemSecret returns the secret stored in one item.
func itemSecret(conn *dbus.Conn, item, session dbus.ObjectPath) (string, error) {
	var secret secretValue
	err := conn.Object(secretServiceName, item).Call(secretItemIface+".GetSecret", 0, session).Store(&secret)
	return string(secret.Value), err
}

const (
	kwalletIface  = "org.kde.KWallet"
	kwalletFolder = "Claude Keys"
	kwalletKey    = "Claude Safe Storage"
	kwalletAppID  = "claude-account-switcher"
)

// kwalletPassword reads the password from KWallet, the KDE keyring. KWallet 6
// (KDE Frameworks 6) and KWallet 5 use different service names, so try both.
func kwalletPassword() (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	for _, version := range []string{"6", "5"} {
		password, err := readKWallet(conn, "org.kde.kwalletd"+version, dbus.ObjectPath("/modules/kwalletd"+version))
		if err == nil {
			return password, nil
		}
	}
	return "", errors.New("KWallet has no \"Claude Safe Storage\" password")
}

// readKWallet opens the default wallet, reads the password and closes the wallet.
func readKWallet(conn *dbus.Conn, service string, path dbus.ObjectPath) (string, error) {
	obj := conn.Object(service, path)
	var wallet string
	if err := obj.Call(kwalletIface+".networkWallet", 0).Store(&wallet); err != nil {
		return "", err
	}
	var handle int32
	if err := obj.Call(kwalletIface+".open", 0, wallet, int64(0), kwalletAppID).Store(&handle); err != nil {
		return "", err
	}
	if handle < 0 {
		return "", errors.New("KWallet did not open the wallet")
	}
	// Closing is best effort. The wallet is closed anyway when the session ends.
	defer obj.Call(kwalletIface+".close", 0, handle, false, kwalletAppID)

	var password string
	if err := obj.Call(kwalletIface+".readPassword", 0, handle, kwalletFolder, kwalletKey, kwalletAppID).Store(&password); err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("KWallet has no \"Claude Safe Storage\" password")
	}
	return password, nil
}
