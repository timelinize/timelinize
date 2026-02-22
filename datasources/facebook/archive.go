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

package facebook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// TODO: Add media from "album" folder (create Collections as well)

const (
	pre2024ProfileInfoPath             = "profile_information/profile_information.json"
	pre2024YourUncategorizedPhotosPath = "posts/your_uncategorized_photos.json"
	pre2024YourVideosPath              = "posts/your_videos.json"
	pre2024YourPostsPrefix             = "posts/your_posts_"
	pre2024MessagesPrefix              = "messages"
	messagesPrefix2025                 = "your_instagram_activity/messages"
)

const (
	year2024ProfileInfoPath             = "personal_information/profile_information/profile_information.json"
	year2024YourUncategorizedPhotosPath = "your_facebook_activity/posts/your_uncategorized_photos.json"
	year2024YourVideosPath              = "your_facebook_activity/posts/your_videos.json"
	year2024YourPostsPrefix             = "your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_"
	year2024MessagesPrefix              = "your_facebook_activity/messages"
	year2024PostMediaPrefix             = "your_facebook_activity/posts/media"
	year2024AlbumPrefix                 = "your_facebook_activity/posts/album"
	year2024TaggedPlacesPath            = "your_facebook_activity/posts/places_you_have_been_tagged_in.json"
	year2024CheckInsPath                = "your_facebook_activity/posts/check-ins.json"
)

// Archive implements the importer for Facebook archives.
type Archive struct {
	owner timeline.Entity
}

// Recognize returns whether the input file is recognized.
func (Archive) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// reject HTML-formatted archives, which we don't support
	const oneDotHTML = "1.html"
	if dirEntry.FileExists(pre2024YourPostsPrefix+oneDotHTML) ||
		dirEntry.FileExists(year2024YourPostsPrefix+oneDotHTML) ||
		dirEntry.FileExists(path.Join(year2024AlbumPrefix, oneDotHTML)) {
		return timeline.Recognition{}, nil
	}

	if dirEntry.FileExists(pre2024ProfileInfoPath) ||
		dirEntry.FileExists(year2024ProfileInfoPath) {
		return timeline.Recognition{Confidence: 1}, nil
	}
	if dirEntry.FileExists("your_facebook_activity") {
		return timeline.Recognition{Confidence: .95}, nil
	}
	if strings.HasPrefix(dirEntry.Name(), "facebook-") {
		return timeline.Recognition{Confidence: .9}, nil
	}
	return timeline.Recognition{}, nil
}

// FileImport imports the data in the file.
func (a Archive) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	if err := a.setOwnerEntity(ctx, dirEntry, dsOpt); err != nil {
		return err
	}

	// start with oldest supported archive version
	postsFilePrefix := pre2024YourPostsPrefix

	// posts
	for i := 1; i < 10000; i++ {
		postsFilename := fmt.Sprintf("%s%d.json", postsFilePrefix, i)

		postsFile, err := dirEntry.Open(postsFilename)
		if errors.Is(err, fs.ErrNotExist) && postsFilePrefix == pre2024YourPostsPrefix {
			// try newer version
			postsFilePrefix = year2024YourPostsPrefix
			postsFilename = fmt.Sprintf("%s%d.json", postsFilePrefix, i)
			postsFile, err = dirEntry.Open(postsFilename)
		}
		if errors.Is(err, fs.ErrNotExist) {
			break // no more posts files
		}
		if err != nil {
			return err
		}

		err = a.processPostsFile(ctx, dirEntry, postsFile, params)
		postsFile.Close()
		if err != nil {
			return fmt.Errorf("processing %s: %w", postsFilename, err)
		}
	}

	// album media
	if err := a.processAlbumFiles(ctx, dirEntry, params, []string{
		year2024AlbumPrefix,
	}); err != nil {
		return fmt.Errorf("processing album folder: %w", err)
	}

	// post media (done separately since they may not be in the same archive as the JSON manifest)
	if err := a.processPostMedia(ctx, dirEntry, params, []string{
		year2024PostMediaPrefix,
		"your_activity_across_facebook", // also found this in a year 2024 archive, full of media files
	}); err != nil {
		return fmt.Errorf("processing post media: %w", err)
	}

	// uncategorized photos
	if err := a.processPhotosOrVideos(ctx, dirEntry, params, []string{
		pre2024YourUncategorizedPhotosPath,
		year2024YourUncategorizedPhotosPath,
	}, new(fbYourUncategorizedPhotos)); err != nil {
		return fmt.Errorf("processing uncategorized photos: %w", err)
	}

	// uncategorized videos
	if err := a.processPhotosOrVideos(ctx, dirEntry, params, []string{
		pre2024YourVideosPath,
		year2024YourVideosPath,
	}, new(fbYourVideos)); err != nil {
		return fmt.Errorf("processing videos: %w", err)
	}

	// tagged places
	if err := a.processTaggedPlaces(ctx, dirEntry, params, []string{
		year2024TaggedPlacesPath,
	}); err != nil {
		return fmt.Errorf("processing tagged places: %w", err)
	}

	// check-ins
	if err := a.processCheckins(ctx, dirEntry, params, []string{
		year2024CheckInsPath,
	}); err != nil {
		return fmt.Errorf("processing check-ins: %w", err)
	}

	// messages
	err := GetMessages("facebook", dirEntry, params)
	if err != nil {
		return err
	}

	return nil
}

