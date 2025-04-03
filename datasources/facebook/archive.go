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
)

const (
	year2024ProfileInfoPath             = "personal_information/profile_information/profile_information.json"
	year2024YourUncategorizedPhotosPath = "your_facebook_activity/posts/your_uncategorized_photos.json"
	year2024YourVideosPath              = "your_facebook_activity/posts/your_videos.json"
	year2024YourPostsPrefix             = "your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_"
	year2024MessagesPrefix              = "your_facebook_activity/messages"
)

// Archive implements the importer for Facebook archives.
type Archive struct {
	owner timeline.Entity
}

// Recognize returns whether the input file is recognized.
func (Archive) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if dirEntry.FileExists(pre2024ProfileInfoPath) ||
		dirEntry.FileExists(year2024ProfileInfoPath) {
		return timeline.Recognition{Confidence: 1}, nil
	}
	return timeline.Recognition{}, nil
}

// FileImport imports the data in the file.
func (a Archive) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// dsOpt := opt.DataSourceOptions.(*Options)

	if err := a.setOwnerEntity(dirEntry); err != nil {
		return err
	}

	// start with oldest supported archive version
	postsFilePrefix := pre2024YourPostsPrefix

	// posts
	for i := 1; i < 10000; i++ {
		postsFilename := fmt.Sprintf("%s%d.json", postsFilePrefix, i)

		postsFile, err := dirEntry.FS.Open(postsFilename)
		if errors.Is(err, fs.ErrNotExist) && postsFilePrefix == pre2024YourPostsPrefix {
			// try newer version
			postsFilePrefix = year2024YourPostsPrefix
			postsFilename = fmt.Sprintf("%s%d.json", postsFilePrefix, i)
			postsFile, err = dirEntry.FS.Open(postsFilename)
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

	// uncategorized photos
	if err := a.processPhotosOrVideos(ctx, dirEntry, params, []string{
		pre2024YourUncategorizedPhotosPath,
		year2024YourUncategorizedPhotosPath,
	}, fbYourUncategorizedPhotos{}); err != nil {
		return fmt.Errorf("processing uncategorized photos: %w", err)
	}

	// (uncategorized) videos
	if err := a.processPhotosOrVideos(ctx, dirEntry, params, []string{
		pre2024YourVideosPath,
		year2024YourVideosPath,
	}, fbYourVideos{}); err != nil {
		return fmt.Errorf("processing videos: %w", err)
	}

	// messages
	err := GetMessages("facebook", dirEntry, params)
	if err != nil {
		return err
	}

	return nil
}

func (a Archive) processPhotosOrVideos(ctx context.Context, d timeline.DirEntry, opt timeline.ImportParams, pathsToTry []string, unmarshalInto any) error {
	const archiveVersionsSupported = 2

	if len(pathsToTry) != archiveVersionsSupported {
		return fmt.Errorf("should have %d paths to try, since we support %d archive versions; got %d",
			archiveVersionsSupported, archiveVersionsSupported, len(pathsToTry))
	}

	file, err := d.FS.Open(pathsToTry[0])
	if errors.Is(err, fs.ErrNotExist) {
		// try newer archive version
		file, err = d.FS.Open(pathsToTry[1])
	}
	if err != nil {
		return err
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(&unmarshalInto); err != nil {
		return err
	}

	var medias []fbArchiveMedia
	switch v := unmarshalInto.(type) {
	case fbYourUncategorizedPhotos:
		medias = v.OtherPhotosV2
	case fbYourVideos:
		medias = v.VideosV2
	}

	for _, media := range medias {
		if err := ctx.Err(); err != nil {
			return err
		}

		item := &timeline.Item{
			Owner: a.owner,
		}

		media.fillItem(item, d, "", opt.Log)

		opt.Pipeline <- &timeline.Graph{Item: item}
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
			Timestamp:      time.Unix(post.Timestamp, 0),
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
						Name:     "facebook_name",
						Value:    matches[1],
						Identity: true,
					},
				},
			})
		}

		for _, attachment := range post.Attachments {
			attachedItem := &timeline.Item{
				Owner:    a.owner,
				Metadata: make(timeline.Metadata),
			}
			var allText []string

			for _, attachData := range attachment.Data {
				if attachData.Text != "" {
					text := FixString(attachData.Text)
					if descStr, ok := attachedItem.Metadata["Description"].(string); ok && text != descStr {
						allText = append(allText, text)
					}
				}
				if attachData.Media.URI != "" {
					attachData.Media.fillItem(attachedItem, d, postText, params.Log)
				}
				if attachData.ExternalContext.URL != "" {
					attachedItem.Content.Data = timeline.StringData(attachData.ExternalContext.Name)
					attachedItem.Metadata["URL"] = attachData.ExternalContext.URL
					attachedItem.Classification = timeline.ClassLocation
					attachedItem.Timestamp = item.Timestamp
				}
			}

			newDescription := strings.Join(allText, "\n")
			if existingDesc, ok := attachedItem.Metadata["Description"].(string); ok && existingDesc != "" {
				newDescription += "\n\n" + existingDesc
			}
			attachedItem.Metadata["Description"] = newDescription

			ig.ToItem(timeline.RelAttachment, attachedItem)
		}

		params.Pipeline <- ig
	}

	return nil
}

