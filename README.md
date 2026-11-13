# KEX

Tabbed SSH client for Windows (Go + Wails + xterm.js).

## Requirements
- Go 1.21+, Node.js 18+, Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- WebView2 runtime (preinstalled on Windows 10/11)

## Build
    cd frontend && npm install && cd ..
    go mod tidy
    wails dev      # live development
    wails build    # -> build/bin/kex.exe

## Encrypted vault
All saved data (sessions, terminal settings, trusted host keys and remembered passwords) lives in one
encrypted file, by default `%AppData%\KEX\vault.kex`. KEX asks for the master password on startup
(or creates the vault on first run).

- Format: Argon2id (3 passes, 64 MiB, 4 lanes) derives a 256-bit key from the master password; the content is
  encrypted and authenticated with AES-256-GCM, with a fresh random nonce on every save and the header
  (KDF parameters, salt) authenticated as additional data. Nothing sensitive is stored outside the file.
- Portable: the file is self-contained. Use **Export vault…** to save a copy, take it to another machine and
  choose **Open other vault…** on the unlock screen of another KEX instance. Same master password.
- **Lock / switch vault** closes all connections, wipes the decrypted data from memory and shows the unlock
  screen again, where you can pick a different vault file.
- **Master password…** changes the password (re-encrypts with a new salt).
- There is no recovery if the master password is forgotten.
- If an old unencrypted config from GoSSH/Shellfinch (`%AppData%\Shellfinch` or `%AppData%\GoSSH`) exists,
  it is imported when the vault is created and KEX offers to delete the old file. Passwords stored by the old
  versions in Windows Credential Manager are not migrated; enter them once and tick "Remember password" again.

## Other notes
- Auth: private key file (passphrase asked on connect, never stored) or password (asked on connect;
  optionally remembered inside the encrypted vault).
- Host keys: trust-on-first-use; a changed key is rejected.
- Shortcuts: Ctrl+Tab / Ctrl+Shift+Tab switch tabs, Ctrl+Shift+W closes, middle-click closes a tab.
- Clipboard: selecting text copies it, right click pastes. Ctrl+C still sends an interrupt to the remote shell.
  If a remote program captures the mouse (tmux, mc, vim with mouse), hold Shift to select.

## Old devices (legacy algorithms)
Tick "Allow legacy algorithms" in a session to additionally offer aes128-cbc, aes192-cbc, aes256-cbc,
diffie-hellman-group14-sha1, diffie-hellman-group1-sha1 and the ssh-rsa / ssh-dss host key types.
Modern algorithms stay first in the list; the weak ones are only used if the server offers nothing better.
Enable it only for devices that need it.

The upstream golang.org/x/crypto/ssh implements aes128-cbc but not aes192-cbc / aes256-cbc. To get those,
run once from the project root:

    powershell -ExecutionPolicy Bypass -File .\setup-legacy-crypto.ps1

It copies x/crypto into third_party\crypto, adds the two ciphers to ssh\cipher.go and points go.mod at the copy.
Manual alternative: add these two lines next to the `aes128cbcID` entry in the `cipherModes` map and use a
`replace golang.org/x/crypto => ./third_party/crypto` directive:

    "aes192-cbc": {24, aes.BlockSize, newAESCBCCipher},
    "aes256-cbc": {32, aes.BlockSize, newAESCBCCipher},
