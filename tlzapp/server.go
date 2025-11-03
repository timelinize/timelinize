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

// TODO: rename this package to app?
package tlzapp

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

type server struct {
	app *App

	log *zap.Logger

	adminLn    net.Listener // plaintext, no authentication (loopback-only by default)
	httpServer *http.Server

	// enforce CORS and prevent DNS rebinding for the unauthenticated admin listener
	allowedOrigins []*url.URL

	mux         *http.ServeMux
	staticFiles http.Handler
	frontend    fs.FS // the file system that static assets are served from
}

func (s server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// don't do any actual handling yet; just set up the request middleware stuff, logging, etc...
	start := time.Now()

	rec := caddyhttp.NewResponseRecorder(w, nil, nil) // TODO: what other places do we pull in Caddy? Maybe we can strip this down and inline it or something...

	w.Header().Set("Server", "Timelinize")

	var err error
	defer func() {
		logFn := s.log.Info
		if err != nil || rec.Status() >= lowestErrorStatus {
			logFn = s.log.Error
		}

		// the log message is intentionally specific to bust log sampling here
		logFn(r.Method+" "+r.RequestURI,
			// TODO: include the listener address used (probably need to use ConnContext or something whatever it's called on the http.Server)
			zap.String("method", r.Method),
			zap.String("uri", r.RequestURI),
			zap.Int("status", rec.Status()),
			zap.Int("size", rec.Size()),
			zap.Duration("duration", time.Since(start)),
			zap.Error(err),
		)
	}()

	// ok, we're all set up, so actual handling can happen now
	s.mux.ServeHTTP(rec, r)
}

func (s *server) fillAllowedOrigins(configuredOrigins []string, listenAddr string) {
	listenHost, listenPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		listenHost = listenAddr // assume no port (or a default port)
	}
	if configuredOrigins == nil {
		if originEnv := os.Getenv("TLZ_ORIGIN"); originEnv != "" {
			// Split comma-separated origins and trim whitespace
			origins := strings.Split(originEnv, ",")
			configuredOrigins = make([]string, 0, len(origins))
			for _, origin := range origins {
				if trimmed := strings.TrimSpace(origin); trimmed != "" {
					configuredOrigins = append(configuredOrigins, trimmed)
				}
			}
		}
	}
	uniqueOrigins := make(map[string]struct{})
	for _, o := range configuredOrigins {
		uniqueOrigins[o] = struct{}{}
	}
	if !strings.HasPrefix(listenAddr, "/") {
		uniqueOrigins[net.JoinHostPort("localhost", listenPort)] = struct{}{}
		uniqueOrigins[net.JoinHostPort("::1", listenPort)] = struct{}{}
		uniqueOrigins[net.JoinHostPort("127.0.0.1", listenPort)] = struct{}{}
		if listenHost != "" && !s.isLoopback(listenAddr) {
			uniqueOrigins[listenAddr] = struct{}{}
		}
	}
	s.allowedOrigins = make([]*url.URL, 0, len(uniqueOrigins))
	for originStr := range uniqueOrigins {
		var origin *url.URL
		if strings.Contains(originStr, "://") {
			var err error
			origin, err = url.Parse(originStr)
			if err != nil {
				continue
			}
			origin.Path = ""
			origin.RawPath = ""
			origin.Fragment = ""
			origin.RawFragment = ""
			origin.RawQuery = ""
		} else {
			origin = &url.URL{Host: originStr}
		}
		s.allowedOrigins = append(s.allowedOrigins, origin)
	}
}

func (server) isLoopback(addr string) bool {
	// unix socket
	if strings.HasPrefix(addr, "/") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // assume no port
	}
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback()
	}
	return false
}

