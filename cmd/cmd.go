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

// Package tlcmd facilitates the command line interface (CLI)
// and implements the main().
package tlcmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/timelinize/timelinize/timeline"
	"github.com/timelinize/timelinize/tlzapp"
	"go.uber.org/zap"
)

func Main(embeddedWebsite fs.FS) {
	cfg, err := loadConfigFile()
	if err != nil {
		timeline.Log.Fatal("failed loading config", zap.Error(err))
	}

	ctx := context.Background()

	app, err := tlzapp.New(ctx, cfg, embeddedWebsite)
	if err != nil {
		timeline.Log.Fatal("failed to run application", zap.Error(err))
	}

	flag.Parse()

	// implement standard (CLI-only) flags
	subCommand, subCommandFunc := getStandardSubcommand(app)
	if subCommandFunc != nil {
		if err := checkFlagParsing(); err != nil {
			timeline.Log.Fatal("possible syntax error detected", zap.Error(err))
		}
		if err := subCommandFunc(); err != nil {
			timeline.Log.Fatal("subcommand failed",
				zap.String("subcommand", subCommand),
				zap.Error(err))
		}
		return
	}

	// check for registered endpoint (API command)
	if remaining := flag.Args(); len(remaining) > 0 {
		if err := app.RunCommand(ctx, remaining); err != nil {
			timeline.Log.Fatal("subcommand failed", zap.Error(err))
		}
		return
	}

	// start the application server
	// TODO: Use a host like tlz.localhost to serve HTTP/2 over HTTPS... just need to automate the CA and cert... - or maybe a public domain like timelinize.app or timelinize.run or something
	startedServer, err := app.Serve()
	if err != nil {
		timeline.Log.Fatal("could not start server", zap.Error(err))
	}

	// once the server is running, open GUI in web browser
	if err := openWebBrowser("http://127.0.0.1:12002"); err != nil {
		timeline.Log.Error("could not open web browser", zap.Error(err))
	}

	if startedServer {
		select {}
	}
}

// openWebBrowser opens the web browser to loc, which must be a
// fully-qualified URL including a trailing slash even if there
// is no path (e.g. "http://host/" not "http://host"); if the
// trailing slash is not present, it will be appended.
func openWebBrowser(loc string) error {
	osCommand := map[string][]string{
		"darwin":  {"open"},
		"freebsd": {"xdg-open"},
		"linux":   {"xdg-open"}, // requires xdg-utils; TODO: also try sensible-browser, or gnome-open
		"netbsd":  {"xdg-open"},
		"openbsd": {"xdg-open"},
		"windows": {"cmd", "/c", "start"},
	}

	// ensure URL is valid and path ends with a trailing slash
	u, err := url.Parse(loc)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	loc = u.String()

	if runtime.GOOS == "windows" {
		// escape characters not allowed by cmd
		loc = strings.ReplaceAll(loc, "&", `^&`)
	}

	all := append(osCommand[runtime.GOOS], loc) //nolint:gocritic
	exe := all[0]
	args := all[1:]

	timeline.Log.Info("opening web browser to application",
		zap.Strings("command", append([]string{exe}, args...)))

	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Gets CLI-only commands.
func getStandardSubcommand(app *tlzapp.App) (string, func() error) {
	standardCommands := map[string]func() error{
		"serve": func() error {
			if err := app.MustServe(); err != nil {
				return err
			}
			select {}
		},
		"help": func() error { //nolint:unparam // bug filed: https://github.com/mvdan/unparam/issues/82
			fmt.Println(app.CommandLineHelp())
			return nil
		},
		"version": func() error {
			fmt.Println("TODO: print version")
			return nil
		},
	}

	if len(flag.Args()) > 0 {
		subCommand := flag.Arg(0)
		subCommandFunc, ok := standardCommands[subCommand]
		if ok {
			return subCommand, subCommandFunc
		}
	}
	return "", nil
}

// checkFlagParsing returns an error if it looks like the
// program may have been invoked with the flags in the
// wrong place. This should NOT be used when the program is
// invoked as an API client and the flags are arbitrary and
// "parsed" by the apicli package. This package intends to
// catch errors like running the program as:
// `relica daemon -config config_dev.toml`
// where it actually needs to be run as:
// `relica -config config_dev.toml daemon`
// in order to set the config variable properly. Failing to
// catch this error could result in a misconfiguration
// and undesirable results. Only for use when a standard
// command (something that we recognize, not as part of the
// API) is present.
func checkFlagParsing() error {
	if len(os.Args) > 2 && flag.NFlag() == 0 {
		return errors.New("it looks like you intended to specify flags, but none were parsed; make sure flags go before positional arguments")
	}
	return nil
}

func loadConfigFile() (*tlzapp.Config, error) {
	cfgBytes, err := os.ReadFile(configFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if configFile == tlzapp.DefaultConfigFilePath() {
				err = nil
			}
			return new(tlzapp.Config), err
		}
	}
	var cfg *tlzapp.Config
	err = json.Unmarshal(cfgBytes, &cfg)
	return cfg, err
}

var configFile = tlzapp.DefaultConfigFilePath()