func (a Archive) processPostsFile(ctx context.Context, d timeline.DirEntry, file fs.File, params timeline.ImportParams) error {
	var posts yourPosts
	if err := json.NewDecoder(file).Decode(&posts); err != nil {
		return err
	}

	for _, post := range posts {
		if err := ctx.Err(); err != nil {
			return err
		}

		var postText string
		for _, postData := range post.Data {
			if postData.Post != "" {
				postText = FixString(postData.Post)
				break
			}
		}

		item := &timeline.Item{
			Classification: timeline.ClassSocial,
			Timestamp:      time.Unix(post.Timestamp, 0).UTC(), // these unix timestams are set in UTC
			Owner:          a.owner,
			Content: timeline.ItemData{
				Data: timeline.StringData(postText),
			},
			Metadata: timeline.Metadata{
				"Title": post.Title,
			},
		}
		ig := &timeline.Graph{Item: item}

		const nameMatchIndex = 1
		if matches := wroteOnOtherTimelineRegex.FindStringSubmatch(post.Title); len(matches) == nameMatchIndex+1 {
			ig.ToEntity(timeline.RelSent, &timeline.Entity{
				Name: matches[1],
				Attributes: []timeline.Attribute{
					{
						Name:        "facebook_name",
						Value:       matches[1],
						Identifying: true,
					},
				},
			})
		}

		// confusingly, an attachment object in this array can have multiple attachments
		// of various types... I don't really know why they group them together, but we
		// just store 1 point of data (whether it be a location, a media file, or text
		// content, for example) per item
		for _, attachmentGroup := range post.Attachments {
			for _, attachment := range attachmentGroup.Data {
				attachedItem := &timeline.Item{
					Owner:     a.owner,
					Timestamp: item.Timestamp, // assume main item timestamp, can be changed later
					Metadata:  make(timeline.Metadata),
				}
				switch {
				case attachment.Text != "":
					text := FixString(attachment.Text)
					if descStr, ok := attachedItem.Metadata["Description"].(string); ok && text != descStr {
						attachedItem.Content.Data = timeline.StringData(text)
					}
				case attachment.Media.URI != "":
					attachedItem.Classification = timeline.ClassSocial
					attachment.Media.fillItem(attachedItem, d, postText, params.Log)
				case attachment.ExternalContext.URL != "":
					attachedItem.Content.Data = timeline.StringData(attachment.ExternalContext.Name)
					attachedItem.Metadata["URL"] = attachment.ExternalContext.URL
					attachedItem.Classification = timeline.ClassLocation
					attachedItem.Timestamp = item.Timestamp
				case attachment.Place.Name != "" ||
					attachment.Place.Address != "" ||
					attachment.Place.URL != "" ||
					(attachment.Place.Coordinate.Latitude != 0 && attachment.Place.Coordinate.Longitude != 0):
					// We don't know the context of this attachment; is it something like, "I visited this
					// place, it was cool?" or is it like "Hey everyone, this place is having an event
					// that you should go to" -- i.e. does it necessarily mean the owner is at this place?
					// Maybe sometimes, but I don't know that we know for sure.
					// I wonder if we should just store this as an entity and attach it

					address := timeline.Attribute{
						Name:  "address",
						Value: attachment.Place.Address,
					}
					if attachment.Place.Coordinate.Latitude != 0 && attachment.Place.Coordinate.Longitude != 0 {
						address.Latitude = &attachment.Place.Coordinate.Latitude
						address.Longitude = &attachment.Place.Coordinate.Longitude
					}

					place := &timeline.Entity{
						Type:       timeline.EntityPlace,
						Name:       attachment.Place.Name,
						Attributes: []timeline.Attribute{address},
					}
					ig.ToEntity(timeline.RelAttachment, place)
				}

				// newDescription := strings.Join(allText, "\n")
				// if existingDesc, ok := attachedItem.Metadata["Description"].(string); ok && existingDesc != "" {
				// 	newDescription += "\n\n" + existingDesc
				// }
				// attachedItem.Metadata["Description"] = newDescription

				if attachedItem.Content.Data != nil || attachedItem.Content.Filename != "" {
					ig.ToItem(timeline.RelAttachment, attachedItem)
				}
			}
		}

		params.Pipeline <- ig
	}

	return nil
}

