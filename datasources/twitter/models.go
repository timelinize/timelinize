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

package twitter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

type tweetFromAPI struct {
	InReplyToUserID  string `json:"in_reply_to_user_id,omitempty"`
	ReferencedTweets []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"referenced_tweets,omitempty"`
	Text          string `json:"text"`
	PublicMetrics struct {
		RetweetCount int `json:"retweet_count"`
		ReplyCount   int `json:"reply_count"`
		LikeCount    int `json:"like_count"`
		QuoteCount   int `json:"quote_count"`
	} `json:"public_metrics"`
	Lang           string    `json:"lang"`
	ConversationID string    `json:"conversation_id"`
	CreatedAt      time.Time `json:"created_at"`
	ID             string    `json:"id"`
	Entities       struct {
		Mentions []struct {
			Start    int    `json:"start"`
			End      int    `json:"end"`
			Username string `json:"username"`
			ID       string `json:"id"`
		} `json:"mentions"`
		URLs []struct {
			Start       int    `json:"start"`
			End         int    `json:"end"`
			URL         string `json:"url"`
			ExpandedURL string `json:"expanded_url"`
			DisplayURL  string `json:"display_url"`
			Images      []struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"images"`
			Status      int    `json:"status"`
			Title       string `json:"title"`
			Description string `json:"description"`
			UnwoundURL  string `json:"unwound_url"`
		} `json:"urls"`
		Annotations []struct {
			Start          int     `json:"start"`
			End            int     `json:"end"`
			Probability    float64 `json:"probability"`
			Type           string  `json:"type"`
			NormalizedText string  `json:"normalized_text"`
		} `json:"annotations"`
	} `json:"entities,omitempty"`
	AuthorID           string `json:"author_id"`
	ReplySettings      string `json:"reply_settings"`
	Source             string `json:"source"`
	PossiblySensitive  bool   `json:"possibly_sensitive"`
	ContextAnnotations []struct {
		Domain idNameDesc `json:"domain"`
		Entity idNameDesc `json:"entity"`
	} `json:"context_annotations,omitempty"`
	Attachments struct {
		MediaKeys []string `json:"media_keys"`
	} `json:"attachments,omitempty"`
	Geo struct {
		Coordinates struct {
			Type        string    `json:"type"`        // "Point"
			Coordinates []float64 `json:"coordinates"` // latitude, longitude pair
		} `json:"coordinates"`
		PlaceID string `json:"place_id,omitempty"`
	} `json:"geo,omitempty"`
}

func (t tweetFromAPI) owner(page userTweetsResponsePage) timeline.Entity {
	owner := timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     identityAttribute,
				Value:    t.AuthorID,
				Identity: true,
			},
		},
	}
	for _, u := range page.Includes.Users {
		if u.Data.ID == t.AuthorID {
			owner.Name = u.Data.Name
			owner.Attributes = append(owner.Attributes, timeline.Attribute{
				Name:  "twitter_username",
				Value: u.Data.Username,
			})
			break
		}
	}
	return owner
}

type idNameDesc struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type userTweetsResponsePage struct {
	Data []tweetFromAPI `json:"data"`

	Includes struct {
		Tweets []tweetFromAPI   `json:"tweets"`
		Users  []twitterAccount `json:"users"`
		Media  []struct {
			MediaKey        string `json:"media_key"`
			Height          int    `json:"height"`
			URL             string `json:"url,omitempty"`
			Type            string `json:"type"`
			Width           int    `json:"width"`
			DurationMs      int    `json:"duration_ms,omitempty"`
			PreviewImageURL string `json:"preview_image_url,omitempty"`
			PublicMetrics   struct {
				ViewCount int `json:"view_count"`
			} `json:"public_metrics,omitempty"`
		} `json:"media"`
		Places []struct {
			Geo struct { // GeoJSON format (look it up)
				Type       string    `json:"type"`
				BBox       []float64 `json:"bbox"` // bounding box is the rectangle (usually 4 points) that contain the object
				Properties struct {
				} `json:"properties"`
			} `json:"geo"`
			CountryCode string `json:"country_code"`
			Name        string `json:"name"`
			ID          string `json:"id"`
			PlaceType   string `json:"place_type"`
			Country     string `json:"country"`
			FullName    string `json:"full_name"`
		} `json:"places"`
	} `json:"includes"`

	Meta struct {
		NextToken   string `json:"next_token"`
		ResultCount int    `json:"result_count"`
		NewestID    string `json:"newest_id"`
		OldestID    string `json:"oldest_id"`
	} `json:"meta"`

	Errors []struct {
		ResourceType string `json:"resource_type"`
		Field        string `json:"field"`
		Title        string `json:"title"`
		Section      string `json:"section"`
		Detail       string `json:"detail"`
		Type         string `json:"type"`
	} `json:"errors"`
}

