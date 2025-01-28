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
	"sync"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

type App struct {
	ctx    context.Context
	cancel context.CancelFunc // shuts down the app

	cfg *Config
	log *zap.Logger

	commands map[string]Endpoint

	server   server
	mlServer *exec.Cmd

	// a reference to the embedded website assets; due to
	// limitations in the go embed tool, it has to be
	// in a parent directory of what is being embedded,
	// so we pass in a reference to it for the app,
	// mainly to pass along to a new app if the config
	// is changed and the app is restarted
	embeddedWebsite fs.FS
}

func New(ctx context.Context, cfg *Config, embeddedWebsite fs.FS) (*App, error) {
	cfg.fillDefaults()

	var frontend fs.FS
	if cfg.WebsiteDir == "" {
		// embedded file systems have a top level folder that is annoying for us
		// because it means all requests for these static resources need to be
		// prefixed by the dir name, as if that's relevant!? anyway, strip it.
		topLevelDir := "."
		entries, err := fs.ReadDir(embeddedWebsite, ".")
		if err == nil && len(entries) == 1 {
			topLevelDir = entries[0].Name()
		}
		frontend, err = fs.Sub(embeddedWebsite, topLevelDir)
		if err != nil {
			return nil, fmt.Errorf("could not strip top level folder from embedded website FS: %w", err)
		}
	} else {
		frontend = os.DirFS(cfg.WebsiteDir)
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)

	newApp := &App{
		ctx:             ctx,
		cfg:             cfg,
		log:             timeline.Log,
		embeddedWebsite: embeddedWebsite,
	}
	newApp.server = server{
		app:      newApp,
		log:      newApp.log.Named("http"),
		frontend: frontend,
	}
	newApp.cancel = func() {
		// cancel the context, so anything relying on it knows to terminate
		cancel()

		// close all open timelines (TODO: Maybe they should be open on the app, not global in the timeline package?)
		shutdownTimelines()

		// stop python server (will wait for it below)
		if err := newApp.mlServer.Process.Kill(); err != nil {
			newApp.log.Error("could not terminate ML server", zap.Error(err))
		}

		// gracefully close the HTTP server (let existing requests finish within a timeout)
		if newApp.server.httpServer != nil {
			// use a different context since the one we have has been canceled
			const shutdownTimeout = 10 * time.Second
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer shutdownCancel()
			_ = newApp.server.httpServer.Shutdown(shutdownCtx)
		}

		// finish waiting for python server to exit
		if state, err := newApp.mlServer.Process.Wait(); err != nil {
			newApp.log.Error("ML server", zap.Error(err), zap.String("state", state.String()))
		}
	}
	newApp.registerCommands()

	appMu.Lock()
	app = newApp
	appMu.Unlock()

	return newApp, nil
}

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

	url := "http://" + a.cfg.listenAddr() + apiBasePath + commandName

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
		resp, err = httpClient.Do(req)
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
func (a *App) Serve() (bool, error) {
	if a.serverRunning() {
		return false, nil
	}
	return true, a.serve()
}

func (a *App) MustServe() error {
	return a.serve()
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

func (a *App) startMLServer() error {
	// TODO: This has to be distributable somehow; maybe embed it into the binary and then write it to an application dir or something
	cmd := exec.Command("uv", "run", "server.py")
	cmd.Dir = "ml"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	a.mlServer = cmd
	return cmd.Start()
}

func (a *App) serve() error {
	// TODO: Eventually this will be configurable, but seems like a good idea to do at first
	if err := installPython(a.ctx); err != nil {
		return fmt.Errorf("setting up ML environment: %w", err)
	}

	if err := a.startMLServer(); err != nil {
		return fmt.Errorf("starting ML server: %w", err)
	}

	if err := a.openRepos(); err != nil {
		return fmt.Errorf("opening previously-opened repositories: %w", err)
	}

	if a.server.adminLn != nil {
		return fmt.Errorf("server already running on %s", a.server.adminLn.Addr())
	}

	adminAddr := a.cfg.listenAddr()
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
	a.server.staticFiles = http.FileServer(http.FS(a.server.frontend))
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
	a.server.httpServer = &http.Server{
		Handler:           a.server,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1024 * 512,
	}

	go func() {
		err := a.server.httpServer.Serve(ln)
		if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
			// normal; the listener or server was deliberately closed
			a.log.Info("stopped server", zap.String("listener", ln.Addr().String()))
		} else if err != nil {
			a.log.Error("server failed", zap.String("listener", ln.Addr().String()), zap.Error(err))
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

func (a *App) openRepos() error {
	// open designated timelines; copy pointer so we don't have to acquire lock on cfg and create deadlock with OpenRepository()
	// TODO: use race detector to verify ^
	lastOpenedRepos := a.cfg.Repositories
	for i, repoDir := range lastOpenedRepos {
		_, err := a.openRepository(a.ctx, repoDir, false)
		if err != nil {
			a.log.Error(fmt.Sprintf("failed to open timeline %d of %d", i+1, len(a.cfg.Repositories)),
				zap.Error(err),
				zap.String("dir", repoDir))
		}
	}

	// persist config so it can be used on restart
	if err := a.cfg.autosave(); err != nil {
		return fmt.Errorf("persisting config file: %w", err)
	}

	return nil
}

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

// The app global instance is used mainly for properly
// shutting down after a signal is received.
var (
	app   *App
	appMu sync.Mutex
)

const lowestErrorStatus = 400

const osWindows = "windows"

const defaultAdminAddr = "127.0.0.1:12002"
