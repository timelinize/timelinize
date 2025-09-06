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
	"embed"
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
	"path/filepath"
	"strconv"
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

	server server

	pyServer   *exec.Cmd
	pyServerMu *sync.Mutex

	// references to embedded assets... due to limitations
	// in the go embed tool, the vars have to be in a parent
	// directory of what is being embedded, so we pass in
	// reference to it for the app, since this package isn't
	// in the root of the project, and also to pass along to
	// a new app if the config is changed and the app is
	// restarted
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
		pyServerMu:      new(sync.Mutex),
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

		newApp.pyServerMu.Lock()
		defer newApp.pyServerMu.Unlock()

		// stop python server (will wait for it below)
		if newApp.pyServer != nil && newApp.pyServer.Process != nil {
			if err := newApp.pyServer.Process.Kill(); err != nil {
				newApp.log.Error("could not terminate Python server", zap.Error(err))
			}
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
		if newApp.pyServer != nil && newApp.pyServer.Process != nil {
			if state, err := newApp.pyServer.Process.Wait(); err != nil {
				newApp.log.Error("Python server", zap.Error(err), zap.String("state", state.String()))
			}
		}

		newApp.pyServer = nil
	}
	newApp.registerCommands()

	appMu.Lock()
	app = newApp
	appMu.Unlock()

	return newApp, nil
}

func (a *App) Shutdown() { a.cancel() }

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

func (a *App) startPythonServer(host string, port int) error {
	// only start the python server if it isn't already running
	a.pyServerMu.Lock()
	serverRunning := a.pyServer != nil
	a.pyServerMu.Unlock()
	if serverRunning {
		a.log.Debug("skipping starting python server since it is already non-nil, so should be running")
		return nil
	}

	// nothing to do if uv isn't installed
	if _, err := exec.LookPath("uv"); err != nil {
		a.log.Warn("uv is not installed in PATH; semantic features will be unavailable (to fix: install uv into PATH, then restart app)", zap.Error(err))
		return nil
	}

	// the subfolder in the embedded directory wherein the Python project lives;
	// the contents of this folder are dumped to disk, and may replace what is
	// already in that folder on disk (to accommodate changes/updates); the
	// virtual env must be in a different folder (via UV_PROJECT_ENVIRONMENT)
	const scriptSubfolder = "server"

	// get the name of the embedded folder
	entries, err := fs.ReadDir(embeddedPython, ".")
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return errors.New("embedded python assets have unexpected structure; semantic features will be unavailable")
	}
	embedFolderName := entries[0].Name()

	// embedded assets need to be written to disk so that the python environment
	// can operate: it needs to know the uv project manifest, the dependencies,
	// and a place to put the virtualenv (venv). By default, uv would put the
	// .venv folder in the project directory, but that would make updating the
	// scripts tricky, since we couldn't just delete the whole thing and re-write
	// the files (we don't want to delete the venv, it's usually a big cache); so
	// we need to put the files we embed into a separate subfolder from the venv;
	// we can do this easily with an environment variable to relocate the venv,
	// and have the scripts/project in their own subfolder, in the app data dir.
	dataDir := AppDataDir()
	projectPath := filepath.Join(dataDir, embedFolderName, scriptSubfolder)
	venvPath := filepath.Join(dataDir, embedFolderName, "venv")

	// clear out old project/script files (this ensures they stay current)
	// then write the embedded files in their place
	if err = os.RemoveAll(projectPath); err != nil {
		return nil
	}
	if err := os.CopyFS(dataDir, embeddedPython); err != nil {
		return err
	}

	a.log.Info("starting python server to enable semantic features", zap.String("dir", projectPath))

	// run the python server such that the venv is relocated to its own
	// folder outside the project folder (but still in the app data dir)
	//nolint:gosec
	cmd := exec.CommandContext(a.ctx,
		"uv", "run", "server.py",
		"--host", host,
		"--port", strconv.Itoa(port))
	cmd.Dir = projectPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "UV_PROJECT_ENVIRONMENT="+venvPath)
	a.pyServerMu.Lock()
	a.pyServer = cmd
	a.pyServerMu.Unlock()
	return cmd.Start() // TODO: need to be sure this stops when our program stops
}

func (a *App) stopPythonServer() error {
	a.pyServerMu.Lock()
	defer a.pyServerMu.Unlock()
	if a.pyServer == nil {
		return nil
	}
	if a.pyServer != nil && a.pyServer.Process != nil {
		return a.pyServer.Process.Kill()
	}
	return nil
}

func (a *App) serve() error {
	if err := a.openRepos(); err != nil {
		return fmt.Errorf("opening previously-opened repositories: %w", err)
	}

	if a.server.adminLn != nil {
		return fmt.Errorf("server already running on %s", a.server.adminLn.Addr())
	}

	adminAddr := a.cfg.listenAddr()
	a.server.fillAllowedOrigins(a.cfg.AllowedOrigins, adminAddr) // for CORS enforcement

	ln, err := new(net.ListenConfig).Listen(a.ctx, "tcp", adminAddr)
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

//go:embed python
var embeddedPython embed.FS