func (a Archive) processAlbumFiles(ctx context.Context, tlDirEntry timeline.DirEntry, opt timeline.ImportParams, pathsToTry []string) error {
	for _, pathToTry := range pathsToTry {
		err := fs.WalkDir(tlDirEntry, pathToTry, func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("visiting %s: %w", fpath, err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}

			f, err := tlDirEntry.Open(fpath)
			if err != nil {
				return err
			}
			defer f.Close()

			var albumInfo fbAlbumMeta
			if err := json.NewDecoder(f).Decode(&albumInfo); err != nil {
				return fmt.Errorf("decoding album file: %s: %w", fpath, err)
			}

			for _, entry := range albumInfo.Photos {
				if entry.URI == "" {
					continue
				}
				it := &timeline.Item{
					Classification:       timeline.ClassMedia,
					IntermediateLocation: entry.URI,
					Owner:                a.owner,
					Timestamp:            time.Unix(entry.CreationTimestamp, 0).UTC(), // this is not when the photo was taken, but we'll get that later, if it's in the actual photo itself
					Content: timeline.ItemData{
						Filename: path.Base(entry.URI),
					},
					Metadata: timeline.Metadata{
						"Description": FixString(entry.Description),
					},
				}
				it.Retrieval.SetKey(retrievalKey(tlDirEntry, entry.URI))
				it.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
					"intermediate_location": timeline.UpdatePolicyPreferExisting,
					"timestamp":             timeline.UpdatePolicyPreferExisting,
					"location":              timeline.UpdatePolicyPreferIncoming,
					"owner":                 timeline.UpdatePolicyPreferIncoming,
					"data":                  timeline.UpdatePolicyPreferExisting,
					"metadata":              timeline.UpdatePolicyPreferIncoming,
				}

				g := &timeline.Graph{Item: it}

				// add photo to album
				g.ToItem(timeline.RelInCollection, &timeline.Item{
					Classification: timeline.ClassCollection,
					Content: timeline.ItemData{
						Data: timeline.StringData(albumInfo.Name),
					},
					Owner: a.owner,
					Metadata: timeline.Metadata{
						"Description": FixString(albumInfo.Description),
					},
				})

				opt.Pipeline <- g
			}

			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			continue // it's valid for an archive not to have this data
		}
		if err != nil {
			return fmt.Errorf("could not walk known album folder: %s: %w", pathToTry, err)
		}
	}

	return nil
}

