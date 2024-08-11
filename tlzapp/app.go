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

package tlzapp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

var app *App

func (a *App) RunCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("no command specified")
	}

	commandName := args[0]

	endpoint, ok := a.commands[commandName]
	if !ok {
		return fmt.Errorf("unrecognized command: %s", commandName)
	}

	// make request body
	var body io.Reader
	switch endpoint.GetContentType() {
	case Form:
		bodyStr, err := makeForm(args[1:])
		if err != nil {
			return err
		}
		body = strings.NewReader(bodyStr)
	case JSON:
		bodyBytes, err := makeJSON(args[1:])
		if err != nil {
			return err
		}
		if len(bodyBytes) > 0 {
			body = bytes.NewReader(bodyBytes)
		}
	}

	url := "http://" + defaultAdminAddr + apiBasePath + commandName

	req, err := http.NewRequest(endpoint.Method, url, body)
	if err != nil {
		return fmt.Errorf("building request: %v", err)
	}
	req.Header.Set("Content-Type", string(endpoint.GetContentType()))
	req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)

	// execute the command; if the server is running in another
	// process already, send the request to it; otherwise send
	// a virtual request directly to the HTTP handler function
	var resp *http.Response
	if a.serverRunning() {
		httpClient := &http.Client{Timeout: 1 * time.Minute}
		resp, err = httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("running command on server: %v", err)
		}
	} else {
		if err := a.openRepos(); err != nil {
			return fmt.Errorf("opening repos from last time: %v", err)
		}
		vrw := &virtualResponseWriter{body: new(bytes.Buffer), header: make(http.Header)}
		err := endpoint.ServeHTTP(vrw, req)
		if err != nil {
			return fmt.Errorf("running command: %v", err)
		}
		resp = &http.Response{
			StatusCode:    vrw.status,
			Header:        vrw.header,
			Body:          io.NopCloser(vrw.body),
			ContentLength: int64(vrw.body.Len()),
		}
	}
	defer resp.Body.Close()

	// print out the response
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		// to pretty-print the JSON, we just decode it
		// and then re-encode it ¯\_(ツ)_/¯
		var js interface{}
		err := json.NewDecoder(resp.Body).Decode(&js)
		if err != nil {
			return err
		}
		if js == nil {
			return nil
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "\t")
		err = enc.Encode(js)
		if err != nil {
			return err
		}
	} else {
		io.Copy(os.Stdout, resp.Body)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned error: HTTP %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return nil
}

// Serve serves the application server only if it is not already running
// (possibly in another process). It returns true if it started the
// application server, or false if it was already running.
func (a *App) Serve(adminAddr string) (bool, error) {
	if a.serverRunning() {
		return false, nil
	}
	return true, a.serve(adminAddr)
}

func (a *App) MustServe(adminAddr string) error {
	return a.serve(adminAddr)
}

func (a *App) serve(adminAddr string) error {
	if err := a.openRepos(); err != nil {
		return fmt.Errorf("opening previously-opened repositories: %v", err)
	}

	if a.server.adminLn != nil {
		return fmt.Errorf("server already running on %s", a.server.adminLn.Addr())
	}

	if adminAddr == "" {
		adminAddr = defaultAdminAddr
	}
	a.server.fillAllowedHosts(adminAddr)   // restrict allowed Host headers to mitigate DNS rebinding attacks
	a.server.fillAllowedOrigins(adminAddr) // for CORS enforcement

	ln, err := net.Listen("tcp", adminAddr)
	if err != nil {
		return fmt.Errorf("opening listener: %v", err)
	}
	a.server.adminLn = ln

	a.server.mux = http.NewServeMux()

	addRoute := func(uriPath string, endpoint Endpoint) {
		handler := a.server.enforceHost(endpoint)                           // simple DNS rebinding mitigation
		handler = a.server.enforceOriginAndMethod(endpoint.Method, handler) // simple cross-origin mitigation
		a.server.mux.Handle(uriPath, wrapErrorHandler(handler))
	}

	// static file server
	a.server.staticFiles = http.FileServer(http.FS(a.frontend))
	addRoute("/", Endpoint{
		Method:  http.MethodGet,
		Handler: a.server.serveFrontend,
	})

	// API endpoints
	for command, endpoint := range a.commands {
		addRoute(apiBasePath+command, endpoint)
	}

	// debug endpoints
	addRoute("/debug/pprof/", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(http.HandlerFunc(pprof.Index)),
	})
	addRoute("/debug/pprof/cmdline", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(http.HandlerFunc(pprof.Cmdline)),
	})
	addRoute("/debug/pprof/profile", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(http.HandlerFunc(pprof.Profile)),
	})
	addRoute("/debug/pprof/symbol", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(http.HandlerFunc(pprof.Symbol)),
	})
	addRoute("/debug/pprof/trace", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(http.HandlerFunc(pprof.Trace)),
	})
	addRoute("/debug/vars", Endpoint{
		Method:  http.MethodGet,
		Handler: httpWrap(expvar.Handler()),
	})

	// TODO: remote server (with TLS mutual auth)

	a.log.Info("started admin server", zap.String("listener", ln.Addr().String()))

	go func() {
		if err := http.Serve(ln, a.server); err != nil {
			if errors.Is(err, net.ErrClosed) {
				// normal; the listener was closed
				a.log.Info("stopped admin server", zap.String("listener", ln.Addr().String()))
			} else {
				a.log.Error("admin server failed", zap.String("listener", ln.Addr().String()), zap.Error(err))
			}
		}
	}()

	// don't return until server is actually serving

	// ensure we don't wait longer than a set amount of time
	var cancel context.CancelFunc
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	// set up HTTP client and request with short timeout and context cancellation
	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+adminAddr, nil)
	if err != nil {
		return err
	}

	// since some operating systems sometimes do weird things with
	// port reuse (*cough* Windows), poll until connection succeeds
	for {
		if resp, err := client.Do(req); err == nil && resp.Header.Get("Server") == "Timelinize" {
			return nil
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
	}
}

