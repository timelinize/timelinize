/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

// Package oauth2client implements a pluggable OAuth2 client that can service
// either local or remote applications.
package oauth2client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	mathrand "math/rand"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// Getter is a type that can get an OAuth2 auth code.
// It must enforce that the state parameter of the
// redirected request matches expectedStateVal.
type Getter interface {
	Get(ctx context.Context, expectedStateVal, authCodeURL string) (code string, err error)
}

// AuthCodeExchangeInfo generates a state and a code verifier challenge string,
// along with the assembled URL for a request to get an authorization code.
func AuthCodeExchangeInfo(cfg *oauth2.Config) (CodeExchangeInfo, error) {
	const stateValLength = 14
	state := randString(stateValLength)

	// support PKCE; we use the "S256" method which is theoretically superior to "plain"
	pkceVerifier, err := generatePKCEVerifier()
	if err != nil {
		return CodeExchangeInfo{}, fmt.Errorf("generating PKCE verifier: %w", err)
	}
	pkceVerifierSha256 := sha256.Sum256([]byte(pkceVerifier))
	pkceVerifierSha256Base64 := base64.RawURLEncoding.EncodeToString(pkceVerifierSha256[:])

	return CodeExchangeInfo{
		State:        state,
		CodeVerifier: pkceVerifier,
		AuthCodeURL: cfg.AuthCodeURL(state,
			oauth2.AccessTypeOffline,
			// PKCE extension
			oauth2.SetAuthURLParam("code_challenge", pkceVerifierSha256Base64),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		),
	}, nil
}

// generatePKCEVerifier generates a PKCE code verifier described at
// https://www.oauth.com/oauth2-servers/pkce/authorization-request/.
// "This is a cryptographically random string using the characters A-Z,
// a-z, 0-9, and the punctuation characters -._~ (hyphen, period,
// underscore, and tilde), between 43 and 128 characters long."
//
// The resulting string meets these criteria even if it does not
// exercise the full range of the character set.
func generatePKCEVerifier() (string, error) {
	const minLength = 43
	p := make([]byte, minLength) // encoded length is longer, but this guarantees at least 43 characters
	if _, err := io.ReadFull(rand.Reader, p); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(p), nil
}

// randString is not safe for cryptographic use.
func randString(n int) string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[mathrand.Intn(len(letterBytes))] //nolint:gosec // the whole point is that it's not crypto-safe, it's fine
	}
	return string(b)
}

type (
	// CodeExchangeInfo holds information for obtaining an auth code.
	CodeExchangeInfo struct {
		State        string `json:"state"`
		CodeVerifier string `json:"code_verifier"` // plaintext value (PKCE extension)
		AuthCodeURL  string `json:"auth_code_url"` // fully-assembled URL
	}

	// App provides a way to get an initial OAuth2 token
	// as well as a continuing token source.
	App interface {
		InitialToken(ctx context.Context) (*oauth2.Token, error)
		TokenSource(ctx context.Context, token *oauth2.Token) oauth2.TokenSource
	}
)

// httpClient is the HTTP client to use for OAuth2 requests.
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

// DefaultRedirectURL is the default URL to
// which to redirect clients after a code
// has been obtained. Redirect URLs may
// have to be registered with your OAuth2
// provider.
const DefaultRedirectURL = "http://localhost:8008/oauth2-redirect"
