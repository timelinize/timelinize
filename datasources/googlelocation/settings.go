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

package googlelocation

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func loadSettingsFromTakeoutArchive(d timeline.DirEntry) (settings, error) {
	var file fs.File
	var err error
	for _, pathToTry := range []string{
		takeoutLocationHistoryPath2024,
		takeoutLocationHistoryPathPre2024,
	} {
		file, err = flexibleOpen(d, path.Join(pathToTry, "Settings.json"))
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	if err != nil {
		return settings{}, err
	}
	defer file.Close()

	var settings settings
	err = json.NewDecoder(file).Decode(&settings)
	return settings, err
}

type settings struct {
	CreatedTime          time.Time        `json:"createdTime"`
	ModifiedTime         time.Time        `json:"modifiedTime"`
	HistoryEnabled       bool             `json:"historyEnabled"`
	HistoryDeletionTime  time.Time        `json:"historyDeletionTime"`
	DeviceSettings       []deviceSettings `json:"deviceSettings"`
	RetentionWindowDays  int64            `json:"retentionWindowDays"`
	HasReportedLocations bool             `json:"hasReportedLocations"`
	HasSetRetention      bool             `json:"hasSetRetention"`
}

type deviceSettings struct {
	DeviceTag                            int64     `json:"deviceTag"`
	ReportingEnabled                     bool      `json:"reportingEnabled"`
	DevicePrettyName                     string    `json:"devicePrettyName"`
	PlatformType                         string    `json:"platformType"`
	DeviceCreationTime                   time.Time `json:"deviceCreationTime"`
	LatestLocationReportingSettingChange struct {
		ReportingEnabledModificationTime time.Time `json:"reportingEnabledModificationTime"`
	} `json:"latestLocationReportingSettingChange"`
	AndroidOSLevel int `json:"androidOsLevel"`
	DeviceSpec     struct {
		Manufacturer string `json:"manufacturer"`
		Brand        string `json:"brand"`
		Product      string `json:"product"`
		Device       string `json:"device"`
		Model        string `json:"model"`
		IsLowRAM     bool   `json:"isLowRam"`
	} `json:"deviceSpec"`
}