func (a Archive) processPostMedia(ctx context.Context, tlDirEntry timeline.DirEntry, opt timeline.ImportParams, pathsToTry []string) error {
	for _, pathToTry := range pathsToTry {
		err := fs.WalkDir(tlDirEntry, pathToTry, func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("visiting %s: %w", fpath, err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return err
			}

			ext := strings.ToLower(path.Ext(info.Name()))
			switch ext {
			// in reality I've only seen .jpg, .mp4, and no extension, for valid media files
			case ".jpg", ".jpeg", ".gif", ".png", ".heic", "", ".mp4", ".mov":
			default:
				return nil
			}

			it := &timeline.Item{
				Classification:       timeline.ClassMedia,
				IntermediateLocation: fpath,
				Owner:                a.owner,
				Content: timeline.ItemData{
					Filename: d.Name(),
					Size:     uint64(info.Size()), //nolint:gosec // Sigh, yes, I know this can overflow... but will it really??
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return tlDirEntry.Open(fpath)
					},
				},
			}

			_, err = media.ExtractAllMetadata(opt.Log, tlDirEntry, fpath, it, timeline.MetaMergeAppend)
			if err != nil {
				opt.Log.Error("extracting metadata from Facebook media",
					zap.String("file", fpath),
					zap.Error(err))
			}

			retKey := retrievalKey(tlDirEntry, fpath)
			it.Retrieval.SetKey(retKey)
			it.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
				"data":     timeline.UpdatePolicyPreferIncoming,
				"metadata": timeline.UpdatePolicyKeepExisting,
			}

			opt.Pipeline <- &timeline.Graph{Item: it}

			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			continue // it's valid for an archive not to have this data
		}
		if err != nil {
			return fmt.Errorf("could not open known post media folder: %w - tried: %s", err, pathToTry)
		}
	}

	return nil
}

func (a Archive) processPhotosOrVideos(ctx context.Context, tlDirEntry timeline.DirEntry, opt timeline.ImportParams, pathsToTry []string, unmarshalInto any) error {
	for _, pathToTry := range pathsToTry {
		file, err := tlDirEntry.Open(pathToTry)
		if errors.Is(err, fs.ErrNotExist) {
			continue // it's valid for an archive not to have this data
		}
		if err != nil {
			return fmt.Errorf("could not open known photos/videos folder: %w - tried: %s", err, pathToTry)
		}
		defer file.Close()

		if err := json.NewDecoder(file).Decode(unmarshalInto); err != nil {
			return err
		}

		var medias []fbArchiveMedia
		switch v := unmarshalInto.(type) {
		case *fbYourUncategorizedPhotos:
			medias = v.OtherPhotosV2
		case *fbYourVideos:
			medias = v.VideosV2
		}

		for _, media := range medias {
			if err := ctx.Err(); err != nil {
				return err
			}

			item := &timeline.Item{
				Owner:          a.owner,
				Classification: timeline.ClassMedia,
			}

			media.fillItem(item, tlDirEntry, "", opt.Log)

			opt.Pipeline <- &timeline.Graph{Item: item}
		}
	}

	return nil
}

func (a Archive) processTaggedPlaces(ctx context.Context, tlDirEntry timeline.DirEntry, params timeline.ImportParams, pathsToTry []string) error {
	for _, pathToTry := range pathsToTry {
		file, err := tlDirEntry.Open(pathToTry)
		if errors.Is(err, fs.ErrNotExist) {
			continue // it's valid for an archive not to have this data
		}
		if err != nil {
			return fmt.Errorf("could not open known tagged places folder: %w - tried: %s", err, pathToTry)
		}
		defer file.Close()

		var taggedPlaces fbTaggedPlaces
		if err := json.NewDecoder(file).Decode(&taggedPlaces); err != nil {
			return err
		}

		for _, taggedPlace := range taggedPlaces {
			if err := ctx.Err(); err != nil {
				return err
			}

			// TODO: a privacy-preserving way to get the coordinates would be good (possibly a lookup using the Facebook ID could do it)

			visitItem := &timeline.Item{
				Owner:          a.owner,
				Classification: timeline.ClassLocation, // TODO: Technically, we don't have coordinates, should we make a new class?? (leaning toward no: a location can still be a visit to a named place, even if coords unknown; the class just means this person was logged at being at a place)
			}
			place := &timeline.Entity{
				Type: timeline.EntityPlace,
				Attributes: []timeline.Attribute{
					{
						Name:     "facebook_id",
						Value:    taggedPlace.FBID,
						Identity: true,
					},
				},
			}

			for _, label := range taggedPlace.LabelValues {
				switch label.Label {
				case "Place name":
					place.Name = label.Value
				case "Visit time":
					visitItem.Timestamp = time.Unix(int64(label.TimestampValue), 0).UTC()
				case "Name of application used to tag this place":
					visitItem.Metadata = timeline.Metadata{
						"Source": label.Value,
					}
				}
			}

			g := &timeline.Graph{Item: visitItem}
			g.ToEntity(timeline.RelVisit, place)

			params.Pipeline <- g
		}
	}

	return nil
}

