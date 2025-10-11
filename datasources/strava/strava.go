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

// Package strava implements a data source for Strava account data.
package strava

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/datasources/gpx"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "strava",
		Title:           "Strava",
		Icon:            "strava.svg",
		Description:     "A Strava account export.",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the data source.
type Options struct {
	// Options specific to the location processor.
	googlelocation.LocationProcessingOptions
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct {
}

// Recognize returns whether the input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	for _, expectedFile := range []string{
		"activities.csv",
		"profile.csv",
	} {
		if _, err := dirEntry.Stat(expectedFile); err == nil {
			continue
		}
		return timeline.Recognition{}, nil
	}

	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport imports data from the input.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	profile, err := fi.readProfile(ctx, dirEntry.FS)
	if err != nil {
		return fmt.Errorf("reading profile: %w", err)
	}
	owner := timeline.Entity{
		Name: profile.FirstName + " " + profile.LastName,
		Attributes: []timeline.Attribute{
			{
				Name:     "strava_athlete_id",
				Value:    profile.AthleteID,
				Identity: true,
			},
			{
				Name:        timeline.AttributeEmail,
				Value:       profile.EmailAddress,
				Identifying: true,
			},
			{
				Name:  timeline.AttributeGender,
				Value: profile.Sex,
			},
			{
				Name:  "weight_kilograms",
				Value: profile.WeightKilograms,
			},
			{
				Name:  "city",
				Value: profile.City,
			},
			{
				Name:  "state",
				Value: profile.State,
			},
			{
				Name:  "country",
				Value: profile.Country,
			},
		},
		NewPicture: func(context.Context) (io.ReadCloser, error) {
			picFile, err := dirEntry.FS.Open("profile.jpg")
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil, nil // not an error, just no picture
				}
				return nil, err
			}
			return picFile, nil
		},
	}

	activitiesFile, err := dirEntry.FS.Open("activities.csv")
	if err != nil {
		return err
	}
	defer activitiesFile.Close()

	reader := csv.NewReader(activitiesFile)

	fields := make(map[string]int)

	for {
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		// use header row to give us order-independent access to named fields
		// (the field order probably doesn't change but if it does we'll be ready)
		// (unless they rename fields, sigh)
		// (this also makes dealing with wide files a little less tedious, no need to count manually)
		if len(fields) == 0 {
			for i, field := range rec {
				// some field names are repeated (!???!?!?!?!) but I've found the second occurrences
				// are more precise than the first (!??!?!?), so let it overwrite prior entries I guess
				fields[field] = i
			}
			continue
		}

		// each activity is a collection of location points
		coll := &timeline.Item{
			ID:             rec[fields["Activity ID"]],
			Classification: timeline.ClassCollection,
			Content: timeline.ItemData{
				Data: timeline.StringData(rec[fields["Activity Name"]]),
			},
			Owner: owner,
			Metadata: timeline.Metadata{
				"Description":               rec[fields["Activity Description"]],
				"Activity Date":             rec[fields["Activity Date"]],
				"Activity Name":             rec[fields["Activity Name"]],
				"Activity Type":             rec[fields["Activity Type"]],
				"Activity Private Note":     rec[fields["Activity Private Note"]],
				"Activity Gear":             rec[fields["Activity Gear"]],
				"Filename":                  rec[fields["Filename"]],
				"Athlete Weight":            rec[fields["Athlete Weight"]],
				"Bike Weight":               rec[fields["Bike Weight"]],
				"Elapsed Time":              rec[fields["Elapsed Time"]],
				"Moving Time":               rec[fields["Moving Time"]],
				"Distance":                  rec[fields["Distance"]],
				"Max Speed":                 rec[fields["Max Speed"]],
				"Average Speed":             rec[fields["Average Speed"]],
				"Elevation Gain":            rec[fields["Elevation Gain"]],
				"Elevation Loss":            rec[fields["Elevation Loss"]],
				"Elevation Low":             rec[fields["Elevation Low"]],
				"Elevation High":            rec[fields["Elevation High"]],
				"Max Grade":                 rec[fields["Max Grade"]],
				"Average Grade":             rec[fields["Average Grade"]],
				"Average Positive Grade":    rec[fields["Average Positive Grade"]],
				"Average Negative Grade":    rec[fields["Average Negative Grade"]],
				"Max Cadence":               rec[fields["Max Cadence"]],
				"Average Cadence":           rec[fields["Average Cadence"]],
				"Max Heart Rate":            rec[fields["Max Heart Rate"]],
				"Average Heart Rate":        rec[fields["Average Heart Rate"]],
				"Max Watts":                 rec[fields["Max Watts"]],
				"Average Watts":             rec[fields["Average Watts"]],
				"Calories":                  rec[fields["Calories"]],
				"Max Temperature":           rec[fields["Max Temperature"]],
				"Average Temperature":       rec[fields["Average Temperature"]],
				"Relative Effort":           rec[fields["Relative Effort"]],
				"Total Work":                rec[fields["Total Work"]],
				"Number of Runs":            rec[fields["Number of Runs"]],
				"Uphill Time":               rec[fields["Uphill Time"]],
				"Downhill Time":             rec[fields["Downhill Time"]],
				"Other Time":                rec[fields["Other Time"]],
				"Perceived Exertion":        rec[fields["Perceived Exertion"]],
				"Weighted Average Power":    rec[fields["Weighted Average Power"]],
				"Power Count":               rec[fields["Power Count"]],
				"Prefer Perceived Exertion": rec[fields["Prefer Perceived Exertion"]],
				"Perceived Relative Effort": rec[fields["Perceived Relative Effort"]],
				"Commute":                   rec[fields["Commute"]],
				"Total Weight Lifted":       rec[fields["Total Weight Lifted"]],
				"From Upload":               rec[fields["From Upload"]],
				"Grade Adjusted Distance":   rec[fields["Grade Adjusted Distance"]],
				"Weather Observation Time":  rec[fields["Weather Observation Time"]],
				"Weather Condition":         rec[fields["Weather Condition"]],
				"Weather Temperature":       rec[fields["Weather Temperature"]],
				"Apparent Temperature":      rec[fields["Apparent Temperature"]],
				"Dewpoint":                  rec[fields["Dewpoint"]],
				"Humidity":                  rec[fields["Humidity"]],
				"Weather Pressure":          rec[fields["Weather Pressure"]],
				"Wind Speed":                rec[fields["Wind Speed"]],
				"Wind Gust":                 rec[fields["Wind Gust"]],
				"Wind Bearing":              rec[fields["Wind Bearing"]],
				"Precipitation Intensity":   rec[fields["Precipitation Intensity"]],
				"Sunrise Time":              rec[fields["Sunrise Time"]],
				"Sunset Time":               rec[fields["Sunset Time"]],
				"Moon Phase":                rec[fields["Moon Phase"]],
				"Bike":                      rec[fields["Bike"]],
				"Gear":                      rec[fields["Gear"]],
				"Precipitation Probability": rec[fields["Precipitation Probability"]],
				"Precipitation Type":        rec[fields["Precipitation Type"]],
				"Cloud Cover":               rec[fields["Cloud Cover"]],
				"Weather Visibility":        rec[fields["Weather Visibility"]],
				"UV Index":                  rec[fields["UV Index"]],
				"Weather Ozone":             rec[fields["Weather Ozone"]],
			},
		}

		// lots of numbers in this metadata could be represented as numbers
		coll.Metadata.StringsToSpecificType()

		err = fi.processActivity(ctx, dirEntry.FS, rec[fields["Filename"]], owner, coll, params)
		if errors.Is(err, fs.ErrNotExist) {
			// Strava is known to write some filenames as "#error#", indicating a bug in their export process: see issue #137
			params.Log.Error("could not open activity file; likely Strava bug causing export corruption",
				zap.String("filename", rec[fields["Filename"]]),
				zap.Error(err))
		} else if err != nil {
			return err
		}
	}

	return nil
}