func (tweet tweetFromAPI) toItemGraph(page userTweetsResponsePage) *timeline.Graph {
	owner := tweet.owner(page)

	// get location info; prefer user's precise location if available, otherwise use place's geo info
	var geo timeline.Location
	if len(tweet.Geo.Coordinates.Coordinates) == 2 {
		geo.Latitude, geo.Longitude = &tweet.Geo.Coordinates.Coordinates[0], &tweet.Geo.Coordinates.Coordinates[1]
	} else if tweet.Geo.PlaceID != "" {
		for _, pl := range page.Includes.Places {
			if len(pl.Geo.BBox) == 4 {
				// TODO: we only support a single point, so find center of bounding box... supposedly they should go from SW to NE (counterclockwise)
			}
		}
	}

	it := &timeline.Item{
		ID:        tweet.ID,
		Timestamp: tweet.CreatedAt,
		Location:  geo,
		Owner:     owner,
		Metadata: timeline.Metadata{
			"Retweets": tweet.PublicMetrics.RetweetCount,
			"Quotes":   tweet.PublicMetrics.QuoteCount,
			"Likes":    tweet.PublicMetrics.LikeCount,
			"Source":   tweet.Source,
			"Language": tweet.Lang,
		},
	}
	if tweet.Text != "" {
		expandedText := tweet.Text

		// replace any shortened URLs with their fully-expanded (and unwound) form
		// (according to Twitter API docs, "unwound" means after following redirects
		// from URL shorteners like bitly, etc.)
		for _, urlEnt := range tweet.Entities.URLs {
			textToReplace := tweet.Text[urlEnt.Start:urlEnt.End]
			expandedText = strings.Replace(expandedText, textToReplace, urlEnt.UnwoundURL, 1)
		}

		it.Content = timeline.ItemData{
			Data: timeline.StringData(expandedText),
		}
	}

	ig := &timeline.Graph{Item: it}

	// attach media elements to the main tweet's item graph
	for _, mediaKey := range tweet.Attachments.MediaKeys {
		// find this media item in the attachments list
		for _, attachment := range page.Includes.Media {
			// skip attachments that aren't the one we're looking for,
			// or which have an empty URL (sigh)
			if attachment.MediaKey != mediaKey || attachment.URL == "" {
				continue
			}

			mediaItem := &timeline.Item{
				ID:        attachment.MediaKey,
				Timestamp: tweet.CreatedAt,
				Owner:     it.Owner,
				Content: timeline.ItemData{
					Filename: path.Base(attachment.URL),
					Data: func(context.Context) (io.ReadCloser, error) {
						resp, err := http.Get(attachment.URL)
						if err != nil {
							return nil, err
						}
						return resp.Body, nil
					},
				},
				Metadata: timeline.Metadata{
					"Width":                   attachment.Width,
					"Height":                  attachment.Height,
					"Duration (milliseconds)": attachment.DurationMs,
					"Views":                   attachment.PublicMetrics.ViewCount,
				},
			}

			ig.ToItem(timeline.RelAttachment, mediaItem)
			break
		}
	}

	return ig
}

