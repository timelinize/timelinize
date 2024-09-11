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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	weakrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"go.uber.org/zap"
)

// PopulateWithFakeData adds a bunch of fake data to the timeline.
func (tl *Timeline) PopulateWithFakeData(ctx context.Context) error {
	// if timeline is new, generate person ID 1 (as simulated timeline owner)
	if tl.Empty() {
		bd := gofakeit.Date()
		bp := gofakeit.City() + ", " + gofakeit.StateAbr()

		err := tl.StoreEntity(ctx, Entity{
			Name:       gofakeit.Name(),
			NewPicture: ByteData(gofakeit.ImageJpeg(512, 512)),
			Attributes: []Attribute{
				{
					Name:  AttributeEmail,
					Value: gofakeit.Email(),
				},
				{
					Name:  AttributePhoneNumber,
					Value: gofakeit.PhoneFormatted(),
				},
				{
					Name:  "birth_date",
					Value: bd,
				},
				{
					Name:  "birth_place",
					Value: bp,
				},
			},
		})
		if err != nil {
			return fmt.Errorf("storing person ID 1: %w", err)
		}
	}

	// generate corpus of people in this pretend person's life circle
	numPeople := gofakeit.Number(10, 500)
	people := make([]Entity, 0, numPeople)
	for range numPeople {
		ent := Entity{
			Name: gofakeit.Name(),
		}

		// add data to entity based on randomness
		rnd := weakrand.Int() //nolint:gosec

		const (
			emailProbability       = 2
			phoneProbability       = 5
			pictureProbability     = 7
			secondPhoneProbability = 3
			secondEmailProbability = 10
		)

		if rnd%10 > emailProbability {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributeEmail,
				Value: gofakeit.Email(),
			})
		}
		if rnd%10 > phoneProbability {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributePhoneNumber,
				Value: gofakeit.Phone(),
			})
		}
		if rnd&10 > pictureProbability {
			ent.NewPicture = ByteData(gofakeit.ImageJpeg(512, 512))
		}

		// less often, add a second email or phone number (TODO: useful?)
		if rnd%100 > secondEmailProbability {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributeEmail,
				Value: gofakeit.Email(),
			})
		}
		if rnd%100 > secondPhoneProbability {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributePhoneNumber,
				Value: gofakeit.Phone(),
			})
		}

		people = append(people, ent)
	}

	for _, ds := range dataSources {
		dsRowID, ok := tl.dataSources[ds.Name]
		if !ok {
			return fmt.Errorf("unknown data source: %s", ds.Name)
		}

		// inject our fake data source
		ds.NewFileImporter = func() FileImporter {
			return &fakeDataSource{
				realDS:       ds,
				peopleCorpus: people,
			}
		}
		ds.NewAPIImporter = nil

		importParams := ImportParameters{
			DataSourceName:    ds.Name,
			Filenames:         []string{"example.zip"},
			DataSourceOptions: nil, // TODO:
		}

		// create an import row in the DB
		mode := importModeAPI
		if len(importParams.Filenames) > 0 {
			mode = importModeFile
		}
		impRow, err := tl.newImport(ctx, importParams.DataSourceName, mode, ProcessingOptions{}, importParams.AccountID)
		if err != nil {
			return fmt.Errorf("creating new import row: %w", err)
		}

		logger := Log.Named("faker").With(zap.String("data_source", ds.Name))
		if len(importParams.Filenames) > 0 {
			logger = logger.With(zap.Strings("filenames", importParams.Filenames))
		}

		// set up the processor with our modified data source
		proc := processor{
			itemCount:        new(int64),
			skippedItemCount: new(int64),
			ds:               ds,
			dsRowID:          dsRowID,
			params:           importParams,
			tl:               tl,
			// acc:              Account{},
			impRow:   impRow,
			log:      logger,
			progress: logger.Named("progress"),
		}

		if err = proc.doImport(ctx); err != nil {
			return fmt.Errorf("processor of fake data failed: %w", err)
		}
	}

	return nil
}

