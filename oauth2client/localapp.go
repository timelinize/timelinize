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

package oauth2client

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// LocalAppSource implements oauth2.TokenSource for
// OAuth2 client apps that have the client app
// credentials (Client ID and Secret) available
// locally. The OAuth2 provider is accessed directly
// using the OAuth2Config field value.
//
// If the OAuth2Config.Endpoint's TokenURL is set
// but the AuthURL is empty, then it is assumed
// that this is a two-legged ("client credentials")
// OAuth2 configuration; i.e. bearer token.
//
// LocalAppSource instances can be ephemeral.
type LocalAppSource struct {
	// OAuth2Config is the OAuth2 configuration.
	OAuth2Config *oauth2.Config

	// AuthCodeGetter is how the auth code
	// is obtained. If not set, a default
	// oauth2client.Browser is used.
	AuthCodeGetter Getter
}

// InitialToken obtains a token using s.OAuth2Config
// and s.AuthCodeGetter (unless the configuration
// is for a client credentials / "two-legged" flow).
func (s LocalAppSource) InitialToken(ctx context.Context) (*oauth2.Token, error) {
	if s.OAuth2Config == nil {
		return nil, fmt.Errorf("missing OAuth2Config")
	}

	// if this is a two-legged config ("client credentials" flow,
	// where the client bears the actual token, like a password,
	// without an intermediate app) configuration, then we can
	// just return that bearer token immediately
	if tlc := s.twoLeggedConfig(); tlc != nil {
		return tlc.Token(ctx)
	}

	if s.AuthCodeGetter == nil {
		s.AuthCodeGetter = Browser{}
	}

	info, err := AuthCodeExchangeInfo(s.OAuth2Config)
	if err != nil {
		return nil, fmt.Errorf("making auth code exchange info: %v", err)
	}

	code, err := s.AuthCodeGetter.Get(ctx, info.State, info.AuthCodeURL)
	if err != nil {
		return nil, fmt.Errorf("getting code via browser: %v", err)
	}

	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)

	return s.OAuth2Config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", info.CodeVerifier))
}

// TokenSource returns a token source for s.
func (s LocalAppSource) TokenSource(ctx context.Context, tkn *oauth2.Token) oauth2.TokenSource {
	if tlc := s.twoLeggedConfig(); tlc != nil {
		return tlc.TokenSource(ctx)
	}
	return s.OAuth2Config.TokenSource(ctx, tkn)
}

// twoLeggedConfig returns a clientcredentials configuration if
// this app source appears to be configured as one (i.e. with
// bearer credentials, with a token URL but without an auth URL,
// because the client credentials is the actual authentication).
func (s LocalAppSource) twoLeggedConfig() *clientcredentials.Config {
	if s.OAuth2Config.Endpoint.TokenURL != "" &&
		s.OAuth2Config.Endpoint.AuthURL == "" {
		return &clientcredentials.Config{
			ClientID:     s.OAuth2Config.ClientID,
			ClientSecret: s.OAuth2Config.ClientSecret,
			TokenURL:     s.OAuth2Config.Endpoint.TokenURL,
			Scopes:       s.OAuth2Config.Scopes,
		}
	}
	return nil
}

var _ App = LocalAppSource{}
