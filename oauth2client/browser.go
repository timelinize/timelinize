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
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// Browser gets an OAuth2 code via the web browser.
type Browser struct {
	// RedirectURL is the URL to redirect the browser
	// to after the code is obtained; it is usually a
	// loopback address. If empty, DefaultRedirectURL
	// will be used instead.
	RedirectURL string
}

// Get opens a browser window to authCodeURL for the user to
// authorize the application, and it returns the resulting
// OAuth2 code. It rejects requests where the "state" param
// does not match expectedStateVal.
func (b Browser) Get(ctx context.Context, expectedStateVal, authCodeURL string) (string, error) {
	redirURLStr := b.RedirectURL
	if redirURLStr == "" {
		redirURLStr = DefaultRedirectURL
	}
	redirURL, err := url.Parse(redirURLStr)
	if err != nil {
		return "", err
	}

	ln, err := net.Listen("tcp", redirURL.Host)
	if err != nil {
		return "", err
	}
	defer ln.Close()

	ch := make(chan string)
	errCh := make(chan error)

	go func() {
		handler := func(w http.ResponseWriter, r *http.Request) {
			state := r.FormValue("state")
			code := r.FormValue("code")

			if r.Method != http.MethodGet || r.URL.Path != redirURL.Path || state == "" || code == "" {
				http.Error(w, "This endpoint is for OAuth2 callbacks only", http.StatusNotFound)
				return
			}

			if state != expectedStateVal {
				http.Error(w, "invalid state", http.StatusUnauthorized)
				errCh <- fmt.Errorf("invalid OAuth2 state; expected '%s' but got '%s'",
					expectedStateVal, state)
				return
			}

			fmt.Fprint(w, successBody)
			ch <- code
		}

		// must disable keep-alives, otherwise repeated calls to
		// this method can block indefinitely in some weird bug
		srv := http.Server{Handler: http.HandlerFunc(handler)}
		srv.SetKeepAlivesEnabled(false)
		srv.Serve(ln)
	}()

	err = openBrowser(authCodeURL)
	if err != nil {
		fmt.Printf("Can't open browser: %s.\nPlease follow this link: %s", err, authCodeURL)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case code := <-ch:
		return code, nil
	case err := <-errCh:
		return "", err
	}
}

// openBrowser opens the browser to url.
func openBrowser(url string) error {
	osCommand := map[string][]string{
		"darwin":  {"open"},
		"freebsd": {"xdg-open"},
		"linux":   {"xdg-open"},
		"netbsd":  {"xdg-open"},
		"openbsd": {"xdg-open"},
		"windows": {"cmd", "/c", "start"},
	}

	if runtime.GOOS == "windows" {
		// escape characters not allowed by cmd
		url = strings.Replace(url, "&", `^&`, -1)
	}

	all := osCommand[runtime.GOOS]
	exe := all[0]
	args := all[1:]

	buf := new(bytes.Buffer)

	cmd := exec.Command(exe, append(args, url)...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()

	if err != nil {
		return fmt.Errorf("%v: %s", err, buf.String())
	}

	return nil
}

const successBody = `<!DOCTYPE html>
<html>
	<head>
		<title>OAuth2 Success</title>
		<meta charset="utf-8">
		<style>
			body { text-align: center; padding: 5%; font-family: sans-serif; }
			h1 { font-size: 20px; }
			p { font-size: 16px; color: #444; }
		</style>
	</head>
	<body>
		<h1>Code obtained, thank you!</h1>
		<p>
			You may now close this page and return to the application.
		</p>
	</body>
</html>
`

var _ Getter = Browser{}