func (page userTweetsResponsePage) process(itemChan chan<- *timeline.Graph, opt Options) error {
nextTweet:
	for _, tweet := range page.Data {
		// skip retweets unless configured
		if !opt.Retweets {
			for _, ref := range tweet.ReferencedTweets {
				if ref.Type == "retweeted" || ref.Type == "quoted" {
					continue nextTweet
				}
			}
		}

		ig := tweet.toItemGraph(page)

		// if this tweet is in reply to another tweet, we add that
		// other tweet to the graph; but since our unidirectional
		// relation ReplyTo goes FROM the first message TO the reply,
		// we need to actually create a graph for the first message,
		// then connect the original tweet which is the reply; this
		// is a little awkward since we're starting with the reply
		// and getting its "parent", which is backwards from how it
		// was designed (start with parent, get replies).

		// TODO: skip replies unless configured to have them

		// attach tweet this tweet is in reply to (if any)
		for _, ref := range tweet.ReferencedTweets {
			if ref.Type != "replied_to" {
				continue
			}

			// find the referenced tweet in the list of attached tweets
			for _, refTweet := range page.Includes.Tweets {
				if refTweet.ID != ref.ID {
					continue
				}

				// TODO: I think this relationship is backwards... double-check this!
				refTweetItemGraph := refTweet.toItemGraph(page)
				refTweetItemGraph.Edges = append(refTweetItemGraph.Edges, timeline.Relationship{
					Relation: timeline.RelReply,
					To:       ig,
				})

				// TODO: How much of the conversation can/should we do? Maybe make it configurable?

				// this will add both the first tweet and the reply to
				// the timeline, then we'll end up sending the reply again,
				// but that should be OK since the timeline should be able
				// to deduplicate for us
				itemChan <- refTweetItemGraph
			}
		}

		itemChan <- ig
	}

	return nil
}

type tweet struct {
	Contributors         any               `json:"contributors"`
	Coordinates          *tweetGeo         `json:"coordinates,omitempty"`
	CreatedAt            string            `json:"created_at"`
	DisplayTextRange     []transInt        `json:"display_text_range"`
	Entities             *twitterEntities  `json:"entities,omitempty"` // DO NOT USE (https://developer.twitter.com/en/docs/tweets/data-dictionary/overview/entities-object.html#media)
	ExtendedEntities     *extendedEntities `json:"extended_entities,omitempty"`
	FavoriteCount        transInt          `json:"favorite_count"`
	Favorited            bool              `json:"favorited"`
	FullText             string            `json:"full_text"` // tweet_mode=extended (https://developer.twitter.com/en/docs/tweets/tweet-updates)
	InReplyToScreenName  string            `json:"in_reply_to_screen_name,omitempty"`
	InReplyToStatusID    transInt          `json:"in_reply_to_status_id,omitempty"`
	InReplyToStatusIDStr string            `json:"in_reply_to_status_id_str,omitempty"`
	InReplyToUserID      transInt          `json:"in_reply_to_user_id,omitempty"`
	InReplyToUserIDStr   string            `json:"in_reply_to_user_id_str,omitempty"`
	IsQuoteStatus        bool              `json:"is_quote_status"`
	Lang                 string            `json:"lang"`
	Place                any               `json:"place"`
	PossiblySensitive    bool              `json:"possibly_sensitive,omitempty"`
	RetweetCount         transInt          `json:"retweet_count"`
	Retweeted            bool              `json:"retweeted"`        // always false for some reason
	RetweetedStatus      *tweet            `json:"retweeted_status"` // API: contains full_text of a retweet (otherwise is truncated)
	Source               string            `json:"source"`
	Text                 string            `json:"text"`      // As of Feb. 2019, Twitter API default; truncated at ~140 chars (see FullText)
	Truncated            bool              `json:"truncated"` // API: always false in tweet_mode=extended, even if full_text is truncated (retweets)
	TweetID              transInt          `json:"id"`
	TweetIDStr           string            `json:"id_str"`
	User                 *twitterUser      `json:"user"`
	WithheldCopyright    bool              `json:"withheld_copyright,omitempty"`
	WithheldInCountries  []string          `json:"withheld_in_countries,omitempty"`
	WithheldScope        string            `json:"withheld_scope,omitempty"`

	createdAtParsed time.Time
	owner           timeline.Entity
	source          string // "api|archive"
}

func (t *tweet) id() string {
	return t.TweetIDStr
}

