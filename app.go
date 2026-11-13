package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

type tab struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
}

type App struct {
	ctx   context.Context
	store *Store
	mu    sync.Mutex
	tabs  map[string]*tab
}

func NewApp() (*App, error) {
	st, err := NewStore()
	if err != nil {
		return nil, err
	}
	return &App{store: st, tabs: map[string]*tab{}}, nil
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(ctx context.Context) { a.closeAll() }

func (a *App) closeAll() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.tabs))
	for id := range a.tabs {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.Close(id)
	}
}

// ---- bound methods: vault ----

var vaultFilters = []runtime.FileFilter{
	{DisplayName: "KEX vault (*.kex)", Pattern: "*.kex"},
	{DisplayName: "All files (*.*)", Pattern: "*.*"},
}

func (a *App) VaultStatus() VaultInfo { return a.store.Info() }

func (a *App) Unlock(password string) error { return a.store.Unlock(password) }

// CreateVault returns the path of an imported old unencrypted config, if any.
func (a *App) CreateVault(password string) (string, error) { return a.store.Create(password) }

func (a *App) DeleteLegacyConfig(path string) error { return a.store.DeleteLegacy(path) }

func (a *App) ChangePassword(oldPw, newPw string) error { return a.store.ChangePassword(oldPw, newPw) }

// Lock closes every connection and wipes the decrypted data from memory.
func (a *App) Lock() {
	a.closeAll()
	a.store.Lock()
}

func (a *App) ChooseVaultFile() (string, error) {
	p, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Open KEX vault",
		Filters: vaultFilters,
	})
	if err != nil || p == "" {
		return "", err
	}
	return p, a.store.SetPath(p)
}

func (a *App) ChooseNewVaultPath() (string, error) {
	p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Choose where to create the KEX vault",
		DefaultFilename: "vault.kex",
		Filters:         vaultFilters,
	})
	if err != nil || p == "" {
		return "", err
	}
	return p, a.store.SetPath(p)
}

// ExportVault saves a copy of the encrypted vault file; open it from another
// KEX instance with "Open other vault…" and the same master password.
func (a *App) ExportVault() (string, error) {
	p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save a copy of the encrypted vault",
		DefaultFilename: "kex-vault-copy.kex",
		Filters:         vaultFilters,
	})
	if err != nil || p == "" {
		return "", err
	}
	return p, a.store.Export(p)
}

// ---- bound methods: sessions & settings ----

func (a *App) ListSessions() []Session { return a.store.Sessions() }

func (a *App) SaveSession(s Session) ([]Session, error) { return a.store.SaveSession(s) }

func (a *App) DeleteSession(id string) ([]Session, error) { return a.store.DeleteSession(id) }

func (a *App) GetSettings() Settings { return a.store.Settings() }

func (a *App) SaveSettings(s Settings) error { return a.store.SaveSettings(s) }

func (a *App) SetClipboard(text string) error { return runtime.ClipboardSetText(a.ctx, text) }

func (a *App) GetClipboard() (string, error) { return runtime.ClipboardGetText(a.ctx) }

func (a *App) PickKeyFile() (string, error) {
	home, _ := os.UserHomeDir()
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Select private key",
		DefaultDirectory: filepath.Join(home, ".ssh"),
	})
}

// ---- bound methods: terminal tabs ----

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func loadSigner(path, passphrase string) (ssh.Signer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("no private key configured for this session")
	}
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(data)
	}
	if err != nil {
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return nil, errors.New("PASSPHRASE_REQUIRED")
		}
		if passphrase != "" && strings.Contains(err.Error(), "decryption password incorrect") {
			return nil, errors.New("PASSPHRASE_WRONG")
		}
		return nil, fmt.Errorf("parsing key: %w", err)
	}
	return signer, nil
}

// Connect opens an SSH shell for the tab id chosen by the frontend, so the
// frontend can subscribe to events before any output is produced.
// secret is the key passphrase (key auth) or the password (password auth).
func (a *App) Connect(tabID, sessionID, secret string, cols, rows int) error {
	sess, ok := a.store.Session(sessionID)
	if !ok {
		return errors.New("session not found")
	}
	passwordAuth := sess.AuthMethod == "password"
	var auth []ssh.AuthMethod
	if passwordAuth {
		pw := secret
		if pw == "" && sess.SavePassword {
			pw, _ = a.store.Password(sess.ID)
		}
		if pw == "" {
			return errors.New("PASSWORD_REQUIRED")
		}
		auth = []ssh.AuthMethod{
			ssh.Password(pw),
			// Many servers expose password login as keyboard-interactive.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}),
		}
	} else {
		signer, err := loadSigner(sess.KeyPath, secret)
		if err != nil {
			return err
		}
		auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}
	cfg := &ssh.ClientConfig{
		User: sess.Username,
		Auth: auth,
		HostKeyCallback: func(host string, _ net.Addr, key ssh.PublicKey) error {
			return a.store.CheckHostKey(host, ssh.FingerprintSHA256(key))
		},
		Timeout: 15 * time.Second,
	}
	if sess.Legacy {
		applyLegacy(cfg)
	}
	addr := net.JoinHostPort(sess.Host, strconv.Itoa(sess.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		if passwordAuth && strings.Contains(err.Error(), "unable to authenticate") {
			return errors.New("PASSWORD_WRONG")
		}
		if !sess.Legacy && strings.Contains(err.Error(), "no common algorithm") {
			return fmt.Errorf("%w (old device? enable \"Allow legacy algorithms\" in this session's settings)", err)
		}
		return err
	}
	if passwordAuth && sess.SavePassword && secret != "" {
		_ = a.store.SetPassword(sess.ID, secret)
	}
	ss, err := client.NewSession()
	if err != nil {
		client.Close()
		return err
	}
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := ss.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		client.Close()
		return err
	}
	stdin, err := ss.StdinPipe()
	if err != nil {
		client.Close()
		return err
	}
	stdout, _ := ss.StdoutPipe()
	stderr, _ := ss.StderrPipe()
	if err := ss.Shell(); err != nil {
		client.Close()
		return err
	}

	a.mu.Lock()
	a.tabs[tabID] = &tab{client: client, sess: ss, stdin: stdin}
	a.mu.Unlock()

	var wg sync.WaitGroup
	pump := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				runtime.EventsEmit(a.ctx, "term:data:"+tabID, base64.StdEncoding.EncodeToString(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout)
	go pump(stderr)
	go func() {
		_ = ss.Wait()
		wg.Wait()
		a.drop(tabID)
		runtime.EventsEmit(a.ctx, "term:closed:"+tabID)
	}()
	return nil
}

func (a *App) get(id string) *tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tabs[id]
}

func (a *App) drop(id string) {
	a.mu.Lock()
	t := a.tabs[id]
	delete(a.tabs, id)
	a.mu.Unlock()
	if t != nil {
		t.client.Close()
	}
}

func (a *App) Write(id, data string) {
	if t := a.get(id); t != nil {
		_, _ = t.stdin.Write([]byte(data))
	}
}

func (a *App) Resize(id string, cols, rows int) {
	if t := a.get(id); t != nil && cols > 0 && rows > 0 {
		_ = t.sess.WindowChange(rows, cols)
	}
}

func (a *App) Close(id string) { a.drop(id) }
