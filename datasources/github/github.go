/*
	Timelinize
	Copyright (c) 2024 Sergio Rubio

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

// Package github implements a data source that imports GitHub starred repositories
// exported from the GitHub API.
//
// It expects a JSON file with an array of objects, each object representing a bookmark.
//
// The file should be named as follows:
// - ghstars.json
// - ghstars-<unix-timestamp>.json
// - ghstars-<yyyy-mm-dd>.json
//
// The file format:
//
//	[
//	{
//	  "id": 841044067,
//	  "name": "timelinize",
//	  "html_url": "https://github.com/timelinize/timelinize",
//	  "description": "Store your data from all your accounts and devices in a single cohesive timeline on your own computer",
//	  "created_at": "2024-08-11T13:27:39Z",
//	  "updated_at": "2024-09-03T07:17:29Z",
//	  "pushed_at": "2024-09-02T15:31:59Z",
//	  "stargazers_count": 504,
//	  "language": "Go",
//	  "full_name": "timelinize/timelinize",
//	  "is_template": false,
//	  "topics": [
//	      "archival",
//	      "data-archiving",
//	      "data-import",
//	      "timeline"
//	  ],
//	  "private": false,
//	  "starred_at": "2024-08-12T17:55:48Z"
//	}
//	]
//
// A tool to export starred repos to JSON and the exported format documentation
// can be found at https://github.com/rubiojr/gh-stars-exporter.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Data source name and ID.
type tlzTestKey int

const (
	DataSourceName            = "GitHub"
	DataSourceID              = "github"
	TLZTest        tlzTestKey = iota
)

type Repository struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	HTMLURL         string    `json:"html_url"`
	Description     string    `json:"description"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	PushedAt        time.Time `json:"pushed_at"`
	StargazersCount int       `json:"stargazers_count"`
	Language        string    `json:"language"`
	FullName        string    `json:"full_name"`
	Topics          []string  `json:"topics"`
	IsTemplate      bool      `json:"is_template"`
	Private         bool      `json:"private"`
	StarredAt       time.Time `json:"starred_at"`
}

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            DataSourceID,
		Title:           DataSourceName,
		Icon:            "github.svg",
		NewFileImporter: func() timeline.FileImporter { return new(GitHub) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// GitHub interacts with the file system to get items.
type GitHub struct{}

// Recognize returns whether the input file is recognized.
func (GitHub) Recognize(_ context.Context, filenames []string) (timeline.Recognition, error) {
	isoDateRegexp := regexp.MustCompile(`^ghstars(-(\d{4}-\d{2}-\d{2}|[0-9]{10}))?.json$`)
	for _, filename := range filenames {
		// ghstars.json or ghstars-YYYY-MM-DD.json or ghstars-UNIX_TIMESTAMP.json
		if isoDateRegexp.MatchString(filepath.Base(filename)) {
			return timeline.Recognition{Confidence: 1}, nil
		}
	}

	return timeline.Recognition{}, nil
}

// FileImport conducts an import of the data using this data source.
func (c *GitHub) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, _ timeline.ListingOptions) error {
	for _, filename := range filenames {
		err := c.process(ctx, filename, itemChan)
		if err != nil {
			return fmt.Errorf("processing %s: %w", filename, err)
		}
	}
	return nil
}

// process reads the JSON file and sends the items to the channel.
func (c *GitHub) process(ctx context.Context, path string, itemChan chan<- *timeline.Graph) error {
	j, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var repos []*Repository
	err = json.Unmarshal(j, &repos)
	if err != nil {
		return fmt.Errorf("malformed JSON: %w", err)
	}

	for _, repo := range repos {
		// StarredAt is a required field.
		if repo.StarredAt.IsZero() {
			return fmt.Errorf("missing starred_at field for repo %s", repo.FullName)
		}

		// HTMLURL is a required field.
		if repo.HTMLURL == "" {
			return fmt.Errorf("missing HTMLURL field for repo %s", repo.FullName)
		}

		select {
		// Callers can cancel the import.
		case <-ctx.Done():
			return ctx.Err()
		default:
			item := &timeline.Item{
				Classification:       timeline.ClassBookmark,
				Timestamp:            repo.StarredAt,
				IntermediateLocation: path,
				Content: timeline.ItemData{
					Filename:  filepath.Base(path),
					Data:      timeline.StringData(repo.HTMLURL),
					MediaType: "text/plain",
				},
				Metadata: timeline.Metadata{
					"ID":          repo.ID,
					"Name":        repo.Name,
					"Full name":   repo.FullName,
					"URL":         repo.HTMLURL,
					"Description": repo.Description,
					"Created at":  repo.CreatedAt,
					"Stargazers":  repo.StargazersCount,
					"Topics":      repo.Topics,
					"Language":    repo.Language,
					"Private":     repo.Private,
					"Starred at":  repo.StarredAt,
				},
			}

			itemChan <- &timeline.Graph{Item: item}
		}
	}

	return nil
}
