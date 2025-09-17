package facebook

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// places_you_have_been_tagged_in.json (year 2024+)
type fbTaggedPlaces []struct {
	Media       []any          `json:"media"`
	LabelValues []fbLabelValue `json:"label_values"`
	FBID        string         `json:"fbid"` // Facebook ID
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
			Place struct {
				Name       string `json:"name"`
				Coordinate struct {
					Latitude  float64 `json:"latitude"`
					Longitude float64 `json:"longitude"`
				} `json:"coordinate"`
				Address string `json:"address"`
				URL     string `json:"url"`
			} `json:"place"`
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
			EXIFData []fbEXIFData `json:"exif_data"`
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
	// the media item might not be in this archive if it's a multi-archive export, so set a retrieval key so we can
	// fill the item in later
	retKey := retrievalKey(d, m.URI)
	item.Retrieval.SetKey(retKey)
	item.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
		"intermediate_location": timeline.UpdatePolicyPreferExisting,
		"timestamp":             timeline.UpdatePolicyPreferIncoming,
		"location":              timeline.UpdatePolicyPreferIncoming,
		"owner":                 timeline.UpdatePolicyPreferIncoming,
		"data":                  timeline.UpdatePolicyPreferExisting,
		"metadata":              timeline.UpdatePolicyPreferIncoming,
	}

	item.IntermediateLocation = m.URI

	if m.CreationTimestamp > 0 {
		item.Timestamp = time.Unix(m.CreationTimestamp, 0).UTC()
	}

	if item.Content.Data == nil {
		item.Content = timeline.ItemData{
			Filename: path.Base(m.URI),
			// don't set the Data func here, because it might not be in this archive at all;
			// we traverse media folder separately and use retrieval key to fill in the data file
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
				item.Timestamp = time.Unix(exif.TakenTimestamp, 0).UTC()

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
				item.Metadata["Modified"] = time.Unix(exif.ModifiedTimestamp, 0).UTC()
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
			item.Metadata["Upload timestamp"] = time.Unix(exif.UploadTimestamp, 0).UTC()
		}
	}
}

func retrievalKey(d timeline.DirEntry, pathOrURI string) string {
	archiveName := filepath.Base(d.FullPath())
	splitAt := strings.LastIndex(archiveName, "-")
	exportID := archiveName
	if splitAt > -1 {
		exportID = archiveName[:splitAt]
	}
	return fmt.Sprintf("facebook::%s::%s", exportID, pathOrURI)
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

type fbAlbumMeta struct {
	Name   string `json:"name"`
	Photos []struct {
		URI               string `json:"uri"`
		CreationTimestamp int64  `json:"creation_timestamp"`
		MediaMetadata     struct {
			PhotoMetadata struct {
				EXIFData []fbEXIFData `json:"exif_data"`
			} `json:"photo_metadata"`
		} `json:"media_metadata"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"photos"`
	CoverPhoto struct {
		URI               string `json:"uri"`
		CreationTimestamp int64  `json:"creation_timestamp"`
		MediaMetadata     struct {
			PhotoMetadata struct {
				EXIFData []fbEXIFData `json:"exif_data"`
			} `json:"photo_metadata"`
		} `json:"media_metadata"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"cover_photo"`
	LastModifiedTimestamp int64  `json:"last_modified_timestamp"`
	Description           string `json:"description"`
}

type fbEXIFData struct {
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
}

type fbLabelValue struct {
	Title          string `json:"title,omitempty"`
	Label          string `json:"label"`
	Value          string `json:"value,omitempty"`
	TimestampValue int    `json:"timestamp_value,omitempty"`
	Dict           []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"dict,omitempty"`
}

type fbCheckIns []fbCheckIn

type fbCheckIn struct {
	Timestamp   int            `json:"timestamp"`
	Media       []any          `json:"media"`
	LabelValues []fbLabelValue `json:"label_values"`
	FBID        string         `json:"fbid"`
}

// parseCoordsFromDictString parses coordinates from the dict label value.
// Expected format: "(38.575804323267 , -121.4804199327)" which is (lat, lon) order
//
//nolint:mnd
func parseCoordsFromDictString(coordStr string) (lat, lon float64, err error) {
	// remove fluff
	coordStr = strings.TrimSpace(coordStr)
	coordStr = strings.TrimPrefix(coordStr, "(")
	coordStr = strings.TrimSuffix(coordStr, ")")

	// split lat/lon and parse each float
	parts := strings.SplitN(coordStr, ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lat/lon string: %q", coordStr)
	}
	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid latitude float: %w", err)
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid longitude float: %w", err)
	}

	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("invalid latitude: %f", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("invalid longitude: %f", lon)
	}

	return
}
