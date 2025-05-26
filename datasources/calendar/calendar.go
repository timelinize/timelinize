package calendar

import (
	"context"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/apognu/gocal"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "calendar",
		Title:           "Calendar",
		Icon:            "calendar.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

// Recognize returns whether the file is recognized for this data source.
func (fi FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: 0.9}

	// TODO: proper detection, not just filename
	ext := strings.ToLower(path.Ext(dirEntry.Name()))
	if ext == extIcs {
		rec.Confidence = 1
	}

	return rec, nil
}

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED for linking
	// entity in DB when calendar provides no/insufficient
	// owner information.
	OwnerEntityID uint64 `json:"owner_entity_id"`
}

// FileImport imports data from a file.
func (fi FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	// if no organizer/owner is specified, we default to the configured entity
	defaultOwner := timeline.Entity{
		ID: dsOpt.OwnerEntityID,
	}

	err := fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			// skip hidden files & folders
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // traverse into subdirectories
		}

		// skip unsupported file types
		ext := path.Ext(strings.ToLower(d.Name()))
		if ext != extIcs {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		c := gocal.NewParser(file)

		// import all items (with reasonable bounds still on recurring events)
		// regardless of date range (set Start/End to configure date range)
		c.SkipBounds = true

		if err := c.Parse(); err != nil {
			// go on to the next calendar file
			params.Log.Error("parsing calendar file",
				zap.String("filename", fpath),
				zap.Error(err))
			return nil
		}

		for _, e := range c.Events {
			var start, end time.Time
			if e.Start != nil {
				start = *e.Start
			}
			if e.End != nil {
				end = *e.End
			}

			var loc timeline.Location
			if e.Geo != nil {
				loc.Latitude = &e.Geo.Lat
				loc.Longitude = &e.Geo.Long
			}

			var owner timeline.Entity
			if e.Organizer == nil {
				owner = defaultOwner
			} else if e.Organizer != nil && e.Organizer.Cn != "" {
				owner.Name = e.Organizer.Cn
			}

			content := strings.TrimSpace(e.Summary)
			if e.Description != "" {
				if content != "" {
					content += "\n"
				}
				content += strings.TrimSpace(e.Description)
			}

			params.Pipeline <- &timeline.Graph{
				Item: &timeline.Item{
					ID:             e.Uid,
					Classification: timeline.ClassEvent,
					Timestamp:      start,
					Timespan:       end,
					Location:       loc,
					Owner:          owner,
					Content: timeline.ItemData{
						MediaType: "text/plain",
						Data:      timeline.StringData(content),
					},
					Metadata: timeline.Metadata{
						"Location": e.Location,
						"Class":    e.Class,
					},
				},
			}
		}

		return nil
	})

	return err
}

const extIcs = ".ics"
