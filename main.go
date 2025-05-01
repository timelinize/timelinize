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

package main

import (
	"embed"

	tlcmd "github.com/timelinize/timelinize/cmd"
	// plug in data sources
	_ "github.com/timelinize/timelinize/datasources/applecontacts"
	_ "github.com/timelinize/timelinize/datasources/beeper"
	_ "github.com/timelinize/timelinize/datasources/calendar"
	_ "github.com/timelinize/timelinize/datasources/contactlist"
	_ "github.com/timelinize/timelinize/datasources/email"
	_ "github.com/timelinize/timelinize/datasources/facebook"
	_ "github.com/timelinize/timelinize/datasources/firefox"
	_ "github.com/timelinize/timelinize/datasources/generic"
	_ "github.com/timelinize/timelinize/datasources/geojson"
	_ "github.com/timelinize/timelinize/datasources/github"
	_ "github.com/timelinize/timelinize/datasources/googlelocation"
	_ "github.com/timelinize/timelinize/datasources/googlephotos"
	_ "github.com/timelinize/timelinize/datasources/gpx"
	_ "github.com/timelinize/timelinize/datasources/icloud"
	_ "github.com/timelinize/timelinize/datasources/instagram"
	_ "github.com/timelinize/timelinize/datasources/iphone"
	_ "github.com/timelinize/timelinize/datasources/kmlgx"
	_ "github.com/timelinize/timelinize/datasources/media"
	_ "github.com/timelinize/timelinize/datasources/nmea"
	_ "github.com/timelinize/timelinize/datasources/smsbackuprestore"
	_ "github.com/timelinize/timelinize/datasources/strava"
	_ "github.com/timelinize/timelinize/datasources/telegram"
	_ "github.com/timelinize/timelinize/datasources/twitter"
	_ "github.com/timelinize/timelinize/datasources/vcard"
)

// Package main is the entry point of the application.
func main() {
	tlcmd.Main(embeddedWebsite)
}

// The frontend assets. This will always be embedded into the binary
// even if the config or command line flag says to use a folder on
// disk (usually for dev).
//
// This is only defined here because goembed can't embed something
// from a parent directory, and I just don't care to have the folder
// nested within another folder right now.
//
//go:embed all:frontend
var embeddedWebsite embed.FS
