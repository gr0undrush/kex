package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// ---------------------------------------------------------------- data model

type Session struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	KeyPath  string `json:"keyPath"`
	// "key" (default) or "password"
	AuthMethod   string `json:"authMethod"`
	SavePassword bool   `json:"savePassword"`
	// allow weak algorithms for old devices (CBC ciphers, DH group1, ssh-rsa/ssh-dss)
	Legacy bool `json:"legacy"`
}

type Settings struct {
	FontFamily string `json:"fontFamily"`
	FontSize   int    `json:"fontSize"`
	Foreground string `json:"foreground"`
	Background string `json:"background"`
}

// Config is the plaintext content of the vault.
type Config struct {
	Sessions   []Session         `json:"sessions"`
	Settings   Settings          `json:"settings"`
	KnownHosts map[string]string `json:"knownHosts"`
	Passwords  map[string]string `json:"passwords"` // session id -> remembered password
}

func defaultSettings() Settings {
	return Settings{
		FontFamily: "Cascadia Mono, Consolas, monospace",
		FontSize:   14,
		Foreground: "#cccccc",
		Background: "#0c0c0c",
	}
}

func newConfig() Config {
	return Config{
		Settings:   defaultSettings(),
		KnownHosts: map[string]string{},
		Passwords:  map[string]string{},
	}
}

func (c *Config) fixup() {
	if c.KnownHosts == nil {
		c.KnownHosts = map[string]string{}
	}
	if c.Passwords == nil {
		c.Passwords = map[string]string{}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ------------------------------------------------------------- vault format
//
//   magic "KEXVAULT" (8) | version (1) | argon2id time (4, BE) | memory KiB (4, BE)
//   | threads (1) | salt (16) | nonce (12) | AES-256-GCM(ciphertext + tag)
//
// The header (everything before the nonce) is authenticated as GCM additional data.
// The key is derived from the master password with Argon2id, so the file is
// self-contained and can be opened by any instance of the app that knows the password.

var vaultMagic = []byte("KEXVAULT")

const (
	vaultVersion   = 1
	saltLen        = 16
	nonceLen       = 12
	keyLen         = 32
	headerLen      = 8 + 1 + 4 + 4 + 1 + saltLen
	minPasswordLen = 8
)

type kdfParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

func defaultKDF() kdfParams { return kdfParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4} }

var (
	errLocked     = errors.New("vault is locked")
	errWrongPass  = errors.New("wrong master password (or the vault file is damaged)")
	errNotVault   = errors.New("this is not a KEX vault file")
	errShortPass  = errors.New("master password must be at least 8 characters")
	errBadCurrent = errors.New("current master password is incorrect")
)

func deriveKey(password string, salt []byte, p kdfParams) []byte {
	return argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
}

func buildHeader(p kdfParams, salt []byte) []byte {
	h := make([]byte, 0, headerLen)
	h = append(h, vaultMagic...)
	h = append(h, vaultVersion)
	h = binary.BigEndian.AppendUint32(h, p.Time)
	h = binary.BigEndian.AppendUint32(h, p.MemoryKiB)
	h = append(h, p.Threads)
	h = append(h, salt...)
	return h
}

func parseVault(data []byte) (p kdfParams, salt, header, body []byte, err error) {
	if len(data) < headerLen+nonceLen+16 || !bytes.Equal(data[:8], vaultMagic) {
		return p, nil, nil, nil, errNotVault
	}
	if data[8] != vaultVersion {
		return p, nil, nil, nil, errors.New("unsupported vault version (update KEX)")
	}
	p.Time = binary.BigEndian.Uint32(data[9:13])
	p.MemoryKiB = binary.BigEndian.Uint32(data[13:17])
	p.Threads = data[17]
	// Refuse absurd parameters from a hostile file.
	if p.Time < 1 || p.Time > 16 || p.MemoryKiB < 8 || p.MemoryKiB > 1<<20 || p.Threads < 1 || p.Threads > 32 {
		return p, nil, nil, nil, errNotVault
	}
	salt = data[18 : 18+saltLen]
	header = data[:headerLen]
	body = data[headerLen:]
	return p, salt, header, body, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealVault(plain, key, header []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(header)+nonceLen+len(plain)+gcm.Overhead())
	out = append(out, header...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, header), nil
}

func openVault(body, key, header []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, body[:nonceLen], body[nonceLen:], header)
	if err != nil {
		return nil, errWrongPass
	}
	return plain, nil
}