func (a *App) serverRunning() bool {
	// TODO: get URL from config?
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, "http://localhost:12002", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	return resp.Header.Get("Server") == "Timelinize"
}

func Init(ctx context.Context, cfg *Config, embeddedWebsite fs.FS) (*App, error) {
	if app != nil {
		return nil, errors.New("application already initialized")
	}

	// // TODO: FOR DEVELOPMENT ONLY!!
	// cfg.WebsiteDir = "./frontend"

	cfg.fillDefaults()

	var frontend fs.FS
	if cfg.WebsiteDir == "" {
		// embedded file systems have a top level folder that is annoying for us
		// because it means all requests for these static resources need to be
		// prefixed by the dir name, as if that's relevant!? anyway, strip it.
		var topLevelDir string
		entries, err := fs.ReadDir(embeddedWebsite, ".")
		if err == nil && len(entries) == 1 {
			topLevelDir = entries[0].Name()
		}
		frontend, err = fs.Sub(embeddedWebsite, topLevelDir)
		if err != nil {
			return nil, fmt.Errorf("stripping top level folder from frontend FS: %v", err)
		}
	} else {
		frontend = os.DirFS(cfg.WebsiteDir)
	}

	app = &App{
		ctx:      ctx,
		cfg:      cfg,
		log:      timeline.Log,
		frontend: frontend,
	}
	app.server = server{
		app: app,
		log: app.log.Named("http"),
	}
	app.registerCommands()

	// TODO: for testing only for now!
	// app.Obfuscation(true)

	return app, nil
}

func (a *App) openRepos() error {
	// open designated timelines; copy pointer so we don't have to acquire lock on cfg and create deadlock with OpenRepository()
	// TODO: use race detector to verify ^
	lastOpenedRepos := a.cfg.Repositories
	for i, repoDir := range lastOpenedRepos {
		_, err := app.OpenRepository(repoDir, false)
		if err != nil {
			app.log.Error(fmt.Sprintf("failed to open timeline %d of %d", i+1, len(a.cfg.Repositories)),
				zap.Error(err),
				zap.String("dir", repoDir))
		}
	}

	// persist config so it can be used on restart
	if err := a.cfg.save(); err != nil {
		return fmt.Errorf("persisting config file: %v", err)
	}

	return nil
}

type App struct {
	ctx context.Context
	cfg *Config
	log *zap.Logger

	commands map[string]Endpoint

	server server // TODO: not sure if this needs to be a pointer?

	frontend fs.FS
}

// func (a *App) Logger() *zap.Logger { return a.log }

// func newApp(cfg *Config) *App {
// 	appLog := timeline.Log.Named("app")
// 	a := &App{
// 		ctx:     context.Background(), // default in case GUI isn't used; otherwise replaced by Wails context on app startup
// 		cfg:     cfg,
// 		log:     appLog,
// 		httpLog: appLog.Named("http"),
// 	}
// 	// TODO: ensure this works & is useful
// 	if cfg.WebsiteDir == "" {
// 		// serve from embedded file system (compile-time load)
// 		a.frontend = embeddedWebsite
// 	} else {
// 		// serve directly from disk (request-time load)
// 		a.frontend = os.DirFS(cfg.WebsiteDir)
// 	}
// 	return a
// }

// func (a *App) Run() error {
// return wails.Run(&options.App{
// 	Title:     "Timelinize",
// 	Width:     1200,
// 	Height:    800,
// 	Frameless: runtime.GOOS != "darwin",
// 	AssetServer: &assetserver.Options{
// 		Assets:  a.frontend,
// 		Handler: a.StaticWebsiteHandler(),
// 		// Middleware: a.StaticWebsiteMiddleware,
// 	},
// 	Mac: &mac.Options{
// 		TitleBar: mac.TitleBarHiddenInset(),
// 		About: &mac.AboutInfo{
// 			Title:   "Timelinize",
// 			Message: "All your data, organized on your own computer.\n\n© Dyanim LLC. All rights reserved.",
// 			Icon:    build.AppIcon,
// 		},
// 	},
// 	OnStartup: func(ctx context.Context) {
// 		a.ctx = ctx
// 		timeline.SetWailsAppContext(ctx)
// 	},
// 	Bind: []any{a},
// })
// }

// virtualResponseWriter is used in virtualized HTTP requests
// where the handler is called directly rather than using a
// network.
type virtualResponseWriter struct {
	status int
	header http.Header
	body   *bytes.Buffer
}

func (vrw *virtualResponseWriter) Header() http.Header {
	return vrw.header
}

func (vrw *virtualResponseWriter) WriteHeader(statusCode int) {
	vrw.status = statusCode
}

func (vrw *virtualResponseWriter) Write(data []byte) (int, error) {
	return vrw.body.Write(data)
}

const defaultAdminAddr = "127.0.0.1:12002"