type fakeDataSource struct {
	realDS       DataSource
	peopleCorpus []Entity
}

func (fakeDataSource) Recognize(_ context.Context, _ []string) (Recognition, error) {
	return Recognition{}, nil
}

func (fake *fakeDataSource) FileImport(_ context.Context, _ []string, itemChan chan<- *Graph, _ ListingOptions) error {
	var class Classification
	switch fake.realDS.Name {
	case "smsbackuprestore":
		class = ClassMessage
	case "google_location":
		class = ClassLocation
	case "google_photos":
		class = ClassMedia
	case "twitter":
		class = ClassSocial
	}

	switch fake.realDS.Name {
	case "contactlist", "vcard":
		for i := range fake.peopleCorpus {
			itemChan <- &Graph{Entity: &fake.peopleCorpus[i]}
		}

	case "google_photos":
		for range gofakeit.Number(100, 10000) {
			filename := gofakeit.Numerify("IMG_####_#####.jpg")
			itemChan <- &Graph{
				Item: &Item{
					ID:             gofakeit.UUID(),
					Classification: class,
					Timestamp: gofakeit.DateRange(
						time.Date(1995, 1, 1, 0, 0, 0, 0, time.Local),
						time.Now(),
					),
					Owner: Entity{ID: 1},
					Content: ItemData{
						Filename:  filename,
						MediaType: imageJpeg,
						Data:      ByteData(gofakeit.ImageJpeg(1024, 1024)),
					},
					// TODO: metadata, location...
					IntermediateLocation: filename,
				},
			}
		}

	case "smsbackuprestore":
		for range gofakeit.Number(100, 10000) {
			owner := fake.peopleCorpus[weakrand.Intn(len(fake.peopleCorpus))] //nolint:gosec
			owner = onlyKeepAttribute(owner, AttributePhoneNumber)

			numSentences := weakrand.Intn(5) //nolint:gosec
			sentLen := weakrand.Intn(10) + 2 //nolint:gosec

			// TODO: MMS, attachments, sent to...
			itemChan <- &Graph{
				Item: &Item{
					ID:             gofakeit.UUID(),
					Classification: class,
					Timestamp: gofakeit.DateRange(
						time.Date(1995, 1, 1, 0, 0, 0, 0, time.Local),
						time.Now(),
					),
					Owner: owner,
					Content: ItemData{
						Data: StringData(gofakeit.Paragraph(1, numSentences, sentLen, " ")),
					},
				},
			}
		}

		// default:
		// 	for i := 0; i < gofakeit.Number(10, 10000); i++ {
		// 		itemChan <- NewItemGraph(&Item{
		// 			ID:             gofakeit.UUID(),
		// 			Classification: class,
		// 			Timestamp: gofakeit.DateRange(
		// 				time.Date(1995, 1, 1, 0, 0, 0, 0, time.Local),
		// 				time.Now(),
		// 			),
		// 			Owner:   people[rand.Intn(len(people))],
		// 			Content: ItemData{},
		// 		})
		// 	}
	}

	return nil
}

func onlyKeepAttribute(ent Entity, keepAttrName string) Entity {
	var newAttrs []Attribute
	for _, attr := range ent.Attributes {
		if attr.Name == keepAttrName {
			newAttrs = append(newAttrs, attr)
		}
	}
	ent.Attributes = newAttrs
	return ent
}

// Anonymize obfuscates the results.
func (sr *SearchResults) Anonymize(opts ObfuscationOptions) {
	for _, item := range sr.Items {
		item.Anonymize(opts)
	}
}

// Anonymize obfuscates the entity.
func (re *relatedEntity) Anonymize(_ ObfuscationOptions) {
	if re == nil || re.ID == nil {
		return
	}

	// using the entity's ID as seed ensures consistency of faked data for this entity
	src := weakrand.New(weakrand.NewSource(*re.ID)) //nolint:gosec
	faker := gofakeit.NewCustom(src)

	if re.Name != nil {
		name := faker.Name()
		re.Name = &name
	}

	if re.Attribute.Name != nil {
		switch *re.Attribute.Name {
		case AttributeEmail:
			val := faker.Email()
			re.Attribute.Value = &val
		case AttributePhoneNumber:
			val := faker.PhoneFormatted()
			re.Attribute.Value = &val
		default:
			val := safeRandomString(len(*re.Attribute.Value), false, src)
			re.Attribute.Value = &val
		}
		re.Attribute.AltValue = nil
	}
}

