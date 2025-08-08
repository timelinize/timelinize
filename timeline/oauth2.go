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

package timeline

import (
	"context"
	"fmt"
	"net/http"

	"github.com/timelinize/timelinize/oauth2client"
	"golang.org/x/oauth2"
)

// oauth2App returns an oauth2client.App for the OAuth2 provider
// with the given ID.
func oauth2App(providerID string, scopes []string) oauth2client.App {
	// TODO: if we ever allow user-configurable OAuth2 apps (so they can have their own rate limits and such)
	// then using a LocalAppSource would be the way to go:
	// cfg := oauth2.Config{
	// 	ClientID:     "NFJuNXlmSFFhWEdYcDRsMlFETko6MTpjaQ",
	// 	ClientSecret: "t0la1nDJRMz2JDuj2z7OWIF01kM4_Vtkvie4-mij3DmoGFozZO",
	// 	Endpoint: oauth2.Endpoint{
	// 		AuthURL:   "https://twitter.com/i/oauth2/authorize",
	// 		TokenURL:  "https://api.twitter.com/2/oauth2/token",
	// 		AuthStyle: oauth2.AuthStyleInHeader, // TODO: this might be only for Twitter... maybe make this customizable in the oauth credentials config?
	// 	},
	// 	RedirectURL: oauth2client.DefaultRedirectURL,
	// 	Scopes:      scopes,
	// }
	// return oauth2client.LocalAppSource{OAuth2Config: &cfg}, nil
	///////////////////////////

	return oauth2client.RemoteAppSource{
		ProxyURL:   "http://localhost:7233/oauth2", // TODO: put url of backend oauth2proxy here
		ProviderID: providerID,
		Scopes:     scopes,
	}
}

// NewOAuth2HTTPClient returns a new HTTP client which performs
// HTTP requests that are authenticated with an oauth2.Token
// stored with the account acc.
func (acc Account) NewOAuth2HTTPClient(ctx context.Context, oa OAuth2) (*http.Client, error) {
	// load the existing token for this account from the database;
	// note that OAuth-enabled accounts might not have an authorization
	// set, and that's OK, the user might not be using the API
	var tkn *oauth2.Token
	if len(acc.authorization) > 0 {
		err := unmarshalGob(acc.authorization, &tkn)
		if err != nil {
			return nil, fmt.Errorf("gob-decoding OAuth2 token: %w", err)
		}
		if tkn == nil || tkn.AccessToken == "" {
			return nil, fmt.Errorf("OAuth2 token is empty: %+v", tkn)
		}
	}

	// load the service's "oauth app", which can provide both tokens and
	// oauth configs -- in this case, we need the oauth config; we should
	// already have a token
	oapp := oauth2App(oa.ProviderID, oa.Scopes)

	// obtain a token source from the oauth's config so that it can keep
	// the token refreshed if it expires
	src := oapp.TokenSource(ctx, tkn)

	// finally, create an HTTP client that authenticates using the token,
	// but wrapping the underlying token source so we can persist any
	// changes to the database
	return oauth2.NewClient(ctx, &persistedTokenSource{
		tl:        acc.tl,
		ts:        src,
		accountID: acc.ID,
		token:     tkn,
	}), nil
}

// authorizeWithOAuth2 gets an initial OAuth2 token from the user.
// It requires OAuth2AppSource to be set or it will panic.
func authorizeWithOAuth2(ctx context.Context, oc OAuth2) ([]byte, error) {
	src := oauth2App(oc.ProviderID, oc.Scopes)
	tkn, err := src.InitialToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token from source: %w", err)
	}
	return marshalGob(tkn)
}

// persistedTokenSource wraps a TokenSource for
// a particular account and persists any changes
// to the account's token to the database.
type persistedTokenSource struct {
	tl        *Timeline
	ts        oauth2.TokenSource
	accountID int64
	token     *oauth2.Token
}

func (ps *persistedTokenSource) Token() (*oauth2.Token, error) {
	tkn, err := ps.ts.Token()
	if err != nil {
		return tkn, err
	}

	// store an updated token in the DB
	if tkn.AccessToken != ps.token.AccessToken {
		ps.token = tkn

		authBytes, err := marshalGob(tkn)
		if err != nil {
			return nil, fmt.Errorf("gob-encoding new OAuth2 token: %w", err)
		}

		_, err = ps.tl.db.ExecContext(context.TODO(), `UPDATE accounts SET authorization=? WHERE id=?`, authBytes, ps.accountID)
		if err != nil {
			return nil, fmt.Errorf("storing refreshed OAuth2 token: %w", err)
		}
	}

	return tkn, nil
}