// content returns the text of the tweet, or, if text is empty, it
// returns the first media item as data (if any).
func (t *tweet) content() timeline.ItemData {
	var data timeline.ItemData
	if txt := t.text(); txt != "" {
		data.Data = timeline.StringData(txt)
	} else if t.ExtendedEntities != nil && len(t.ExtendedEntities.Media) > 0 {
		data.Filename = t.ExtendedEntities.Media[0].fileName()
		data.Data = t.ExtendedEntities.Media[0].fileReader
		data.MediaType = t.ExtendedEntities.Media[0].mediaType()
	}
	return data
}

func (t *tweet) isRetweet() bool {
	if t.Retweeted || t.RetweetedStatus != nil {
		return true
	}
	// TODO: For some reason, when exporting one's Twitter data,
	// it always sets "retweeted" to false, even when "full_text"
	// clearly shows it's a retweet by prefixing it with "RT @"
	// - this seems like a bug with Twitter's exporter... okay
	// actually the API does it too, that's dumb
	return strings.HasPrefix(t.rawText(), "RT @")
}

func (t *tweet) isEmpty() bool {
	return strings.TrimSpace(t.text()) == "" &&
		(t.ExtendedEntities == nil || len(t.ExtendedEntities.Media) == 0)
}

// text returns the full text of the tweet, with entities added inline.
func (t *tweet) text() string {
	txt := t.rawText()
	expandedText := html.UnescapeString(txt)

	// replace any annoying t.co shortened URLs with their fully-expanded form
	if t.Entities != nil {
		for _, urlEnt := range t.Entities.URLs {
			if len(urlEnt.Indices) != 2 {
				continue
			}
			textToReplace := txt[urlEnt.Indices[0]:urlEnt.Indices[1]]
			expandedText = strings.Replace(expandedText, textToReplace, urlEnt.ExpandedURL, 1)
		}
	}

	// replace any links to embedded media with the full URL
	// (although, this is not necessary, because we link the
	// media in our own way, without a URL)
	if t.ExtendedEntities != nil {
		for _, ent := range t.ExtendedEntities.Media {
			if len(ent.Indices) != 2 {
				continue
			}
			textToReplace := txt[ent.Indices[0]:ent.Indices[1]]
			expandedText = strings.Replace(expandedText, textToReplace, ent.ExpandedURL, 1)
		}
	}

	return expandedText
}

// rawText returns the "raw" text of the tweet, without
// replacing entities (but it does dereference any
// retweeted status to obtain its text, if present).
func (t *tweet) rawText() string {
	// sigh, retweets get truncated if they're tall,
	// so we have to get the full text from a subfield
	if t.RetweetedStatus != nil {
		return strings.TrimSpace(fmt.Sprintf("RT @%s %s",
			t.RetweetedStatus.User.ScreenName, t.RetweetedStatus.text()))
	}
	if t.FullText != "" {
		return t.FullText
	}
	return t.Text
}

// location returns the best guess for the tweet's location, because Twitter
// randomizes the order of the coordinates we can't always be sure which is which >:(
func (t *tweet) location() timeline.Location {
	var loc timeline.Location
	if t.Coordinates == nil {
		return loc
	}

	// grr, during dev I noticed that Twitter randomly orders the coordinate values,
	// so we only know which is which if one of them is > |90|.
	c0, err := strconv.ParseFloat(t.Coordinates.Coordinates[0], 64)
	if err != nil {
		return loc
	}
	c1, err := strconv.ParseFloat(t.Coordinates.Coordinates[1], 64)
	if err != nil {
		return loc
	}
	if math.Abs(c0) > 90 {
		loc.Latitude = &c1
		loc.Longitude = &c0
	} else {
		// if c1 > |90|, great, but if both are less than 90, we just don't know
		loc.Latitude = &c0
		loc.Longitude = &c1
	}

	return loc
}

type tweetGeo struct {
	Type        string   `json:"type"`
	Coordinates []string `json:"coordinates"` // TODO: these are not in any particular order! That's *GREAT*... sigh. My own export has 2 tweets with coords, and they're the same point, but both are in a different order
}