// -------------------------------------------------------------------- store

type VaultInfo struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Unlocked bool   `json:"unlocked"`
}

type Store struct {
	mu       sync.Mutex
	dir      string // %AppData%\KEX
	path     string // vault file
	cfg      Config
	key      []byte
	salt     []byte
	kdf      kdfParams
	unlocked bool
}

func NewStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "KEX")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, path: filepath.Join(dir, "vault.kex"), cfg: newConfig()}
	// Remember which vault file was used last.
	if b, err := os.ReadFile(filepath.Join(dir, "location.json")); err == nil {
		var loc struct {
			VaultPath string `json:"vaultPath"`
		}
		if json.Unmarshal(b, &loc) == nil && strings.TrimSpace(loc.VaultPath) != "" {
			s.path = loc.VaultPath
		}
	}
	return s, nil
}

func (s *Store) Info() VaultInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(s.path)
	return VaultInfo{Path: s.path, Exists: err == nil, Unlocked: s.unlocked}
}

// SetPath switches the vault file; only allowed while locked.
func (s *Store) SetPath(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unlocked {
		return errors.New("lock the vault first")
	}
	s.path = path
	b, _ := json.Marshal(map[string]string{"vaultPath": path})
	return os.WriteFile(filepath.Join(s.dir, "location.json"), b, 0o600)
}

func (s *Store) Unlock(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unlocked {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	p, salt, header, body, err := parseVault(data)
	if err != nil {
		return err
	}
	key := deriveKey(password, salt, p)
	plain, err := openVault(body, key, header)
	if err != nil {
		return err
	}
	cfg := newConfig()
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return errors.New("vault content is corrupted")
	}
	cfg.fixup()
	s.cfg, s.key, s.salt, s.kdf, s.unlocked = cfg, key, append([]byte{}, salt...), p, true
	return nil
}

func (s *Store) legacyPaths() []string {
	base := filepath.Dir(s.dir)
	return []string{
		filepath.Join(base, "Shellfinch", "config.json"),
		filepath.Join(base, "GoSSH", "config.json"),
	}
}

// Create makes a new vault at the current path. If an old unencrypted config from
// a previous version exists, its sessions/settings/known hosts are imported and its
// path is returned so the UI can offer to delete it.
func (s *Store) Create(password string) (string, error) {
	if len(password) < minPasswordLen {
		return "", errShortPass
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		return "", errors.New("a file already exists at this location")
	}
	cfg := newConfig()
	imported := ""
	for _, lp := range s.legacyPaths() {
		b, err := os.ReadFile(lp)
		if err != nil {
			continue
		}
		var old Config
		if json.Unmarshal(b, &old) == nil {
			cfg = newConfig()
			cfg.Sessions, cfg.KnownHosts = old.Sessions, old.KnownHosts
			if old.Settings.FontFamily != "" {
				cfg.Settings = old.Settings
			}
			cfg.fixup()
			imported = lp
			break
		}
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	s.kdf = defaultKDF()
	s.salt = salt
	s.key = deriveKey(password, salt, s.kdf)
	s.cfg = cfg
	s.unlocked = true
	if err := s.saveLocked(); err != nil {
		s.lockLocked()
		return "", err
	}
	return imported, nil
}

func (s *Store) DeleteLegacy(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lp := range s.legacyPaths() {
		if lp == path {
			return os.Remove(path)
		}
	}
	return errors.New("not a known legacy config path")
}

func (s *Store) lockLocked() {
	for i := range s.key {
		s.key[i] = 0
	}
	s.key, s.salt = nil, nil
	s.cfg = newConfig()
	s.unlocked = false
}

func (s *Store) Lock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockLocked()
}