func (Archive) loadProfileInfo(d timeline.DirEntry) (profileInfo, error) {
	file, err := d.Open(pre2024ProfileInfoPath)
	if errors.Is(err, fs.ErrNotExist) {
		// try another archive version
		file, err = d.Open(year2024ProfileInfoPath)
	}
	if err != nil {
		return profileInfo{}, err
	}
	defer file.Close()

	var profileInfo profileInfo
	err = json.NewDecoder(file).Decode(&profileInfo)
	return profileInfo, err
}

func (a *Archive) setOwnerEntity(d timeline.DirEntry) error {
	profileInfo, err := a.loadProfileInfo(d)
	if err != nil {
		return err
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
			Name:        "birth_date",
			Value:       bdate,
			Identifying: true,
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

// Generated January 2023
type profileInfo struct {
	ProfileV2 struct {
		Name struct {
			FullName   string `json:"full_name"`
			FirstName  string `json:"first_name"`
			MiddleName string `json:"middle_name"`
			LastName   string `json:"last_name"`
		} `json:"name"`
		Emails struct {
			Emails          []string `json:"emails"`
			PreviousEmails  []string `json:"previous_emails"`
			PendingEmails   []any    `json:"pending_emails"`
			AdAccountEmails []any    `json:"ad_account_emails"`
		} `json:"emails"`
		Birthday fbDate `json:"birthday"`
		Gender   struct {
			GenderOption string `json:"gender_option"`
			Pronoun      string `json:"pronoun"`
		} `json:"gender"`
		PreviousNames []any `json:"previous_names"`
		OtherNames    []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Timestamp int    `json:"timestamp"`
		} `json:"other_names"`
		Hometown struct {
			Name      string `json:"name"`
			Timestamp int    `json:"timestamp"`
		} `json:"hometown"`
		Relationship struct {
			Status      string `json:"status"`
			Partner     string `json:"partner"`
			Anniversary fbDate `json:"anniversary"`
			Timestamp   int    `json:"timestamp"`
		} `json:"relationship"`
		FamilyMembers []struct {
			Name      string `json:"name"`
			Relation  string `json:"relation"`
			Timestamp int    `json:"timestamp"`
		} `json:"family_members"`
		EducationExperiences []struct {
			Name           string   `json:"name"`
			StartTimestamp int      `json:"start_timestamp,omitempty"`
			EndTimestamp   int      `json:"end_timestamp"`
			Graduated      bool     `json:"graduated"`
			Concentrations []string `json:"concentrations"`
			Degree         string   `json:"degree,omitempty"`
			SchoolType     string   `json:"school_type"`
			Timestamp      int      `json:"timestamp"`
		} `json:"education_experiences"`
		WorkExperiences []any `json:"work_experiences"`
		BloodInfo       struct {
			BloodDonorStatus string `json:"blood_donor_status"`
		} `json:"blood_info"`
		Websites []struct {
			Address string `json:"address"`
		} `json:"websites"`
		PhoneNumbers []struct {
			PhoneType   string `json:"phone_type"`
			PhoneNumber string `json:"phone_number"`
			Verified    bool   `json:"verified"`
		} `json:"phone_numbers"`
		Username              string `json:"username"`
		RegistrationTimestamp int    `json:"registration_timestamp"`
		ProfileURI            string `json:"profile_uri"`
	} `json:"profile_v2"`
}

type fbDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

type yourPosts []struct {
	Timestamp   int64 `json:"timestamp"`
	Attachments []struct {
		Data []struct {
			Text            string `json:"text"`
			ExternalContext struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"external_context"`
			Media fbArchiveMedia `json:"media"`
		} `json:"data"`
	} `json:"attachments,omitempty"`
	Data []struct {
		Post string `json:"post"`
	} `json:"data"`
	Title string `json:"title,omitempty"`
	Tags  []struct {
		Name string `json:"name"`
	} `json:"tags,omitempty"`
}

type fbYourUncategorizedPhotos struct {
	OtherPhotosV2 []fbArchiveMedia `json:"other_photos_v2"`
}

type fbYourVideos struct {
	VideosV2 []fbArchiveMedia `json:"videos_v2"`
}

type fbArchiveMedia struct {
	URI               string `json:"uri"`
	CreationTimestamp int64  `json:"creation_timestamp"`
	MediaMetadata     struct {
		PhotoMetadata *struct {
			EXIFData []struct {
				ISO               int     `json:"iso"`
				FocalLength       string  `json:"focal_length"`
				UploadIP          string  `json:"upload_ip"`
				TakenTimestamp    int64   `json:"taken_timestamp"`
				ModifiedTimestamp int64   `json:"modified_timestamp"`
				CameraMake        string  `json:"camera_make"`
				CameraModel       string  `json:"camera_model"`
				Exposure          string  `json:"exposure"`
				FStop             string  `json:"f_stop"`
				Orientation       int     `json:"orientation"`
				OriginalWidth     int     `json:"original_width"`
				OriginalHeight    int     `json:"original_height"`
				Latitude          float64 `json:"latitude"`
				Longitude         float64 `json:"longitude"`
			} `json:"exif_data"`
		} `json:"photo_metadata"`
		VideoMetadata *struct {
			EXIFData []struct {
				UploadIP        string `json:"upload_ip"`
				UploadTimestamp int64  `json:"upload_timestamp"`
			} `json:"exif_data"`
		} `json:"video_metadata"`
	} `json:"media_metadata"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (m fbArchiveMedia) fillItem(item *timeline.Item, d timeline.DirEntry, postText string, logger *zap.Logger) {
	if m.CreationTimestamp > 0 {
		item.Timestamp = time.Unix(m.CreationTimestamp, 0)
	}

	if item.Content.Data == nil {
		item.Content = timeline.ItemData{
			Filename: path.Base(m.URI),
			Data: func(_ context.Context) (io.ReadCloser, error) {
				return d.FS.Open(m.URI)
			},
		}
	}

	if item.Metadata == nil {
		item.Metadata = make(timeline.Metadata)
	}

	item.Metadata["Title"] = FixString(m.Title)
	if desc := FixString(m.Description); desc != postText {
		// TODO: this filter probably doesn't work with multiple attachment data where some are text with the same description
		item.Metadata["Description"] = FixString(m.Description)
	}

	// TODO: use media importer functions for this as well...
	// Collect metadata... while we do, prefer TakenTimestamp over CreationTimestamp;
	// I think TakenTimestamp is when the media was captured, and CreationTimestamp is
	// when the post or attachment was created on Facebook. I think.
	// We also include all the EXIF data Facebook gives us because they strip the EXIF
	// data from the files they give us.
	if m.MediaMetadata.PhotoMetadata != nil {
		for _, exif := range m.MediaMetadata.PhotoMetadata.EXIFData {
			if exif.TakenTimestamp != 0 { // negative is valid! (pre-1970)
				item.Timestamp = time.Unix(exif.TakenTimestamp, 0)

				// I've seen this happen where the value is -62169958800 (for multiple items!)
				// which results in a Very Wrong Timestamp. I don't know what to make of this
				// value, so we have to simply throw it away.
				if item.Timestamp.Year() <= 0 {
					logger.Warn("data source provided TakenTimestamp that is before year 0; not using it", zap.Time("taken_timestamp", item.Timestamp))
					item.Timestamp = time.Time{}
				}
			}
			if lat := exif.Latitude; lat != 0 {
				item.Location.Latitude = &lat
			}
			if lon := exif.Longitude; lon != 0 {
				item.Location.Longitude = &lon
			}
			item.Metadata["ISO"] = exif.ISO
			item.Metadata["Focal length"] = exif.FocalLength
			item.Metadata["Upload IP"] = exif.UploadIP
			if exif.ModifiedTimestamp > 0 {
				item.Metadata["Modified"] = time.Unix(exif.ModifiedTimestamp, 0)
			}
			item.Metadata["Camera make"] = exif.CameraMake
			item.Metadata["Camera model"] = exif.CameraModel
			item.Metadata["Exposure"] = exif.Exposure
			item.Metadata["F-stop"] = exif.FStop
			item.Metadata["Orientation"] = exif.Orientation
			item.Metadata["Original width"] = exif.OriginalWidth
			item.Metadata["Original height"] = exif.OriginalHeight
		}
	} else if m.MediaMetadata.VideoMetadata != nil {
		for _, exif := range m.MediaMetadata.VideoMetadata.EXIFData {
			item.Metadata["Upload IP"] = exif.UploadTimestamp
			item.Metadata["Upload timestamp"] = time.Unix(exif.UploadTimestamp, 0)
		}
	}
}

type fbMessengerThread struct {
	Participants []struct {
		Name string `json:"name"`
	} `json:"participants"`
	Messages []struct {
		SenderName  string `json:"sender_name"`
		TimestampMS int64  `json:"timestamp_ms"`
		IsUnsent    bool   `json:"is_unsent,omitempty"`
		Content     string `json:"content,omitempty"`
		Share       struct {
			Link      string `json:"link"`
			ShareText string `json:"share_text"`
		} `json:"share,omitempty"`
		Reactions []struct {
			Reaction string `json:"reaction"`
			Actor    string `json:"actor"`
		} `json:"reactions,omitempty"`
		Photos     []fbArchiveMedia `json:"photos,omitempty"`
		Videos     []fbArchiveMedia `json:"videos,omitempty"`
		GIFs       []fbArchiveMedia `json:"gifs,omitempty"`
		AudioFiles []fbArchiveMedia `json:"audio_files,omitempty"`
		Sticker    fbArchiveMedia   `json:"sticker,omitempty"`
	} `json:"messages"`
	Title              string `json:"title"`
	IsStillParticipant bool   `json:"is_still_participant"`
	ThreadPath         string `json:"thread_path"`
	MagicWords         []any  `json:"magic_words"`
}

func (thread fbMessengerThread) sentTo(senderName, dsName string) []*timeline.Entity {
	var sentTo []*timeline.Entity //nolint:prealloc // bug filed: https://github.com/alexkohler/prealloc/issues/30
	for _, participant := range thread.Participants {
		participantName := FixString(participant.Name)
		if participantName == senderName {
			continue
		}
		sentTo = append(sentTo, &timeline.Entity{
			Name: participantName,
			Attributes: []timeline.Attribute{
				{
					Name:     dsName + "_name",
					Value:    participantName,
					Identity: true,
				},
			},
		})
	}
	return sentTo
}

var wroteOnOtherTimelineRegex = regexp.MustCompile(`.* wrote on (.*)'s timeline.`)
