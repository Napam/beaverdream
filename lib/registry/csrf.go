package registry

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// CSRFToken derives a form token from an app session token. The page's model
// script cannot read the token across origins, and the app session cookie can
// still arrive on an app-port request because cookies ignore ports. Every
// state-changing app POST must carry both the session and this token.
func (r *Registry) CSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, r.csrfKey)
	mac.Write([]byte("csrf:"))
	mac.Write([]byte(sessionToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyCSRF compares a submitted token in constant time.
func (r *Registry) VerifyCSRF(sessionToken, token string) bool {
	if sessionToken == "" || token == "" {
		return false
	}
	want := r.CSRFToken(sessionToken)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