func (a Archive) processCheckins(ctx context.Context, tlDirEntry timeline.DirEntry, params timeline.ImportParams, pathsToTry []string) error {
	for _, pathToTry := range pathsToTry {
		file, err := tlDirEntry.Open(pathToTry)
		if errors.Is(err, fs.ErrNotExist) {
			continue // it's valid for an archive not to have this data
		}
		if err != nil {
			return fmt.Errorf("could not open known check-ins folder: %w - tried: %s", err, pathToTry)
		}
		defer file.Close()

		// the check-ins file, at least as of 2024-2025, has two possible formats: an array of objects,
		// or if there's only 1 check-in, it will have just that single object (no array)... sigh
		var checkIns fbCheckIns
		if err := json.NewDecoder(file).Decode(&checkIns); err != nil {
			// wasn't an array, try the single-object decode
			file.Close()
			file, err = tlDirEntry.Open(pathToTry)
			if err != nil {
				return fmt.Errorf("reopening check-in file to try alternate decoding target: %s: %w", pathToTry, err)
			}
			defer file.Close()
			var checkIn fbCheckIn
			if err := json.NewDecoder(file).Decode(&checkIn); err != nil {
				return err
			}
			checkIns = fbCheckIns{checkIn}
		}

		for _, checkIn := range checkIns {
			if err := ctx.Err(); err != nil {
				return err
			}

			visitItem := &timeline.Item{
				Owner:          a.owner,
				Classification: timeline.ClassLocation,
				Timestamp:      time.Unix(int64(checkIn.Timestamp), 0).UTC(),
				Metadata: timeline.Metadata{
					"Facebook ID": checkIn.FBID,
				},
			}

			place := &timeline.Entity{
				Type: timeline.EntityPlace,
			}

			for _, label := range checkIn.LabelValues {
				switch label.Label {
				case "Message":
					visitItem.Content.Data = timeline.StringData(strings.TrimSpace(label.Value))
				case "Place tags":
					for _, dictLabel := range label.Dict {
						switch dictLabel.Label {
						case "Coordinates":
							lat, lon, err := parseCoordsFromDictString(dictLabel.Value)
							if err != nil {
								params.Log.Error("invalid check-in coordinates", zap.Error(err))
								continue
							}
							if lat != 0 && lon != 0 {
								visitItem.Location = timeline.Location{
									Longitude: &lon,
									Latitude:  &lat,
								}
							}
						case "Address":
							place.Attributes = append(place.Attributes, timeline.Attribute{
								Name:  "address",
								Value: strings.TrimSpace(dictLabel.Value),
							})
						case "Name":
							place.Name = strings.TrimSpace(dictLabel.Value)
						}
					}
				}
			}

			g := &timeline.Graph{Item: visitItem}
			if place.Name != "" || len(place.Attributes) > 0 {
				g.ToEntity(timeline.RelVisit, place)
			}

			params.Pipeline <- g
		}
	}

	return nil
}

// loadProfileInfo loads the profile info found within the DirEntry.
// It is possible for there to be none, in which case an empty profileInfo
// will be returned with no error (because no error occurred while looking
// for it; it is valid for multi-archive exports to not contain any except
// in just one of the archives).
func (Archive) loadProfileInfo(tlDirEntry timeline.DirEntry) (profileInfo, error) {
	for _, pathToTry := range []string{
		year2024ProfileInfoPath,
		pre2024ProfileInfoPath,
	} {
		file, err := tlDirEntry.Open(pathToTry)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return profileInfo{}, fmt.Errorf("could not open known places for the profile info: %w", err)
		}
		defer file.Close()

		var profileInfo profileInfo
		err = json.NewDecoder(file).Decode(&profileInfo)
		return profileInfo, err
	}
	return profileInfo{}, nil
}