func (FileImporter) processActivity(ctx context.Context, fsys fs.FS, filename string, owner timeline.Entity, coll *timeline.Item, opt timeline.ImportParams) error {
	dsOpt := opt.DataSourceOptions.(*Options)

	file, err := fsys.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	proc, err := gpx.NewProcessor(file, owner, opt, dsOpt.LocationProcessingOptions)
	if err != nil {
		return err
	}

	var position int

	for {
		g, err := proc.NextGPXGraph(ctx)
		if err != nil {
			return err
		}
		if g == nil {
			break
		}

		g.ToItemWithValue(timeline.RelInCollection, coll, position)

		opt.Pipeline <- g

		position++
	}

	return nil
}

func (FileImporter) readProfile(ctx context.Context, fsys fs.FS) (profile, error) {
	profileFile, err := fsys.Open("profile.csv")
	if err != nil {
		return profile{}, err
	}
	defer profileFile.Close()

	reader := csv.NewReader(profileFile)

	var headerRow []string

	var profile profile
	for {
		if err := ctx.Err(); err != nil {
			return profile, err
		}

		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return profile, err
		}
		if len(headerRow) == 0 {
			headerRow = rec
			continue
		}

		profile.AthleteID = rec[0]
		profile.EmailAddress = rec[1]
		profile.FirstName = rec[2]
		profile.LastName = rec[3]
		profile.Sex = rec[4]
		profile.Description = rec[5]
		profile.WeightKilograms = rec[6]
		profile.City = rec[7]
		profile.State = rec[8]
		profile.Country = rec[9]
		profile.HealthConsentStatus = rec[10]
		profile.DateOfHealthConsent = rec[11]

		break
	}

	return profile, nil
}

type profile struct {
	AthleteID           string
	EmailAddress        string
	FirstName           string
	LastName            string
	Sex                 string
	Description         string
	WeightKilograms     string
	City                string
	State               string
	Country             string
	HealthConsentStatus string
	DateOfHealthConsent string
}
