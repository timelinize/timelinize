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
	weakrand "math/rand/v2"
	"mime"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
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

		if rnd%10 > 2 {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributeEmail,
				Value: gofakeit.Email(),
			})
		}
		if rnd%10 > 5 {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributePhoneNumber,
				Value: gofakeit.Phone(),
			})
		}
		if rnd&10 > 7 {
			ent.NewPicture = ByteData(gofakeit.ImageJpeg(512, 512))
		}

		// less often, add a second email or phone number (TODO: useful?)
		if rnd%100 > 3 {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributeEmail,
				Value: gofakeit.Email(),
			})
		}
		if rnd%100 > 10 {
			ent.Attributes = append(ent.Attributes, Attribute{
				Name:  AttributePhoneNumber,
				Value: gofakeit.Phone(),
			})
		}

		people = append(people, ent)
	}

	for _, ds := range dataSources {
		// dsRowID, ok := tl.dataSources[ds.Name]
		// if !ok {
		// 	return fmt.Errorf("unknown data source: %s", ds.Name)
		// }

		// inject our fake data source
		ds.NewFileImporter = func() FileImporter {
			return &fakeDataSource{
				realDS:       ds,
				peopleCorpus: people,
			}
		}
		ds.NewAPIImporter = nil

		// TODO: Start the faker import job! Old code:

		// importParams := ImportParameters{
		// 	DataSourceName:    ds.Name,
		// 	Filenames:         []string{"example.zip"},
		// 	DataSourceOptions: nil, // TODO:
		// }

		// // create an import row in the DB
		// impRow, err := tl.newJob(ctx, importParams.DataSourceName, mode, ProcessingOptions{}, importParams.AccountID)
		// if err != nil {
		// 	return fmt.Errorf("creating new import row: %w", err)
		// }

		// logger := Log.Named("faker").With(zap.String("data_source", ds.Name))
		// if len(importParams.Filenames) > 0 {
		// 	logger = logger.With(zap.Strings("filenames", importParams.Filenames))
		// }

		// // set up the processor with our modified data source
		// proc := processor{
		// 	itemCount:        new(int64),
		// 	skippedItemCount: new(int64),
		// 	ds:               ds,
		// 	dsRowID:          dsRowID,
		// 	params:           importParams,
		// 	tl:               tl,
		// 	// acc:              Account{},
		// 	jobRow:   impRow,
		// 	log:      logger,
		// 	progress: logger.Named("progress"),
		// }

		// if err = proc.doImport(ctx); err != nil {
		// 	return fmt.Errorf("processor of fake data failed: %w", err)
		// }
	}

	return nil
}

type fakeDataSource struct {
	realDS       DataSource
	peopleCorpus []Entity
}

func (fakeDataSource) Recognize(_ context.Context, _ DirEntry, _ RecognizeParams) (Recognition, error) {
	return Recognition{}, nil
}

