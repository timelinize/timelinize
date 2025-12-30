package geocaching

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "geocaching",
		Title:           "Geocaching GPX",
		Icon:            "geocaching.png",
		Description:     "Groundspeak My Finds Pocket Query (.gpx)",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the Geocaching data source.
type Options struct {
	// The ID of the owner entity. REQUIRED for linking entity in DB.
	OwnerEntityID uint64 `json:"owner_entity_id"`
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

// Recognize returns whether the file is a Groundspeak GPX export.
func (FileImporter) Recognize(ctx context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: 0.9}

	if dirEntry.IsDir() {
		return rec, nil
	}

	if strings.ToLower(path.Ext(dirEntry.Name())) != ".gpx" {
		return rec, nil
	}

	// Peek a small portion of the file to see if it advertises Groundspeak/Geocache keywords.
	f, err := dirEntry.FS.Open(dirEntry.Filename)
	if err != nil {
		return rec, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	snippet := strings.ToLower(string(buf[:n]))
	if strings.Contains(snippet, "groundspeak") || strings.Contains(snippet, "geocache") {
		// Outrank the generic GPX importer (which also returns 1 for .gpx files)
		rec.Confidence = 0.9
	}

	return rec, nil
}

// FileImport imports data from a Groundspeak GPX file or folder of files.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

	return fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		if strings.ToLower(path.Ext(d.Name())) != ".gpx" {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		gpxDoc, err := decodeGPX(file)
		if err != nil {
			return fmt.Errorf("decode GPX %s: %w", fpath, err)
		}

		for _, w := range gpxDoc.Waypoints {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			graph := waypointToGraph(w, owner)
			if graph != nil {
				params.Pipeline <- graph
			}
		}

		return nil
	})
}

type gpxFile struct {
	XMLName   xml.Name `xml:"gpx"`
	Name      string   `xml:"name"`
	Desc      string   `xml:"desc"`
	Author    string   `xml:"author"`
	Email     string   `xml:"email"`
	Time      string   `xml:"time"`
	Keywords  string   `xml:"keywords"`
	Waypoints []wpt    `xml:"wpt"`
}

type wpt struct {
	Lat     float64 `xml:"lat,attr"`
	Lon     float64 `xml:"lon,attr"`
	Time    string  `xml:"time"`
	Name    string  `xml:"name"`
	Desc    string  `xml:"desc"`
	URL     string  `xml:"url"`
	URLName string  `xml:"urlname"`
	Sym     string  `xml:"sym"`
	Type    string  `xml:"type"`

	Cache groundspeakCache `xml:"http://www.groundspeak.com/cache/1/0/1 cache"`
}

type groundspeakCache struct {
	ID        string `xml:"id,attr"`
	Archived  bool   `xml:"archived,attr"`
	Available bool   `xml:"available,attr"`

	Name       string   `xml:"http://www.groundspeak.com/cache/1/0/1 name"`
	PlacedBy   string   `xml:"http://www.groundspeak.com/cache/1/0/1 placed_by"`
	Owner      gsOwner  `xml:"http://www.groundspeak.com/cache/1/0/1 owner"`
	Type       string   `xml:"http://www.groundspeak.com/cache/1/0/1 type"`
	Container  string   `xml:"http://www.groundspeak.com/cache/1/0/1 container"`
	Attributes []gsAttr `xml:"http://www.groundspeak.com/cache/1/0/1 attributes>attribute"`
	Difficulty float64  `xml:"http://www.groundspeak.com/cache/1/0/1 difficulty"`
	Terrain    float64  `xml:"http://www.groundspeak.com/cache/1/0/1 terrain"`
	Country    string   `xml:"http://www.groundspeak.com/cache/1/0/1 country"`
	State      string   `xml:"http://www.groundspeak.com/cache/1/0/1 state"`
	ShortDesc  gsText   `xml:"http://www.groundspeak.com/cache/1/0/1 short_description"`
	LongDesc   gsText   `xml:"http://www.groundspeak.com/cache/1/0/1 long_description"`
	Hints      string   `xml:"http://www.groundspeak.com/cache/1/0/1 encoded_hints"`
	Logs       []gsLog  `xml:"http://www.groundspeak.com/cache/1/0/1 logs>log"`
}

type gsOwner struct {
	ID   string `xml:"id,attr"`
	Name string `xml:",chardata"`
}

type gsAttr struct {
	ID    string `xml:"id,attr"`
	Inc   string `xml:"inc,attr"`
	Label string `xml:",chardata"`
}

type gsText struct {
	HTML bool   `xml:"html,attr"`
	Text string `xml:",chardata"`
}

type gsLog struct {
	ID     string    `xml:"id,attr"`
	Date   string    `xml:"date"`
	Type   string    `xml:"type"`
	Finder gsOwner   `xml:"finder"`
	Text   gsLogText `xml:"text"`
}

type gsLogText struct {
	Encoded bool   `xml:"encoded,attr"`
	Body    string `xml:",chardata"`
}

func decodeGPX(r io.Reader) (*gpxFile, error) {
	dec := xml.NewDecoder(r)
	var g gpxFile
	if err := dec.Decode(&g); err != nil {
		return nil, err
	}
	return &g, nil
}

