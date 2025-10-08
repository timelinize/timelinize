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
	"os"
	"os/signal"
	"sync/atomic"

	"github.com/cshum/vipsgen/vips"
	"github.com/timelinize/timelinize/timeline"
)

// TrapSignals create signal handlers for all applicable signals for this system.
func TrapSignals() {
	trapSignalsCrossPlatform()
	trapSignalsPosix()
}

// trapSignalsCrossPlatform captures SIGINT, which triggers forceful
// shutdown that cleans up any network or local resources. A second
// interrupt signal will exit the process immediately.
func trapSignalsCrossPlatform() {
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)

		for i := 0; true; i++ {
			<-sig

			if i > 0 {
				timeline.Log.Fatal("SIGINT: force quit")
				os.Exit(2) //nolint:mnd
			}

			timeline.Log.Warn("SIGINT: shutting down")
			go shutdown(1)
		}
	}()
}

// Shutdown shuts down this client process. It is a no-op
// if the process is already exiting.
func shutdown(exitCode int) {
	if !atomic.CompareAndSwapInt32(shuttingDown, 0, 1) {
		return
	}

	shutdownTimelines()
	vips.Shutdown()

	appMu.Lock()
	if app != nil {
		app.cancel()
	}
	appMu.Unlock()

	_ = timeline.Log.Sync()

	os.Exit(exitCode)
}

// shuttingDown is an atomic value which will be set
// to 1 when the program is shutting down.
var shuttingDown = new(int32)
