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

// Package tlzapp provides the application functionality for timelines, including
// file utilities, the HTTP server and APIs, the registration of endpoints for the
// CLI, signals, etc.
package tlzapp

import (
	"bytes"
	"context"
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
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

var app *App

func (a *App) RunCommand(ctx context.Context, args []string) error {
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
		bodyStr := makeForm(args[1:])
		body = strings.NewReader(bodyStr)
	case JSON:
		bodyBytes, err := makeJSON(args[1:])
		if err != nil {
			return err
		}
		if len(bodyBytes) > 0 {
			body = bytes.NewReader(bodyBytes)
		}
	case None:
	}

	url := "http://" + defaultAdminAddr + apiBasePath + commandName

	req, err := http.NewRequestWithContext(ctx, endpoint.Method, url, body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", string(endpoint.GetContentType()))
	req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)

	// execute the command; if the server is running in another
	// process already, send the request to it; otherwise send
	// a virtual request directly to the HTTP handler function
	var resp *http.Response
	if a.serverRunning() {
		httpClient := &http.Client{Timeout: 1 * time.Minute}
		resp, err = httpClient.Do(req) //nolint:bodyclose // bug filed: https://github.com/timakin/bodyclose/issues/61
		if err != nil {
			return fmt.Errorf("running command on server: %w", err)
		}
	} else {
		if err := a.openRepos(); err != nil {
			return fmt.Errorf("opening repos from last time: %w", err)
		}
		vrw := &virtualResponseWriter{body: new(bytes.Buffer), header: make(http.Header)}
		err := endpoint.ServeHTTP(vrw, req)
		if err != nil {
			return fmt.Errorf("running command: %w", err)
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
		_, _ = io.Copy(os.Stdout, resp.Body)
	}

	if resp.StatusCode >= lowestErrorStatus {
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

// TODO: This is not ideal, but I'm just throwing this together temporarily to get us up and running quickly and easily.
// We should have a less externally-dependent way of getting this running. :)
func installPython(ctx context.Context) error {
	if _, err := exec.LookPath("uv"); err == nil {
		return nil
	}

	switch runtime.GOOS {
	case "darwin", "linux":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://astral.sh/uv/install.sh", nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("downloading installer script: got HTTP status %d", resp.StatusCode)
		}

		const maxScriptSize = 1024 * 256
		respBody := io.LimitReader(resp.Body, maxScriptSize)

		cmd := exec.Command("sh")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		inPipe, err := cmd.StdinPipe()
		if err != nil {
			return err
		}

		if err := cmd.Start(); err != nil {
			return err
		}
		if _, err := io.Copy(inPipe, respBody); err != nil {
			return err
		}
		inPipe.Close()
		if err := cmd.Wait(); err != nil {
			return err
		}

		// TODO: Figure out how to update the PATH or at least get the 'uv' path so we can execute it right away;
		// currently we have to restart the shell Timelinize is running in

	case osWindows:
		cmd := exec.Command("powershell", "-c", "irm https://astral.sh/uv/install.ps1 | iex")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}

	default:
		return fmt.Errorf("OS not supported for auto-installation of ML environment: %s", runtime.GOOS)
	}

	return nil
}

func startMLServer() error {
	// TODO: This has to be distributable somehow; maybe embed it into the binary and then write it to an application dir or something
	cmd := exec.Command("uv", "run", "server.py")
	cmd.Dir = "ml"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// TODO: a way to manage the process (stop it, etc)...
	return cmd.Start()
}

func (a *App) serve(adminAddr string) error {
	// TODO: Eventually this will be configurable, but seems like a good idea to do at first
	if err := installPython(a.ctx); err != nil {
		return fmt.Errorf("setting up ML environment: %w", err)
	}

	if err := startMLServer(); err != nil {
		return fmt.Errorf("starting ML server: %w", err)
	}

	if err := a.openRepos(); err != nil {
		return fmt.Errorf("opening previously-opened repositories: %w", err)
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
		return fmt.Errorf("opening listener: %w", err)
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
		server := &http.Server{
			Handler:           a.server,
			ReadHeaderTimeout: 10 * time.Second,
			MaxHeaderBytes:    1024 * 512,
		}
		if err := server.Serve(ln); err != nil {
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
	const maxWait = 30 * time.Second
	var cancel context.CancelFunc
	ctx, cancel := context.WithTimeout(a.ctx, maxWait)
	defer cancel()

	// set up HTTP client and request with short timeout and context cancellation
	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:12002", nil)
	if err != nil {
		return err
	}

	// since some operating systems sometimes do weird things with
	// port reuse (*cough* Windows), poll until connection succeeds
	for {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.Header.Get("Server") == "Timelinize" {
				return nil
			}
		}

		const interval = 500 * time.Millisecond
		timer := time.NewTimer(interval)
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
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
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
			return nil, fmt.Errorf("stripping top level folder from frontend FS: %w", err)
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

	return app, nil
}

func (a *App) openRepos() error {
	// open designated timelines; copy pointer so we don't have to acquire lock on cfg and create deadlock with OpenRepository()
	// TODO: use race detector to verify ^
	lastOpenedRepos := a.cfg.Repositories
	for i, repoDir := range lastOpenedRepos {
		_, err := app.openRepository(a.ctx, repoDir, false)
		if err != nil {
			app.log.Error(fmt.Sprintf("failed to open timeline %d of %d", i+1, len(a.cfg.Repositories)),
				zap.Error(err),
				zap.String("dir", repoDir))
		}
	}

	// persist config so it can be used on restart
	if err := a.cfg.save(); err != nil {
		return fmt.Errorf("persisting config file: %w", err)
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

const lowestErrorStatus = 400

const osWindows = "windows"

const defaultAdminAddr = "127.0.0.1:12002"
