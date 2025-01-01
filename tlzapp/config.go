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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Config describes the server configuration.
// Config values must not be copied (i.e. use pointers).
type Config struct {
	sync.RWMutex `json:"-"`

	// The listen address to bind the socket to.
	Listen string `json:"listen,omitempty"`

	// Serves the website from this folder on disk instead of
	// the embedded file system. This can make local, rapid
	// development easier so you don't have to recompile for
	// every website change. If empty, website assets that are
	// compiled into the binary will be used by default.
	WebsiteDir string `json:"website_dir,omitempty"`

	// The API token to use for Mapbox GL JS and tiles. The
	// user should set this to their own to guarantee
	// availability of the maps.
	MapboxAPIKey string `json:"mapbox_api_key,omitempty"`

	// The folder paths of timeline repositories to open at
	// program start.
	Repositories []string `json:"repositories,omitempty"`

	// Obfuscation is often used for demonstrating the
	// software to mask personal data and details.
	Obfuscation timeline.ObfuscationOptions `json:"obfuscation,omitempty"`

	log *zap.Logger
}

func (cfg *Config) listenAddr() string {
	cfg.RLock()
	defer cfg.RUnlock()
	if envVal := os.Getenv("TLZ_ADMIN_ADDR"); envVal != "" {
		return envVal
	}
	if cfg.Listen != "" {
		return cfg.Listen
	}
	return defaultAdminAddr
}

func (cfg *Config) fillDefaults() {
	cfg.Lock()
	defer cfg.Unlock()
	cfg.Obfuscation.Logger = timeline.Log.Named("faker")
	if cfg.log == nil {
		cfg.log = timeline.Log.Named("config").With(zap.Time("loaded", time.Now()))
	}
}

// autosave persists the config to disk by obtaining a read lock, so it is safe for concurrent use.
func (cfg *Config) autosave() error {
	cfg.RLock()
	defer cfg.RUnlock()
	if err := cfg.unsyncedSave(); err != nil {
		return err
	}
	return nil
}

func (cfg *Config) unsyncedSave() error {
	filename := DefaultConfigFilePath()
	err := os.MkdirAll(filepath.Dir(filename), 0755)
	if err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	cfgFile, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("creating config file: %w", err)
	}
	defer cfgFile.Close()
	enc := json.NewEncoder(cfgFile)
	enc.SetIndent("", "\t")
	if err = enc.Encode(cfg); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if cfg.log != nil {
		cfg.log.Info("saved config file", zap.String("path", filename))
	}
	return nil
}

func (cfg *Config) syncOpenRepos() error {
	// assemble the list of open timelines into a sorted slice
	// so we can update the stored config and reopen these
	// timelines automatically at next start
	open := make([]string, len(openTimelines))
	i := 0
	for _, otl := range openTimelines {
		open[i] = otl.RepoDir
		i++
	}
	sort.StringSlice(open).Sort()

	// only update the config if list of open repos changed;
	// I guess doing so wouldn't hurt but it's not necessary
	cfg.Lock()
	sameOpenTimelines := slices.Equal(open, cfg.Repositories)
	cfg.Repositories = open
	cfg.Unlock()

	if !sameOpenTimelines {
		if err := cfg.autosave(); err != nil {
			return err
		}
	}

	return nil
}

// DefaultConfigFilePath returns the file path where
// configuration is persisted.
func DefaultConfigFilePath() string {
	cfgDir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(cfgDir, "timelinize", "config.json")
	}
	cfgDir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(cfgDir, ".timelinize", "config.json")
	}
	return filepath.Join(".timelinize", "config.json")
}

// DefaultCacheDir returns the file path where
// a local application cache is persisted.
func DefaultCacheDir() string {
	cacheDir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(cacheDir, "timelinize")
	}
	homeDir, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(homeDir, ".timelinize", "cache")
	}
	return filepath.Join(".timelinize", "cache")
}