func (fake *fakeDataSource) FileImport(_ context.Context, _ DirEntry, params ImportParams) error {
	var class Classification
	switch fake.realDS.Name {
	case "sms_backup_restore":
		class = ClassMessage
	case "google_location":
		class = ClassLocation
	case "google_photos":
		class = ClassMedia
	case "twitter":
		class = ClassSocial
	}

	switch fake.realDS.Name {
	case "contact_list", "vcard":
		for i := range fake.peopleCorpus {
			params.Pipeline <- &Graph{Entity: &fake.peopleCorpus[i]}
		}

	case "google_photos":
		for range gofakeit.Number(100, 10000) {
			filename := gofakeit.Numerify("IMG_####_#####.jpg")
			params.Pipeline <- &Graph{
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
						MediaType: ImageJPEG,
						Data:      ByteData(gofakeit.ImageJpeg(1024, 1024)),
					},
					// TODO: metadata, location...
					IntermediateLocation: filename,
				},
			}
		}

	case "sms_backup_restore":
		for range gofakeit.Number(100, 10000) {
			owner := fake.peopleCorpus[weakrand.IntN(len(fake.peopleCorpus))] //nolint:gosec
			owner = onlyKeepAttribute(owner, AttributePhoneNumber)

			numSentences := weakrand.IntN(5) //nolint:gosec
			sentLen := weakrand.IntN(10) + 2 //nolint:gosec

			// TODO: MMS, attachments, sent to...
			params.Pipeline <- &Graph{
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
		// 		params.Pipeline <- NewItemGraph(&Item{
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
func (re *relatedEntity) Anonymize(opts ObfuscationOptions) {
	if re == nil || re.ID == nil {
		return
	}

	// using the entity's ID as seed ensures consistency of faked data for this entity
	src := weakrand.NewPCG(*re.ID, 0)
	faker := gofakeit.NewFaker(src, false)

	if re.Name != nil {
		name := consistentFakeEntityName(*re.Name)
		re.Name = &name
	}

	if re.Attribute.Name != nil && re.Attribute.Value != nil {
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

		if re.Attribute.Latitude != nil && re.Attribute.Longitude != nil {
			for _, locob := range opts.Locations {
				if locob.Contains(*re.Attribute.Latitude, *re.Attribute.Longitude) {
					lat, lon := locob.Obfuscate(*re.Attribute.Latitude, *re.Attribute.Longitude, *re.Attribute.ID)
					re.Attribute.Latitude = &lat
					re.Attribute.Longitude = &lon
					break
				}
			}
		}
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
func (e *Entity) Anonymize(opts ObfuscationOptions) {
	if e == nil {
		return
	}

	// using the entity's ID as seed ensures consistency of faked data for this entity
	src := weakrand.NewPCG(e.ID, 0)
	faker := gofakeit.NewFaker(src, false)

	// but to fake the name, we actually use a faker with a seed based on each
	// space-separated part of the name; this means that names like Betty will
	// always be replaced with the same fake name, so if you have entities "Betty
	// Jane" and "Betty Jane Smith" who are actually the same person, then you
	// will see more consistent fake names like "Kristen May" and "Kristen May
	// Pike" -- keeping the name pattern, which is more helpful in demos.
	// This isn't perfect though, like "Matt" is obviously short for "Matthew"
	// in the unobfuscated space, but the faker just uses hashing so it doesn't
	// care about that, and they will have totally different fake names in the
	// obfuscated space. Oh well.
	e.Name = consistentFakeEntityName(e.Name)

	for i := range e.Attributes {
		if e.Attributes[i].Value != nil {
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

		if e.Attributes[i].Latitude != nil && e.Attributes[i].Longitude != nil {
			for _, locob := range opts.Locations {
				if locob.Contains(*e.Attributes[i].Latitude, *e.Attributes[i].Longitude) {
					lat, lon := locob.Obfuscate(*e.Attributes[i].Latitude, *e.Attributes[i].Longitude, e.Attributes[i].ID)
					e.Attributes[i].Latitude = &lat
					e.Attributes[i].Longitude = &lon
					break
				}
			}
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

// consistentFakeEntityName generates a fake name seeded by the individual
// space-separated words in the real name.
func consistentFakeEntityName(realName string) string {
	if realName == "" {
		return ""
	}
	var fakeName string
	names := strings.Split(strings.ToLower(realName), " ")
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		nameSrc := weakrand.NewPCG(dumbHash(name), 0)
		nameFaker := gofakeit.NewFaker(nameSrc, false)
		if len(name) == 1 { //nolint:gocritic
			if len(fakeName) > 0 {
				fakeName += " "
			}
			fakeName += strings.ToUpper(nameFaker.Letter()) // name len 1 is likely an initial; generate a random initial in turn
		} else if fakeName == "" {
			fakeName = nameFaker.FirstName()
		} else if i < len(names)-1 {
			fakeName += " " + nameFaker.MiddleName()
		} else {
			fakeName += " " + nameFaker.LastName()
		}
	}
	return fakeName
}

func dumbHash(input string) uint64 {
	var checksum uint64
	for i, ch := range strings.ToLower(input) {
		checksum += uint64(int(ch) * (i + 1)) //nolint:gosec
	}
	return checksum
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
				break
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

	if ir.Filename != nil || ir.IntermediateLocation != nil || ir.OriginalLocation != nil {
		// try to preserve the file extension, but we can't be sure what it is
		// (what if it's .tar.gz? What if there's a dot in the middle of the filename?
		// I don't want to chance preserving sensitive parts of the filename by parsing)
		// so try to deduce from the DataType; otherwise just use a random one
		var extensions []string
		var err error
		if ir.DataType != nil {
			if *ir.DataType == "image/heic" {
				extensions = []string{".heic"}
			} else {
				extensions, err = mime.ExtensionsByType(*ir.DataType)
				// prefer common extensions, not ".jfif" and ".f4v" or whatever
				sort.Slice(extensions, func(i int, _ int) bool {
					switch extensions[i] {
					case ".jpg", ".jpeg", ".mp4", ".m4v", ".mov", ".heic":
						return true
					}
					return false
				})
			}
		}
		if err != nil || len(extensions) == 0 {
			opts.Logger.Warn("no extensions for MIME type; generating a random one",
				zap.Stringp("mime_type", ir.DataType),
				zap.Error(err))
			extensions = []string{"." + faker.FileExtension()}
		}
		fakeName := faker.Word() + faker.Password(true, true, true, false, false, weakrand.IntN(3)+2) + extensions[0] //nolint:gosec
		if ir.Filename != nil {
			ir.Filename = &fakeName
		}
		if ir.IntermediateLocation != nil {
			fakePath := fmt.Sprintf("demo/%s/%s", faker.Word(), fakeName)
			ir.IntermediateLocation = &fakePath
		}
		if ir.OriginalLocation != nil {
			fakePath := fmt.Sprintf("%s/%s", faker.Word(), fakeName)
			ir.OriginalLocation = &fakePath
		}
	}

	if ir.DataFile != nil {
		if opts.DataFiles {
			// using a hard-coded string value here at least lets the frontend know that the filename is obfuscated,
			// but if this setting is enabled, the frontend shouldn't be trying to request the data files anyway
			fakePath := path.Join(path.Dir(*ir.DataFile), "(obfuscated)")
			ir.DataFile = &fakePath
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
				// confusing and not particularly useful to obfuscate the size of a location cluster
				if key == "Cluster size" {
					continue
				}
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
		return rune(weakrand.IntN('9'-'0') + '0') //nolint:gosec
	}
	if ch >= 'a' && ch <= 'z' {
		return rune(weakrand.IntN('z'-'a') + 'a') //nolint:gosec
	}
	if ch >= 'A' && ch <= 'Z' {
		return rune(weakrand.IntN('Z'-'A') + 'A') //nolint:gosec
	}
	if ch > 127 {
		// ¯\_(ツ)_/¯
		ch += rune(weakrand.IntN(20) - 10) //nolint:gosec
	}
	return ch
}

// ObfuscationOptions controls how obfuscation is performed.
// TODO: Finish implementing these
type ObfuscationOptions struct {
	Logger *zap.Logger `json:"-"`

	// Whether to enable obfuscation.
	Enabled bool `json:"enabled"`

	// The timeline IDs to apply obfuscation to. If nil,
	// all timelines will be obfuscated if enabled.
	RepoIDs []string `json:"repo_ids,omitempty"`

	Locations []ObfuscatedLocation `json:"locations,omitempty"`

	// If true, the base (last) component of data file names will be
	// obfuscated, but this prevents the frontend from requesting the
	// (obfuscated) data, because it won't have the filename with
	// which to craft the request. Enable this if the data file is
	// what is being displayed rather than the content.
	DataFiles bool `json:"data_files,omitempty"`
}

func (obf ObfuscationOptions) AppliesTo(tl *Timeline) bool {
	return obf.Enabled &&
		(obf.RepoIDs == nil || slices.Contains(obf.RepoIDs, tl.id.String()))
}

// ObfuscatedLocation describes how to obfuscate a coordinate.
type ObfuscatedLocation struct {
	Description  string  `json:"description,omitempty"` // for convenience with managing
	Lat          float64 `json:"lat,omitempty"`         // latitude
	Lon          float64 `json:"lon,omitempty"`         // longitude
	RadiusMeters int     `json:"radius_meters,omitempty"`
}

// Contains returns true if the circle approximately contains the given coordinate.
func (l ObfuscatedLocation) Contains(lat, lon float64) bool {
	return haversineDistanceMeters(l.Lat, l.Lon, lat, lon) < float64(l.RadiusMeters)
}

// Obfuscate returns obfuscated lat/lon values.
func (l ObfuscatedLocation) Obfuscate(lat, lon float64, rowID uint64) (float64, float64) {
	faker := gofakeit.New(rowID)

	// translate all points within the circle a fixed vector; necessary to preven`t
	// averaging the smattering of points to find the original center(s)
	circleFaker := gofakeit.New(uint64((l.Lat + l.Lon) * 1e7))

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