type tweetPlace struct {
	ID          string      `json:"id"`
	URL         string      `json:"url"`
	PlaceType   string      `json:"place_type"`
	Name        string      `json:"name"`
	FullName    string      `json:"full_name"`
	CountryCode string      `json:"country_code"`
	Country     string      `json:"country"`
	BoundingBox boundingBox `json:"bounding_box"`
}

type boundingBox struct {
	Type string `json:"type"`

	// "A series of longitude and latitude points, defining a box which will contain
	// the Place entity this bounding box is related to. Each point is an array in
	// the form of [longitude, latitude]. Points are grouped into an array per bounding
	// box. Bounding box arrays are wrapped in one additional array to be compatible
	// with the polygon notation."
	Coordinates [][][]float64 `json:"coordinates"`
}

type twitterEntities struct {
	Hashtags     []hashtagEntity     `json:"hashtags"`
	Symbols      []symbolEntity      `json:"symbols"`
	UserMentions []userMentionEntity `json:"user_mentions"`
	URLs         []urlEntity         `json:"urls"`
	Polls        []pollEntity        `json:"polls"`
}

type hashtagEntity struct {
	Indices []transInt `json:"indices"`
	Text    string     `json:"text"`
}

type symbolEntity struct {
	Indices []transInt `json:"indices"`
	Text    string     `json:"text"`
}

type urlEntity struct {
	URL         string            `json:"url"`
	ExpandedURL string            `json:"expanded_url"`
	DisplayURL  string            `json:"display_url"`
	Unwound     *urlEntityUnwound `json:"unwound,omitempty"`
	Indices     []transInt        `json:"indices"`
}

type urlEntityUnwound struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type userMentionEntity struct {
	Name       string     `json:"name"`
	ScreenName string     `json:"screen_name"`
	Indices    []transInt `json:"indices"`
	IDStr      string     `json:"id_str"`
	ID         transInt   `json:"id"`
}

type pollEntity struct {
	Options         []pollOption `json:"options"`
	EndDatetime     string       `json:"end_datetime"`
	DurationMinutes int          `json:"duration_minutes"`
}

type pollOption struct {
	Position int    `json:"position"`
	Text     string `json:"text"`
}

type extendedEntities struct {
	Media []*mediaItem `json:"media"`
}

type mediaItem struct {
	AdditionalMediaInfo *additionalMediaInfo `json:"additional_media_info,omitempty"`
	DisplayURL          string               `json:"display_url"`
	ExpandedURL         string               `json:"expanded_url"`
	Indices             []transInt           `json:"indices"`
	MediaID             transInt             `json:"id"`
	MediaIDStr          string               `json:"id_str"`
	MediaURL            string               `json:"media_url"`
	MediaURLHTTPS       string               `json:"media_url_https"`
	Sizes               mediaSizes           `json:"sizes"`
	SourceStatusID      transInt             `json:"source_status_id"`
	SourceStatusIDStr   string               `json:"source_status_id_str"`
	SourceUserID        transInt             `json:"source_user_id"`
	SourceUserIDStr     string               `json:"source_user_id_str"`
	Type                string               `json:"type"`
	URL                 string               `json:"url"`
	VideoInfo           *videoInfo           `json:"video_info,omitempty"`

	parent     *tweet
	readCloser io.ReadCloser // access to the media contents
}

func (m mediaItem) owner() timeline.Entity {
	if m.SourceUserIDStr == "" {
		// assume it is owned by owner of tweet it is contained in
		return m.parent.owner
	}
	return timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     identityAttribute,
				Value:    m.SourceUserIDStr,
				Identity: true,
			},
		},
	}
}

func (m mediaItem) fileName() string {
	source := m.getURL()
	u, err := url.Parse(source)
	if err == nil {
		source = path.Base(u.Path)
	} else {
		source = path.Base(source)
	}
	// media in the export archives are prefixed by the
	// tweet ID they were posted with and a hyphen
	if m.parent.source == "archive" {
		source = fmt.Sprintf("%s-%s", m.parent.TweetIDStr, source)
	}
	return source
}

