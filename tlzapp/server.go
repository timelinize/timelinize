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
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// // Run runs the HTTP server backed by the given application instance.
// // It blocks until it is stopped.
// func Run(listen string, app *application.App) error {
// 	if serverInstance != nil {
// 		return fmt.Errorf("server already running")
// 	}

// 	serverInstance = newServer(listen, app)

// 	// if CLI invoked an API endpoint, execute it and
// 	// return instead of starting server
// 	handled, err := serverInstance.cliMux.Handle()
// 	if err != nil {
// 		serverInstance.log.Fatal(err.Error())
// 	}
// 	if handled {
// 		return nil
// 	}

// 	ln, err := net.Listen("tcp", listen)
// 	if err != nil {
// 		return fmt.Errorf("opening listener: %v", err)
// 	}
// 	serverInstance.listener = ln

// 	serverInstance.log.Info("started server", zap.String("listener", ln.Addr().String()))

// 	err = http.Serve(ln, serverInstance.httpMux)
// 	if err != nil {
// 		if errors.Is(err, net.ErrClosed) {
// 			// normal; the listener was closed
// 			serverInstance.log.Info("stopped server", zap.String("listener", ln.Addr().String()))
// 		} else {
// 			return err
// 		}
// 	}

// 	return nil
// }

type server struct {
	app *App

	log *zap.Logger

	adminLn    net.Listener // plaintext, no authentication (loopback-only by default)
	httpServer *http.Server

	// enforce CORS and prevent DNS rebinding for the unauthenticated admin listener
	allowedHosts   []string
	allowedOrigins []string

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

	// if endpointName, ok := strings.CutPrefix(r.URL.Path, apiBasePath); ok {
	// 	endpoint, ok := s.app.commands[endpointName]
	// 	if !ok {
	// 		err = Error{
	// 			Err:        fmt.Errorf("endpoint '%s' not registered", endpointName),
	// 			HTTPStatus: http.StatusNotFound,
	// 			Log:        "looking up API endpoint by name",
	// 			Message:    "Unknown API endpoint",
	// 		}
	// 		handleError(w, r, err)
	// 		return
	// 	}
	// 	if err = endpoint.ServeHTTP(w, r); err != nil {
	// 		handleError(w, r, err)
	// 		return
	// 	}
	// }

	// ok, we're all set up, so actual handling can happen now
	s.mux.ServeHTTP(rec, r)
}

func (s *server) fillAllowedHosts(listenAddr string) {
	loopbacks := []string{
		"localhost",
		"127.0.0.1",
		"::1",
	}
	allowed := make([]string, 0, len(loopbacks))
	_, port, _ := net.SplitHostPort(listenAddr)
	for _, host := range loopbacks {
		// clients generally omit port if standard, so only expect port if non-standard
		if port != "80" && port != "443" {
			host = net.JoinHostPort(host, port)
		}
		allowed = append(allowed, host)
	}
	s.allowedHosts = allowed
}

func (s *server) fillAllowedOrigins(listenAddr string) {
	loopbacks := []string{
		"http://localhost",
		"http://127.0.0.1",
		"http://[::1]",
	}
	allowed := make([]string, 0, len(loopbacks))
	_, port, _ := net.SplitHostPort(listenAddr)
	for _, origin := range loopbacks {
		// clients generally omit port if standard, so only expect port if non-standard
		if port != "80" && port != "443" {
			origin += ":" + port
		}
		allowed = append(allowed, origin)
	}
	s.allowedOrigins = allowed
}

// enforceHost returns a handler that wraps next such that
// it will only be called if the request's Host header matches
// a trustworthy/expected value. This helps to mitigate DNS
// rebinding attacks.
func (s server) enforceHost(next handler) handler {
	return handlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		var allowed bool
		for _, allowedHost := range s.allowedHosts {
			if r.Host == allowedHost {
				allowed = true
				break
			}
		}
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
		origin := r.Header.Get("Origin")

		// TODO: Arggh, Firefox has a bug (Jan. 2022) where it doesn't set the Origin header in
		// HTTPS-Only mode, breaking CORS requests. https://bugzilla.mozilla.org/show_bug.cgi?id=1751105
		// (Yes, I found and reported the bug after much chin-scratching and head-banging).
		// (Was verified on Twitter by Eric Lawrence, one of the Edge engineers: https://twitter.com/ericlaw/status/1483972139241857027)
		// Anyway, for now, disable HTTPS-Only mode in Firefox OR add an exception for "http://localhost"

		// only enforce CORS on cross-origin requests (Origin header would be set)
		if origin != "" {
			var allowed bool
			for _, allowedOrigin := range s.allowedOrigins {
				if origin == allowedOrigin {
					allowed = true
					break
				}
			}
			if !allowed {
				return Error{
					Err:        fmt.Errorf("unrecognized origin '%s'", origin),
					HTTPStatus: http.StatusForbidden,
					Log:        "Origin not allowed",
					Message:    "You can only access this API from a recognized origin.",
				}
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, "+method)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
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
		}
		return next.ServeHTTP(w, r)
	})
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
