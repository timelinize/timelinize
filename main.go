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
// nested within something else. We can move things around later
// though.
//
//go:embed all:frontend
var embeddedWebsite embed.FS