func waypointToGraph(w wpt, owner timeline.Entity) *timeline.Graph {
	ts := parseTime(w.Time)

	location := timeline.Location{
		Latitude:  &w.Lat,
		Longitude: &w.Lon,
	}

	meta := timeline.Metadata{
		"Geocache code": w.Name,
	}
	if w.URL != "" {
		meta["URL"] = w.URL
	}
	if w.URLName != "" {
		meta["URL name"] = w.URLName
	}
	if w.Sym != "" {
		meta["Symbol"] = w.Sym
	}
	if w.Type != "" {
		meta["Type"] = w.Type
	}

	c := w.Cache
	if c.Name != "" {
		meta["Cache name"] = c.Name
	}
	if c.Type != "" {
		meta["Cache type"] = c.Type
	}
	if c.Container != "" {
		meta["Container"] = c.Container
	}
	if c.Difficulty != 0 {
		meta["Difficulty"] = c.Difficulty
	}
	if c.Terrain != 0 {
		meta["Terrain"] = c.Terrain
	}
	meta["Archived"] = c.Archived
	meta["Available"] = c.Available
	if c.Country != "" {
		meta["Country"] = c.Country
	}
	if c.State != "" {
		meta["State"] = c.State
	}
	if c.PlacedBy != "" {
		meta["Placed by"] = c.PlacedBy
	}
	if c.Owner.Name != "" {
		meta["Owner name"] = c.Owner.Name
	}
	if c.Owner.ID != "" {
		meta["Owner ID"] = c.Owner.ID
	}
	if c.ShortDesc.Text != "" {
		meta["Short description"] = c.ShortDesc.Text
	}
	if c.LongDesc.Text != "" {
		meta["Long description"] = c.LongDesc.Text
	}
	if c.Hints != "" {
		meta["Hint"] = c.Hints
	}

	if len(c.Attributes) > 0 {
		attrs := make([]string, 0, len(c.Attributes))
		for _, a := range c.Attributes {
			attrs = append(attrs, a.Label)
		}
		meta["Attributes"] = attrs
	}

	var latestLog *gsLog
	if len(c.Logs) > 0 {
		logs := make([]map[string]any, 0, len(c.Logs))
		for i := range c.Logs {
			l := &c.Logs[i]
			logs = append(logs, map[string]any{
				"ID":        l.ID,
				"Date":      l.Date,
				"Type":      l.Type,
				"Finder ID": l.Finder.ID,
				"Finder":    l.Finder.Name,
				"Text":      l.Text.Body,
			})
			if latestLog == nil || parseTime(l.Date).After(parseTime(latestLog.Date)) {
				latestLog = l
			}
			if ts.IsZero() {
				ts = parseTime(l.Date)
			}
		}
		meta["Logs"] = logs
	}

	// Choose visible content
	cacheName := c.Name
	if cacheName == "" {
		cacheName = w.URLName
	}
	if cacheName == "" {
		cacheName = w.Name
	}
	// Visible content: concise header + latest log text if available
	header := fmt.Sprintf("%s (%s) D%.1f/T%.1f", cacheName, w.Name, c.Difficulty, c.Terrain)
	contentText := header
	if latestLog != nil && latestLog.Text.Body != "" {
		contentText = header + "\n\n" + latestLog.Text.Body
	}

	item := &timeline.Item{
		ID:             w.Name,
		Classification: timeline.ClassLocation,
		Timestamp:      ts,
		Location:       location,
		Owner:          owner,
		Metadata:       meta,
		Content: timeline.ItemData{
			Data: timeline.StringData(contentText),
		},
	}

	graph := &timeline.Graph{Item: item}
	graph.ToEntity(timeline.RelVisit, makeCacheEntity(w, c))

	// Surface latest log in metadata for quick UI access
	if latestLog != nil && meta != nil {
		meta["Latest log by"] = latestLog.Finder.Name
		meta["Latest log type"] = latestLog.Type
		meta["Latest log text"] = latestLog.Text.Body
		meta["Latest log date"] = latestLog.Date
	}

	// Attach cache owner as an entity for browsing caches by owner
	if c.Owner.Name != "" {
		graph.ToEntity(timeline.RelIncludes, makeOwnerEntity(c.Owner))
	}

	return graph
}

func makeOwnerEntity(owner gsOwner) *timeline.Entity {
	return &timeline.Entity{
		Type: timeline.EntityPerson,
		Name: owner.Name,
		Attributes: []timeline.Attribute{
			{
				Name:     "geocaching_username",
				Value:    owner.Name,
				Identity: true,
				Metadata: timeline.Metadata{
					"Geocaching owner ID": owner.ID,
				},
			},
		},
		Metadata: timeline.Metadata{
			"Geocaching owner ID": owner.ID,
		},
	}
}

func makeCacheEntity(w wpt, c groundspeakCache) *timeline.Entity {
	name := c.Name
	if name == "" {
		name = w.URLName
	}
	if name == "" {
		name = w.Name
	}

	lat := w.Lat
	lon := w.Lon

	ent := &timeline.Entity{
		Type: timeline.EntityPlace,
		Name: name,
		Attributes: []timeline.Attribute{
			{
				Name:      "coordinate",
				Latitude:  &lat,
				Longitude: &lon,
				Identity:  true,
				Metadata: timeline.Metadata{
					"Geocache code": w.Name,
					"URL":           w.URL,
				},
			},
		},
	}

	if c.Type != "" {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "geocache_type",
			Value: c.Type,
		})
	}
	if c.Container != "" {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "container",
			Value: c.Container,
		})
	}
	if c.Difficulty != 0 {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "difficulty",
			Value: c.Difficulty,
		})
	}
	if c.Terrain != 0 {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "terrain",
			Value: c.Terrain,
		})
	}
	return ent
}

func parseTime(val string) time.Time {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, val); err == nil {
			return t
		}
	}
	return time.Time{}
}
