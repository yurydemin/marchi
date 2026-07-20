package imapclient

import (
	"github.com/emersion/go-sasl"

	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
)

// xoauth2SASLClient implements sasl.Client for the XOAUTH2 mechanism
// (not in go-sasl itself — see internal/oauth2.XOAUTH2InitialResponse's
// doc comment for why it's a distinct, non-RFC mechanism from go-sasl's
// own OAUTHBEARER). Constructed by Connect when
// ConnectOptions.OAuth2AccessToken is set, used via
// *client.Client.Authenticate instead of Login.
type xoauth2SASLClient struct {
	username    string
	accessToken string
}

const xoauth2Mechanism = "XOAUTH2"

func (c *xoauth2SASLClient) Start() (mech string, ir []byte, err error) {
	return xoauth2Mechanism, oauth2pkg.XOAUTH2InitialResponse(c.username, c.accessToken), nil
}

// Next handles the server's response to a rejected XOAUTH2 attempt. Per
// Google's XOAUTH2 protocol, a failed auth doesn't fail immediately —
// the server sends a JSON-encoded error as a challenge, and the client
// must respond with an empty message to complete the exchange (at which
// point the server sends the real tagged NO/BAD go-imap surfaces as the
// Authenticate error). This mirrors go-sasl's own OAUTHBEARER client
// handling of the same two-step failure pattern.
func (c *xoauth2SASLClient) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

var _ sasl.Client = (*xoauth2SASLClient)(nil)
