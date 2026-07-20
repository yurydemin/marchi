package oauth2

import "fmt"

// XOAUTH2InitialResponse builds the raw (pre-base64) SASL initial
// response for the XOAUTH2 mechanism, per Google's XOAUTH2 protocol
// (https://developers.google.com/gmail/imap/xoauth2-protocol) — the same
// mechanism Microsoft's IMAP/SMTP also accept. XOAUTH2 isn't in RFC 4422;
// it predates and is distinct from RFC 7628's OAUTHBEARER, which neither
// Google nor Microsoft's mail protocols require.
//
// The format is fixed: "user=<email>\x01auth=Bearer <token>\x01\x01".
// Base64-encoding this (and framing it as the SASL initial response for
// mechanism name "XOAUTH2") is the caller's job — internal/imapclient
// wraps this in a sasl.Client, internal/restore/smtp.go wraps it in a
// net/smtp-style Auth for the SMTP fallback (Phase 3 step 14).
func XOAUTH2InitialResponse(username, accessToken string) []byte {
	return []byte(fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", username, accessToken))
}