// Anonymize obfuscates the result.
func (sr *SearchResult) Anonymize(opts ObfuscationOptions) {
	if sr == nil {
		return
	}

	sr.ItemRow.Anonymize(opts)
	sr.Entity.Anonymize(opts)

	for _, rel := range sr.Related {
		rel.FromEntity.Anonymize(opts)
		rel.ToEntity.Anonymize(opts)
		rel.FromItem.Anonymize(opts)
		rel.ToItem.Anonymize(opts)
	}
}

// Anonymize obfuscates the entity.
func (e *Entity) Anonymize() {
	if e == nil {
		return
	}

	// using the entity's ID as seed ensures consistency of faked data for this entity
	src := weakrand.New(weakrand.NewSource(e.ID)) //nolint:gosec
	faker := gofakeit.NewCustom(src)

	if e.Name != "" {
		e.Name = faker.Name()
	}

	for i := range e.Attributes {
		switch e.Attributes[i].Name {
		case AttributeEmail:
			e.Attributes[i].Value = faker.Email()
		case AttributePhoneNumber:
			e.Attributes[i].Value = faker.PhoneFormatted()
		case "birth_date":
			e.Attributes[i].Value = gofakeit.Date()
		case "birth_place":
			e.Attributes[i].Value = gofakeit.City() + ", " + gofakeit.StateAbr()
		default:
			e.Attributes[i].Value = safeRandomString(len(e.Attributes[i].valueString()), false, src)
		}
	}

	// TODO: maybe we want to set a picture even if there isn't one, randomly? like 50-50 chance?
	if e.Picture != nil {
		e.NewPicture = func(ctx context.Context) (io.ReadCloser, error) {
			picURL := fmt.Sprintf("https://picsum.photos/seed/%d/1024/768", e.ID)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, picURL, nil)
			if err != nil {
				return nil, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			return resp.Body, err
		}
	}
}

// Anonymize obfuscates the item.
func (ir *ItemRow) Anonymize(opts ObfuscationOptions) {
	if ir == nil {
		return
	}

	// using the item's ID as seed ensures consistency of faked data for this item
	// and also helps prevent reversing the true data by averaging many observations
	faker := gofakeit.New(ir.ID)

	if ir.Latitude != nil && ir.Longitude != nil {
		for _, locob := range opts.Locations {
			if locob.Contains(*ir.Latitude, *ir.Longitude) {
				lat, lon := locob.Obfuscate(*ir.Latitude, *ir.Longitude, ir.ID)
				ir.Latitude = &lat
				ir.Longitude = &lon
			}
		}
	}

	if ir.DataText != nil {
		paragraphs := strings.Count(*ir.DataText, "\n") + 1
		sentences := strings.Count(*ir.DataText, ". ") + strings.Count(*ir.DataText, "? ") + strings.Count(*ir.DataText, "! ") + 1
		words := strings.Count(*ir.DataText, " ") + 1
		txt := faker.Paragraph(paragraphs, sentences/paragraphs, words/sentences, "\n")
		ir.DataText = &txt
	}

	if ir.DataType != nil && ir.DataFile != nil {
		switch {
		case strings.HasPrefix(*ir.DataType, "image/"):
			// simply use thumbhash
			ir.DataFile = nil

			// or use a random image completely... :shrug:
			// pic := fmt.Sprintf("https://picsum.photos/seed/%d/1024/768", ir.ID)
			// ir.DataFile = &pic
		case strings.HasPrefix(*ir.DataType, "video/"):
			// let frontend request videos; frontend handler will obfuscate them
		default:
			// TODO: not sure how to generate/obfuscate other stuff for now
			ir.DataFile = nil
		}
	}

	if len(ir.Metadata) > 0 {
		var meta Metadata
		err := json.Unmarshal(ir.Metadata, &meta)
		if err != nil {
			opts.Logger.Warn("could not anonymize metadata; clearing it instead", zap.Error(err))
			ir.Metadata = nil
		} else {
			for key, val := range meta {
				// TODO: some field-aware replacement might be cool, for now just switch on type
				switch v := val.(type) {
				case int:
					meta[key] = faker.IntRange(-v*10, v*10)
				case float64:
					meta[key] = faker.Float64Range(-v*10, v*10)
				case string:
					// replace numbers with random numbers, and letters with random letters
					chars := []rune(v)
					for i, ch := range chars {
						chars[i] = randRune(ch)
					}
					meta[key] = string(chars)
				case time.Time:
					meta[key] = faker.PastDate()
				case bool:
					meta[key] = faker.Bool()
				}
			}
			ir.Metadata, _ = json.Marshal(meta) //nolint:errchkjson
		}
	}
}