// enforceHost returns a handler that wraps next such that
// it will only be called if the request's Host header matches
// a trustworthy/expected value. This helps to mitigate DNS
// rebinding attacks.
func (s server) enforceHost(next handler) handler {
	return handlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		allowed := slices.ContainsFunc(s.allowedOrigins, func(u *url.URL) bool {
			return r.Host == u.Host
		})
		if !allowed {
			return Error{
				Err:        fmt.Errorf("unrecognized Host header value '%s'", r.Host),
				HTTPStatus: http.StatusForbidden,
				Log:        "Host not allowed",
				Message:    "This endpoint can only be accessed via a trusted host.",
			}
		}
		return next.ServeHTTP(w, r)
	})
}

// enforceOriginAndMethod ensures that the Origin header matches the expected value(s),
// sets CORS headers, and also enforces the proper/expected method for the route.
// This prevents arbitrary sites from issuing requests to our listener.
func (s server) enforceOriginAndMethod(method string, next handler) handler {
	return handlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		origin := s.getOrigin(r)
		if origin != nil {
			if !s.originAllowed(origin) {
				return Error{
					Err:        fmt.Errorf("unrecognized origin '%s'", origin),
					HTTPStatus: http.StatusForbidden,
					Log:        "Origin not allowed",
					Message:    "You can only access this API from a recognized origin.",
				}
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Origin", origin.String())
				w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, "+method)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Origin", origin.String())
		}
		if r.Method == http.MethodOptions {
			return nil
		}
		// method must match, unless GET is expected, in which case HEAD is also allowed
		if r.Method != method ||
			(method == http.MethodGet &&
				r.Method != method &&
				r.Method != http.MethodHead) {
			// "sir, this is an Arby's"
			return Error{
				Err:        fmt.Errorf("method '%s' not allowed", r.Method),
				HTTPStatus: http.StatusMethodNotAllowed,
			}
		}
		return next.ServeHTTP(w, r)
	})
}

func (s server) getOrigin(r *http.Request) *url.URL {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Arggh, Firefox has a bug (Jan. 2022) where it doesn't set the Origin header in
		// HTTPS-Only mode, breaking CORS requests. https://bugzilla.mozilla.org/show_bug.cgi?id=1751105
		// (Yes, I found and reported the bug after much chin-scratching and head-banging).
		// (Was verified on Twitter by Eric Lawrence, one of the Edge engineers: https://twitter.com/ericlaw/status/1483972139241857027)
		// Anyway, for now, disable HTTPS-Only mode in Firefox OR add an exception for "http://localhost"
		// ... or use the Referer header, I guess.
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return nil
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return nil
	}
	originURL.Path = ""
	originURL.RawPath = ""
	originURL.Fragment = ""
	originURL.RawFragment = ""
	originURL.RawQuery = ""
	return originURL
}

func (s server) originAllowed(origin *url.URL) bool {
	for _, allowedOrigin := range s.allowedOrigins {
		if allowedOrigin.Scheme != "" && origin.Scheme != allowedOrigin.Scheme {
			continue
		}
		if origin.Host == allowedOrigin.Host {
			return true
		}
	}
	return false
}

// TODO: not sure if we'll need this with this program...
// // apiClient is an HTTP client to be used for API requests,
// // considering that some requests are blocking and can take
// // many seconds to finish, like setting repo key. It honors
// // the application's IPv6 configuration setting.
// var apiClient = &http.Client{
// 	Transport: &http.Transport{
// 		Proxy: http.ProxyFromEnvironment,
// 		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
// 			dialer := &net.Dialer{
// 				Timeout:   10 * time.Second,
// 				KeepAlive: 120 * time.Second,
// 			}
// 			if !Config.IPv6 && network == "tcp" {
// 				return dialer.DialContext(ctx, "tcp4", addr)
// 			}
// 			return dialer.DialContext(ctx, network, addr)
// 		},
// 		MaxIdleConns:          100,
// 		IdleConnTimeout:       90 * time.Second,
// 		TLSHandshakeTimeout:   10 * time.Second,
// 		ExpectContinueTimeout: 1 * time.Second,
// 	},
// 	Timeout: 60 * time.Second,
// }