func (m mediaItem) content() timeline.ItemData {
	return timeline.ItemData{
		Filename:  m.fileName(),
		Data:      m.fileReader,
		MediaType: m.mediaType(),
	}
}

func (m mediaItem) fileReader(_ context.Context) (io.ReadCloser, error) {
	return m.readCloser, nil
}

func (m mediaItem) mediaType() string {
	switch m.Type {
	case "animated_gif":
		fallthrough
	case "video":
		_, contentType, _ := m.getLargestVideo()
		return contentType
	case "photo":
		fname := m.fileName()
		if fname == "" {
			return ""
		}
		ext := strings.ToLower(path.Ext(fname))
		if len(ext) == 0 {
			return ""
		}
		suffix := ext[1:] // trim the leading dot
		if suffix == "jpg" {
			suffix = "jpeg"
		}
		return "image/" + suffix
	}
	return ""
}

func (m mediaItem) getLargestVideo() (bitrate int, contentType, source string) {
	if m.VideoInfo == nil {
		return
	}
	bitrate = -1 // so that greater-than comparison below works for video bitrate=0 (animated_gif)
	for _, v := range m.VideoInfo.Variants {
		if int(v.Bitrate) > bitrate {
			source = v.URL
			contentType = v.ContentType
			bitrate = int(v.Bitrate)
		}
	}

	return
}

func (m mediaItem) getURL() string {
	switch m.Type {
	case "animated_gif":
		fallthrough
	case "video":
		_, _, source := m.getLargestVideo()
		return source
	case "photo":
		// the size of the photo can be adjusted
		// when downloading by appending a size
		// to the end of the URL: ":thumb", ":small",
		// ":medium", ":large", or ":orig" -- but
		// we don't do that here, only do that when
		// actually downloading
		if m.MediaURLHTTPS != "" {
			return m.MediaURLHTTPS
		}
		return m.MediaURL
	}
	return ""
}

type additionalMediaInfo struct {
	Monetizable bool `json:"monetizable"`
}

type videoInfo struct {
	AspectRatio    []transFloat    `json:"aspect_ratio"`
	DurationMillis transInt        `json:"duration_millis"`
	Variants       []videoVariants `json:"variants"`
}

