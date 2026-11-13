package main

import "golang.org/x/crypto/ssh"

func withExtra(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for _, e := range extra {
		found := false
		for _, o := range out {
			if o == e {
				found = true
				break
			}
		}
		if !found {
			out = append(out, e)
		}
	}
	return out
}

// applyLegacy keeps the library's modern defaults first and appends weak
// algorithms last, so they are only chosen when the server offers nothing better.
func applyLegacy(cfg *ssh.ClientConfig) {
	var def ssh.Config
	def.SetDefaults()
	cfg.KeyExchanges = withExtra(def.KeyExchanges,
		"diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1")
	// aes192-cbc / aes256-cbc need the patched crypto module (see README).
	cfg.Ciphers = withExtra(def.Ciphers, "aes128-cbc", "aes192-cbc", "aes256-cbc")
	cfg.HostKeyAlgorithms = []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSA, ssh.KeyAlgoDSA,
	}
}