func (a *Archive) setOwnerEntity(ctx context.Context, d timeline.DirEntry, options *Options) error {
	// we might need to get the username from the data source options, since one is not available to us in the data
	if options.Username == "" {
		if repoOwner, ok := ctx.Value(timeline.RepoOwnerCtxKey).(timeline.Entity); ok {
			if accountUsername, ok := repoOwner.AttributeValue("facebook_username").(string); ok {
				options.Username = accountUsername
			}
		}
	}

	profileInfo, err := a.loadProfileInfo(d)
	if err != nil {
		return err
	}
	if profileInfo.ProfileV2.Username == "" {
		if options.Username == "" {
			return errors.New("account username is needed, and cannot be empty")
		}

		// this archive doesn't contain profile info; that's expected with multi-archive exports
		// for all the archives but one; so use the user-supplied username
		a.owner = timeline.Entity{
			Attributes: []timeline.Attribute{
				{
					Name:     "facebook_username",
					Value:    options.Username,
					Identity: true,
				},
			},
		}
		return nil
	}

	// these have to match to ensure the imported data is attributed consistently to the correct owner entity
	if profileInfo.ProfileV2.Username != options.Username {
		return fmt.Errorf("configured username (%s) does not match what is in the profile manifest: %q",
			options.Username, profileInfo.ProfileV2.Username)
	}

	name := FixString(profileInfo.ProfileV2.Name.FullName)
	a.owner = timeline.Entity{
		Name: name,
		Attributes: []timeline.Attribute{
			{
				Name:     "facebook_username",
				Value:    profileInfo.ProfileV2.Username,
				Identity: true, // the data export only gives us this info for the owner, so it is the identity, but only for this user
			},
			{
				Name:        "facebook_name",
				Value:       name,
				Identifying: true, // this is not a great identifier, but it's what we're given throughout the data archive
			},
			{
				Name:  timeline.AttributeGender,
				Value: strings.ToLower(profileInfo.ProfileV2.Gender.Pronoun),
			},
			{
				Name:  "birth_place",
				Value: profileInfo.ProfileV2.Hometown.Name,
			},
		},
	}

	if profileInfo.ProfileV2.Birthday.Month != 0 &&
		profileInfo.ProfileV2.Birthday.Day != 0 {
		bdate := time.Date(
			profileInfo.ProfileV2.Birthday.Year,
			time.Month(profileInfo.ProfileV2.Birthday.Month),
			profileInfo.ProfileV2.Birthday.Day,
			0, 0, 0, 0, time.Local)
		a.owner.Attributes = append(a.owner.Attributes, timeline.Attribute{
			Name:  "birth_date",
			Value: bdate,
		})
	}

	for _, email := range profileInfo.ProfileV2.Emails.Emails {
		a.owner.Attributes = append(a.owner.Attributes, timeline.Attribute{
			Name:        timeline.AttributeEmail,
			Value:       email,
			Identifying: true,
		})
	}
	for _, email := range profileInfo.ProfileV2.Emails.PreviousEmails {
		a.owner.Attributes = append(a.owner.Attributes, timeline.Attribute{
			Name:        timeline.AttributeEmail,
			Value:       email,
			Identifying: true,
		})
	}
	for _, website := range profileInfo.ProfileV2.Websites {
		a.owner.Attributes = append(a.owner.Attributes, timeline.Attribute{
			Name:  "website",
			Value: website.Address,
		})
	}

	return nil
}

// FixString fixes a malformed string created by decoding UTF-8-encoded JSON string
// values as UTF-16 strings. JSON string values *should* be encoded as UTF-16:
// https://datatracker.ietf.org/doc/html/rfc7159#section-7 -- but as of January 2023,
// Facebook's account archive exporter encodes emoji incorrectly with UTF-8 escapes.
// For example, code point U+1F642 is encoded as "\u00f0\u009f\u0099\u0082" instead of
// "\uD83D\uDE42" -- resulting in garbage like "ð". This function transforms the string
// to runes, then back to bytes as long as their rune value is < 255.
//
// Thanks to Jorropo on the Gophers Slack for helping me figure this out.
//
// TODO: what should we do in case of an error? continue, or would the whole string be malformed after?
func FixString(malformed string) string {
	const maxByte = 255
	asRunes := []rune(malformed)
	final := make([]byte, len(asRunes))
	for i, r := range asRunes {
		if r > maxByte {
			continue // TODO: FIXME: Is this the best thing to do? Would the rest of the string be corrupted?
		}
		final[i] = byte(r)
	}
	return string(final)
}

var wroteOnOtherTimelineRegex = regexp.MustCompile(`.* wrote on (.*)'s timeline.`)