type videoVariants struct {
	Bitrate     transInt `json:"bitrate,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	URL         string   `json:"url"`
}

type mediaSizes struct {
	Thumb  mediaSize `json:"thumb"`
	Small  mediaSize `json:"small"`
	Medium mediaSize `json:"medium"`
	Large  mediaSize `json:"large"`
}

type mediaSize struct {
	W      transInt `json:"w"`
	H      transInt `json:"h"`
	Resize string   `json:"resize"` // fit|crop
}

type twitterUser struct {
	ContributorsEnabled            bool             `json:"contributors_enabled"`
	CreatedAt                      string           `json:"created_at"`
	DefaultProfile                 bool             `json:"default_profile"`
	DefaultProfileImage            bool             `json:"default_profile_image"`
	Description                    string           `json:"description"`
	Entities                       *twitterEntities `json:"entities"`
	FavouritesCount                int              `json:"favourites_count"`
	FollowersCount                 int              `json:"followers_count"`
	Following                      any              `json:"following"`
	FollowRequestSent              any              `json:"follow_request_sent"`
	FriendsCount                   int              `json:"friends_count"`
	GeoEnabled                     bool             `json:"geo_enabled"`
	HasExtendedProfile             bool             `json:"has_extended_profile"`
	IsTranslationEnabled           bool             `json:"is_translation_enabled"`
	IsTranslator                   bool             `json:"is_translator"`
	Lang                           string           `json:"lang"`
	ListedCount                    int              `json:"listed_count"`
	Location                       string           `json:"location"`
	Name                           string           `json:"name"`
	Notifications                  any              `json:"notifications"`
	ProfileBackgroundColor         string           `json:"profile_background_color"`
	ProfileBackgroundImageURL      string           `json:"profile_background_image_url"`
	ProfileBackgroundImageURLHTTPS string           `json:"profile_background_image_url_https"`
	ProfileBackgroundTile          bool             `json:"profile_background_tile"`
	ProfileBannerURL               string           `json:"profile_banner_url"`
	ProfileImageURL                string           `json:"profile_image_url"`
	ProfileImageURLHTTPS           string           `json:"profile_image_url_https"`
	ProfileLinkColor               string           `json:"profile_link_color"`
	ProfileSidebarBorderColor      string           `json:"profile_sidebar_border_color"`
	ProfileSidebarFillColor        string           `json:"profile_sidebar_fill_color"`
	ProfileTextColor               string           `json:"profile_text_color"`
	ProfileUseBackgroundImage      bool             `json:"profile_use_background_image"`
	Protected                      bool             `json:"protected"`
	ScreenName                     string           `json:"screen_name"`
	StatusesCount                  int              `json:"statuses_count"`
	TimeZone                       any              `json:"time_zone"`
	TranslatorType                 string           `json:"translator_type"`
	URL                            string           `json:"url"`
	UserID                         transInt         `json:"id"`
	UserIDStr                      string           `json:"id_str"`
	UtcOffset                      any              `json:"utc_offset"`
	Verified                       bool             `json:"verified"`
}

type phoneNumberFile []struct {
	Device struct {
		PhoneNumber string `json:"phoneNumber"`
	} `json:"device"`
}

type profileFile []struct {
	Profile struct {
		Description struct {
			Bio      string `json:"bio"`
			Website  string `json:"website"`
			Location string `json:"location"`
		} `json:"description"`
		AvatarMediaURL string `json:"avatarMediaUrl"`
		HeaderMediaURL string `json:"headerMediaUrl"`
	} `json:"profile"`
}

type twitterAccountFile []struct {
	Account twitterAccount `json:"account"`
}

type twitterAccount struct {
	// fields from export archive file: account.js
	PhoneNumber        string `json:"phoneNumber"`
	Email              string `json:"email"`
	CreatedVia         string `json:"createdVia"`
	CreatedAt          string `json:"createdAt"`
	Username           string `json:"username"`
	AccountID          string `json:"accountId"`
	AccountDisplayName string `json:"accountDisplayName"`

	// info from file: phone-number.js
	PhoneNumbers phoneNumberFile

	// info from file: profile.js
	Profile profileFile

	// fields from API endpoint: GET /2/users[/by/username/...]
	Data struct {
		Verified    bool      `json:"verified"`
		CreatedAt   time.Time `json:"created_at"`
		Description string    `json:"description"`
		Location    string    `json:"location"`
		Entities    struct {
			URL struct {
				URLs []struct {
					Start       int    `json:"start"`
					End         int    `json:"end"`
					URL         string `json:"url"`
					ExpandedURL string `json:"expanded_url"`
					DisplayURL  string `json:"display_url"`
				} `json:"urls"`
			} `json:"url"`
			Description struct {
				Mentions []struct {
					Start    int    `json:"start"`
					End      int    `json:"end"`
					Username string `json:"username"`
				} `json:"mentions"`
			} `json:"description"`
		} `json:"entities"`
		PublicMetrics struct {
			FollowersCount int `json:"followers_count"`
			FollowingCount int `json:"following_count"`
			TweetCount     int `json:"tweet_count"`
			ListedCount    int `json:"listed_count"`
		} `json:"public_metrics"`
		URL             string `json:"url"`
		ProfileImageURL string `json:"profile_image_url"`
		Name            string `json:"name"`
		Protected       bool   `json:"protected"`
		PinnedTweetID   string `json:"pinned_tweet_id"`
		Username        string `json:"username"`
		ID              string `json:"id"`
	} `json:"data"`
}

func (ta twitterAccount) screenName() string {
	if ta.Data.Username != "" {
		return ta.Data.Username // from API
	}
	return ta.Username // from archive file
}

func (ta twitterAccount) id() string {
	if ta.Data.ID != "" {
		return ta.Data.ID // from API
	}
	return ta.AccountID // from archive file
}

func (ta twitterAccount) name() string {
	if ta.Data.Name != "" {
		return ta.Data.Name // from API
	}
	return ta.AccountDisplayName // from archive file
}

// entity returns a populated Entity from a populated twitterAccount.
func (ta twitterAccount) entity(ctx context.Context, fsys fs.FS) timeline.Entity {
	ent := timeline.Entity{
		Name: ta.AccountDisplayName,
		Attributes: []timeline.Attribute{
			{
				Name:     identityAttribute,
				Value:    ta.AccountID,
				Identity: true,
			},
			{
				Name:        timeline.AttributeEmail,
				Value:       ta.Email,
				Identifying: true,
			},
			{
				Name:        timeline.AttributePhoneNumber,
				Value:       ta.PhoneNumber,
				Identifying: true,
			},
			{
				Name:        "twitter_username",
				Value:       ta.Username,
				Identifying: true,
			},
		},
	}

	for _, ph := range ta.PhoneNumbers {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:        timeline.AttributePhoneNumber,
			Value:       ph.Device.PhoneNumber,
			Identifying: true,
		})
	}

	if len(ta.Profile) > 0 {
		profile := ta.Profile[0].Profile
		if profile.AvatarMediaURL != "" {
			if fsys == nil {
				ent.NewPicture = timeline.DownloadData(ctx, profile.AvatarMediaURL)
			} else {
				ent.NewPicture = func(ctx context.Context) (io.ReadCloser, error) {
					avatarFilename := ta.AccountID + "-" + path.Base(profile.AvatarMediaURL)
					picPath := path.Join("data", "profile_media", avatarFilename)
					return fsys.Open(picPath)
				}
			}
		}

		ent.Metadata = timeline.Metadata{
			"Twitter bio": profile.Description.Bio,
		}

		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "twitter_location",
			Value: profile.Description.Location,
		})
	}

	return ent
}

type directMessages struct {
	DMConversation dmConversation `json:"dmConversation"`
}

type dmConversation struct {
	ConversationID string `json:"conversationId"`
	Messages       []struct {
		MessageCreate struct {
			RecipientID string `json:"recipientId"`
			Reactions   []any  `json:"reactions"`
			URLs        []struct {
				URL      string `json:"url"`
				Expanded string `json:"expanded"`
				Display  string `json:"display"`
			} `json:"urls"`
			Text      string    `json:"text"`
			MediaURLs []string  `json:"mediaUrls"`
			SenderID  string    `json:"senderId"`
			ID        string    `json:"id"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"messageCreate"`
	} `json:"messages"`
}

type archiveManifest struct {
	UserInfo struct {
		AccountID   string `json:"accountId"`
		UserName    string `json:"userName"`
		DisplayName string `json:"displayName"`
	} `json:"userInfo"`
	ArchiveInfo struct {
		SizeBytes        string    `json:"sizeBytes"`
		GenerationDate   time.Time `json:"generationDate"`
		IsPartialArchive bool      `json:"isPartialArchive"`
		MaxPartSizeBytes string    `json:"maxPartSizeBytes"`
	} `json:"archiveInfo"`
	ReadmeInfo struct {
		FileName  string `json:"fileName"`
		Directory string `json:"directory"`
		Name      string `json:"name"`
	} `json:"readmeInfo"`
	DataTypes map[string]struct {
		Files []struct {
			FileName   string `json:"fileName"`
			GlobalName string `json:"globalName"`
			Count      string `json:"count"`
		} `json:"files"`
	} `json:"dataTypes"`
}

// transInt is an integer that could be
// unmarshaled from a string, too. This
// is needed because the archive JSON
// from Twitter uses all string values,
// but the same fields are integers with
// the API.
type transInt int64

func (ti *transInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("no value")
	}
	b = bytes.Trim(b, "\"")
	var i int64
	err := json.Unmarshal(b, &i)
	if err != nil {
		return err
	}
	*ti = transInt(i)
	return nil
}

// transFloat is like transInt but for floats.
type transFloat float64

func (tf *transFloat) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("no value")
	}
	b = bytes.Trim(b, "\"")
	var f float64
	err := json.Unmarshal(b, &f)
	if err != nil {
		return err
	}
	*tf = transFloat(f)
	return nil
}

const identityAttribute = "twitter_id"
