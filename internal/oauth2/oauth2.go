// Package oauth2 implements FR-AM-01's OAuth2 accounts (Google and
// Microsoft): the BYO-app authorization-code exchange, refresh, and the
// SASL XOAUTH2 initial-response encoding IMAP and SMTP both use to
// authenticate with a bearer token instead of a plain password.
//
// поправка #4 ("BYO app"): MailVault ships no shared OAuth client — the
// user registers their own application with Google/Microsoft and pastes
// in its client_id/client_secret (internal/oauth2config.Manager, backed
// by the oauth2_apps table). This package only knows how to talk to
// whichever provider's endpoint once it's been handed those credentials.
package oauth2

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	"github.com/yurydemin/marchi/internal/domain"
)

// googleEndpoint/microsoftEndpoint are hand-specified rather than pulled
// from golang.org/x/oauth2/google (which drags in extra Google Cloud SDK
// dependencies this project has no other use for) — both are just two
// well-known, stable URLs.
var googleEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

var microsoftEndpoint = oauth2.Endpoint{
	AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
	TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
}

// defaultScopes is each provider's minimum scope set for full IMAP+SMTP
// access (Phase 3 step 14 adds SMTP; requesting it upfront here avoids a
// second consent round-trip later). Microsoft's offline_access scope is
// what makes a refresh_token come back at all — without it the access
// token silently can't be renewed.
var defaultScopes = map[string][]string{
	domain.OAuth2ProviderGoogle:    {"https://mail.google.com/"},
	domain.OAuth2ProviderMicrosoft: {"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send", "offline_access"},
}

func endpointFor(provider string) (oauth2.Endpoint, error) {
	switch provider {
	case domain.OAuth2ProviderGoogle:
		return googleEndpoint, nil
	case domain.OAuth2ProviderMicrosoft:
		return microsoftEndpoint, nil
	default:
		return oauth2.Endpoint{}, fmt.Errorf("oauth2: unknown provider %q", provider)
	}
}

// Token is the JSON shape stored (AES-256-GCM encrypted) in
// accounts.oauth2_token_encrypted — FR-ST-03's schema note: "должна
// хранить сериализованный JSON (access_token, refresh_token, expiry,
// scope)". golang.org/x/oauth2.Token doesn't carry Scope as a top-level
// field (providers return it as an ad-hoc "scope" extra, if at all), so
// this wraps it rather than storing *oauth2.Token directly.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	Scope        string    `json:"scope"`
}

func tokenFrom(t *oauth2.Token) Token {
	scope, _ := t.Extra("scope").(string)
	return Token{
		AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Expiry: t.Expiry, Scope: scope,
	}
}

// toOAuth2Token rebuilds the golang.org/x/oauth2.Token shape Refresh
// needs as input (TokenSource only reads AccessToken/RefreshToken/Expiry
// from it, so Scope isn't needed here).
func (t Token) toOAuth2Token() *oauth2.Token {
	return &oauth2.Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Expiry: t.Expiry}
}

// Expired reports whether the access token needs refreshing — with a
// small early-refresh margin so a request doesn't race an about-to-expire
// token.
func (t Token) Expired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(30 * time.Second).After(t.Expiry)
}

// App is one provider's BYO OAuth2 application, ready to drive the
// authorization-code flow — plaintext ClientSecret, decrypted by the
// caller (internal/oauth2config.Manager) right before constructing this.
type App struct {
	Provider     string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func (a App) config() (*oauth2.Config, error) {
	endpoint, err := endpointFor(a.Provider)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID: a.ClientID, ClientSecret: a.ClientSecret, Endpoint: endpoint,
		RedirectURL: a.RedirectURL, Scopes: defaultScopes[a.Provider],
	}, nil
}

// AuthURL builds the URL to send the user's browser to for consent.
// state should be an unguessable, per-attempt value the caller verifies
// on the callback (CSRF protection for the OAuth2 dance) — this package
// doesn't generate or track it itself.
func (a App) AuthURL(state string) (string, error) {
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	// AccessTypeOffline + prompt=consent is what makes Google actually
	// return a refresh_token — by default it only does so on the very
	// first consent, which is useless for re-adding an account later.
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")), nil
}

// Exchange trades an authorization code (from the callback redirect) for
// an access/refresh token pair.
func (a App) Exchange(ctx context.Context, code string) (Token, error) {
	cfg, err := a.config()
	if err != nil {
		return Token{}, err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return Token{}, fmt.Errorf("oauth2: exchanging authorization code: %w", err)
	}
	return tokenFrom(tok), nil
}

// Refresh obtains a fresh access token using current's refresh token.
// Providers don't always return a new refresh_token on every refresh —
// if the response omits one, the returned Token keeps current's.
func (a App) Refresh(ctx context.Context, current Token) (Token, error) {
	cfg, err := a.config()
	if err != nil {
		return Token{}, err
	}
	src := cfg.TokenSource(ctx, current.toOAuth2Token())
	tok, err := src.Token()
	if err != nil {
		return Token{}, fmt.Errorf("oauth2: refreshing token: %w", err)
	}
	refreshed := tokenFrom(tok)
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = current.RefreshToken
	}
	return refreshed, nil
}
