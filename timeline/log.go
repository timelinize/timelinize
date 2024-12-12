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
	"errors"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Log is the main process log. All named logs should be derivatives of
// this logger. All log emissions should be sent through this logger or
// one of its derivatives.
var Log = newLogger()

// newLogger returns a logger that writes to websocketLogOutputs
// and the console, with JSON and console encoders, respectively.
// It is intended for setting up the main process logger during
// the program's init phase.
func newLogger() *zap.Logger {
	websocketsSync := zapcore.AddSync(websocketLogOutputs)

	websocketsOut := zapcore.Lock(websocketsSync)
	consoleOut := zapcore.Lock(os.Stderr)

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = func(ts time.Time, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(ts.UTC().Format("2006/01/02 15:04:05.000"))
	}
	encCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	consoleEncoder := zapcore.NewConsoleEncoder(encCfg)
	jsonEncoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())

	core := zapcore.NewTee(
		zapcore.NewCore(consoleEncoder, consoleOut, zap.DebugLevel), // TODO: keep at debug? make this optional?
		zapcore.NewCore(jsonEncoder, websocketsOut, zap.InfoLevel),  // sent to web frontend / UI
	)

	// avoid a firehose of logs
	const firstNMsgs, everyNthMsg = 10, 100
	core = zapcore.NewSamplerWithOptions(core, time.Second, firstNMsgs, everyNthMsg)

	return zap.New(&customCore{core})
}

// multiConnWriter is like io.multiWriter from the standard lib,
// except this supports dynamically adding and removing writers
// and is specifically for WebSocket connections and Wails
// application events.
//
// This is a "best-effort" multi-writer. If there is an error writing
// to one conn, it does not abort and will continue to write to the
// other conns. Write errors are discarded, but write errors that are
// specifically closed connections will result in that connection
// being removed from the pool.
type multiConnWriter struct {
	// appCtx  context.Context // must be a Wails application context
	conns   []*websocket.Conn
	connsMu sync.RWMutex // also protects appCtx
}

func (mw *multiConnWriter) Write(p []byte) (n int, err error) {
	mw.connsMu.RLock()
	// if mw.appCtx != nil {
	// 	runtime.EventsEmit(mw.appCtx, "log", string(p))
	// }
	for _, w := range mw.conns {
		err = w.WriteMessage(websocket.TextMessage, p)
		// the handler that added this connection to the pool should
		// have removed it when it was closed, but just in case we
		// find out first that it was closed, we can remove it now
		if errors.Is(err, websocket.ErrCloseSent) {
			defer mw.RemoveConn(w)
		}
	}
	mw.connsMu.RUnlock()
	return len(p), err
}

// AddConn subscribes conn to writes.
func (mw *multiConnWriter) AddConn(conn *websocket.Conn) {
	mw.connsMu.Lock()
	mw.conns = append(mw.conns, conn)
	mw.connsMu.Unlock()
}

// RemoveConn unsubscribes conn from writes, if it is subscribed.
func (mw *multiConnWriter) RemoveConn(conn *websocket.Conn) {
	mw.connsMu.Lock()
	for i, mww := range mw.conns {
		if mww == conn {
			mw.conns = append(mw.conns[:i], mw.conns[i+1:]...)
			break
		}
	}
	mw.connsMu.Unlock()
}

// websocketLogOutputs mediates the list of active
// websocket connections that are receiving process
// logs.
var websocketLogOutputs = new(multiConnWriter)

// AddLogConn subscribes conn to the log output. When
// the conn is closed, it should be removed with
// RemoveLogConn().
func AddLogConn(conn *websocket.Conn) {
	websocketLogOutputs.AddConn(conn)
}

// RemoveLogConn removes conn from receiving logs.
// It is idempotent.
func RemoveLogConn(conn *websocket.Conn) {
	websocketLogOutputs.RemoveConn(conn)
}

// customCore wraps another zapcore.Core and prevents sampling based on logger name.
type customCore struct {
	zapcore.Core
}

func (c *customCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if ent.LoggerName == "job.status" {
		// always allow through, no sampling -- otherwise UI gets out of sync
		return ce.AddCore(ent, c)
	}
	return c.Core.Check(ent, ce)
}