// randRune randomizes ch by replacing it with a random
// character in its same class (number, lowercase, uppercase).
// For non-ASCII, it's just randomly shifted.
func randRune(ch rune) rune {
	if ch >= '0' && ch <= '9' {
		return rune(weakrand.Intn('9'-'0') + '0') //nolint:gosec
	}
	if ch >= 'a' && ch <= 'z' {
		return rune(weakrand.Intn('z'-'a') + 'a') //nolint:gosec
	}
	if ch >= 'A' && ch <= 'Z' {
		return rune(weakrand.Intn('Z'-'A') + 'A') //nolint:gosec
	}
	if ch > 127 {
		// ¯\_(ツ)_/¯
		ch += rune(weakrand.Intn(20) - 10) //nolint:gosec
	}
	return ch
}

// ObfuscationOptions controls how obfuscation is performed.
// TODO: Finish implementing these
type ObfuscationOptions struct {
	Locations []LocationObfuscation
	Logger    *zap.Logger
}

// LocationObfuscation describes how to obfuscate a coordinate.
type LocationObfuscation struct {
	Latitude, Longitude float64
	RadiusMeters        int
}

// Contains returns true if the circle approximately contains the given coordinate.
func (l LocationObfuscation) Contains(lat, lon float64) bool {
	return haversineDistanceMeters(l.Latitude, l.Longitude, lat, lon) < float64(l.RadiusMeters)
}

// Obfuscate returns obfuscated lat/lon values.
func (l LocationObfuscation) Obfuscate(lat, lon float64, rowID int64) (float64, float64) {
	faker := gofakeit.New(rowID)

	// translate all points within the circle a fixed vector; necessary to prevent
	// averaging the smattering of points to find the original center(s)
	circleFaker := gofakeit.New(int64((l.Latitude + l.Longitude) * 1e7))

	// then shift each point a little bit individually to prevent map overlay attacks
	// where you estimate the initial translation by seeing what map features the
	// points align with by translating the map around underneath it
	lat += (circleFaker.Float64Range(-0.1, 0.1) + faker.Float64Range(-0.02, 0.02))
	lon += (circleFaker.Float64Range(-0.1, 0.1) + faker.Float64Range(-0.01, 0.01))
	return lat, lon
}

// haversineDistanceMeters returns the approximate number of meters between two points on earth's surface.
func haversineDistanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := degreesToRadians(lat1)
	phi2 := degreesToRadians(lat2)
	lambda1 := degreesToRadians(lon1)
	lambda2 := degreesToRadians(lon2)
	return 2 * (earthRadiusKm * 1000) * math.Asin(math.Sqrt(haversin(phi2-phi1)+math.Cos(phi1)*math.Cos(phi2)*haversin(lambda2-lambda1)))
}

func haversin(theta float64) float64 {
	return 0.5 * (1 - math.Cos(theta))
}

func degreesToRadians(d float64) float64 {
	return d * (math.Pi / 180)
}

const (
	earthRadiusMi = 3958
	earthRadiusKm = 6371
)
