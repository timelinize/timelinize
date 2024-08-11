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
	"path/filepath"
	"strings"
)

// getFileSelectorRoots gets the platform-specific filepath roots,
// like drive letters for Windows, the user's home folder for
// all, or mounted drives for Mac/Linux.
func getFileSelectorRoots() ([]fileSelectorRoot, error) {
	var roots []fileSelectorRoot

	// add user's home folder
	if home := userHomeDir(); home != "" && home != "." {
		homedirName := filepath.Base(home)
		if homedirName == "" {
			homedirName = "Home"
		}
		roots = append(roots, fileSelectorRoot{Label: homedirName, Path: home, Type: "home"})
	}

	// add file system root
	roots = append(roots, fileSelectorRoot{Label: "Macintosh HD", Path: "/", Type: "root"})

	// add any mounted folders in the /Volumes directory
	vols, err := os.ReadDir("/Volumes")
	if err == nil {
		for _, vol := range vols {
			info, err := vol.Info()
			if err != nil {
				continue
			}
			// skip symlinks ("Macintosh HD" appears here as a symlink)
			if info.Mode()&os.ModeSymlink > 0 {
				continue
			}
			volPath := filepath.Join("/Volumes", vol.Name())
			roots = append(roots, fileSelectorRoot{Label: vol.Name(), Path: volPath, Type: "mount"})
		}
	}

	return roots, nil
}

func fileHidden(filename string) bool {
	return strings.HasPrefix(filename, ".")
}