func (s *Store) saveLocked() error {
	if !s.unlocked {
		return errLocked
	}
	plain, err := json.Marshal(s.cfg)
	if err != nil {
		return err
	}
	out, err := sealVault(plain, s.key, buildHeader(s.kdf, s.salt))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) ChangePassword(oldPw, newPw string) error {
	if len(newPw) < minPasswordLen {
		return errShortPass
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return errLocked
	}
	if subtle.ConstantTimeCompare(deriveKey(oldPw, s.salt, s.kdf), s.key) != 1 {
		return errBadCurrent
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	oldKey, oldSalt, oldKDF := s.key, s.salt, s.kdf
	s.kdf = defaultKDF()
	s.salt = salt
	s.key = deriveKey(newPw, salt, s.kdf)
	if err := s.saveLocked(); err != nil {
		s.key, s.salt, s.kdf = oldKey, oldSalt, oldKDF
		return err
	}
	return nil
}

// Export copies the (already encrypted) vault file to dest.
func (s *Store) Export(dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return errLocked
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, b, 0o600)
}

// ----------------------------------------------------- sessions and settings

func (s *Store) Sessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Session{}, s.cfg.Sessions...)
}

func (s *Store) Session(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return Session{}, false
	}
	for _, x := range s.cfg.Sessions {
		if x.ID == id {
			return x, true
		}
	}
	return Session{}, false
}

func (s *Store) SaveSession(in Session) ([]Session, error) {
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	in.Name = strings.TrimSpace(in.Name)
	if in.Host == "" || in.Username == "" {
		return nil, errors.New("host and username are required")
	}
	if in.Port <= 0 || in.Port > 65535 {
		in.Port = 22
	}
	if in.AuthMethod != "password" {
		in.AuthMethod = "key"
		in.SavePassword = false
	}
	if in.Name == "" {
		in.Name = in.Username + "@" + in.Host
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return nil, errLocked
	}
	found := false
	if in.ID != "" {
		for i := range s.cfg.Sessions {
			if s.cfg.Sessions[i].ID == in.ID {
				s.cfg.Sessions[i] = in
				found = true
				break
			}
		}
	}
	if !found {
		in.ID = newID()
		s.cfg.Sessions = append(s.cfg.Sessions, in)
	}
	if !(in.AuthMethod == "password" && in.SavePassword) {
		delete(s.cfg.Passwords, in.ID)
	}
	return append([]Session{}, s.cfg.Sessions...), s.saveLocked()
}

func (s *Store) DeleteSession(id string) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return nil, errLocked
	}
	out := make([]Session, 0, len(s.cfg.Sessions))
	for _, x := range s.cfg.Sessions {
		if x.ID != id {
			out = append(out, x)
		}
	}
	s.cfg.Sessions = out
	delete(s.cfg.Passwords, id)
	return append([]Session{}, out...), s.saveLocked()
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Settings
}

func (s *Store) SaveSettings(in Settings) error {
	d := defaultSettings()
	if strings.TrimSpace(in.FontFamily) == "" {
		in.FontFamily = d.FontFamily
	}
	if in.FontSize < 6 || in.FontSize > 72 {
		in.FontSize = d.FontSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return errLocked
	}
	s.cfg.Settings = in
	return s.saveLocked()
}

func (s *Store) Password(sessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pw, ok := s.cfg.Passwords[sessionID]
	return pw, ok && pw != ""
}

func (s *Store) SetPassword(sessionID, pw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return errLocked
	}
	if s.cfg.Passwords[sessionID] == pw {
		return nil
	}
	s.cfg.Passwords[sessionID] = pw
	return s.saveLocked()
}

// Trust-on-first-use host key storage.
func (s *Store) CheckHostKey(host, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return errLocked
	}
	known, ok := s.cfg.KnownHosts[host]
	if !ok {
		s.cfg.KnownHosts[host] = fingerprint
		return s.saveLocked()
	}
	if known != fingerprint {
		return errors.New("HOST KEY MISMATCH for " + host +
			" (expected " + known + ", got " + fingerprint +
			"). Possible man-in-the-middle attack; connection refused")
	}
	return nil
}
