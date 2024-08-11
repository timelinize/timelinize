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

// Package twitter implements a Timeliner service for importing
// and downloading data from Twitter.
package twitter

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

var (
	oauth2 = timeline.OAuth2{
		ProviderID: "twitter",
		Scopes:     []string{"tweet.read", "users.read", "offline.access"},
	}

	rateLimit = timeline.RateLimit{
		// TODO: from v1...
		// // from https://developer.twitter.com/en/docs/basics/rate-limits
		// // with some leeway since it's actually a pretty generous limit
		// RequestsPerHour: 5900,
		// as of December 2020, project caps allow pulling 500,000 tweets / mo.: https://developer.twitter.com/en/docs/projects/overview
		RequestsPerHour: 1800, // https://developer.twitter.com/en/docs/twitter-api/tweets/search/api-reference/get-tweets-search-recent
	}
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "twitter",
		Title:           "Twitter",
		Icon:            "twitter.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(Client) },
		NewAPIImporter:  func() timeline.APIImporter { return new(Client) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

type Client struct {
	fsys          fs.FS // if reading from an archive
	httpClient    *http.Client
	checkpoint    checkpoint
	acc           timeline.Account
	owner         timeline.Entity
	otherAccounts map[string]twitterAccount // keyed by user/account ID
}

func (c *Client) prepareTweet(ctx context.Context, t *tweet, source string, opt Options) (skip bool, err error) {
	// mark whether this tweet came from the API or an export file
	t.source = source

	// set the owner account information; this has to be done differently
	// depending on the source (it's not embedded in the archive's tweets...)
	switch t.source {
	case "archive":
		t.owner = c.owner
	case "api":
		if t.User != nil {
			if t.User.UserIDStr == c.owner.AttributeValue(identityAttribute) {
				// tweet author is the owner of the account - awesome
				t.owner = c.owner
			} else {
				// look up author's account info
				acc, ok := c.otherAccounts[t.User.UserIDStr]
				if !ok {
					acc, err = c.getAccountFromAPI("", t.User.UserIDStr)
					if err != nil {
						return false, fmt.Errorf("looking up tweet author's account information: %v", err)
					}
					// cache this for later
					if len(c.otherAccounts) > 2000 {
						for id := range c.otherAccounts {
							delete(c.otherAccounts, id)
							break
						}
					}
					c.otherAccounts[acc.id()] = acc
				}
				t.owner = acc.entity(ctx, c.fsys)
			}
		}
	default:
		return false, fmt.Errorf("unrecognized source: %s", t.source)
	}

	// skip empty tweets
	if t.isEmpty() {
		return true, nil
	}

	// skip tweets we aren't interested in
	if !opt.Retweets && t.isRetweet() {
		return true, nil
	}
	if !opt.Replies && t.InReplyToUserIDStr != "" && t.InReplyToUserIDStr != t.owner.AttributeValue(identityAttribute) {
		// TODO: Replies should have more context, like what are we replying to, etc... the whole thread, even?
		// this option is about replies to tweets other than our own (which are like a continuation of one thought)
		return true, nil
	}

	// parse Twitter's time string into an actual time value
	t.createdAtParsed, err = time.Parse("Mon Jan 2 15:04:05 -0700 2006", t.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("parsing created_at time: %v", err)
	}

	return false, nil
}

func (c *Client) makeItemGraphFromTweet(ctx context.Context, t tweet, fsys fs.FS, opt Options) (*timeline.Graph, error) {
	// oneMediaItem := t.hasExactlyOneMediaItem()

	item := &timeline.Item{
		ID:             t.TweetIDStr,
		Classification: timeline.ClassSocial,
		Timestamp:      t.createdAtParsed,
		Location:       t.location(),
		Owner:          t.owner,
		Content:        t.content(),
		Metadata: timeline.Metadata{
			"Retweets": int(t.RetweetCount),
			"Likes":    int(t.FavoriteCount),
		},
	}
	ig := &timeline.Graph{Item: item}

	hasText := t.text() != ""

	// process the media items attached to the tweet
	if t.ExtendedEntities != nil {
		// TODO: use collection or attachments? (or both?) attachment relation is simpler, but collections preserves order
		// var collItems []timeline.CollectionItem

		for i, m := range t.ExtendedEntities.Media {
			if i == 0 && !hasText {
				// if tweet does not have any text, then the first media item was
				// used as the main item's content
				continue
			}

			m.parent = &t

			var dataFileName string
			if dfn := m.fileName(); dfn == "" {
				// TODO: proper logging
				// log.Printf("[ERROR][%s/%s] Tweet media has no data file name: %+v",
				// 	DataSourceID, c.acc.User.UserID, m)
				continue
			} else {
				dataFileName = dfn
			}

			switch t.source {
			case "archive":
				targetFileInArchive := path.Join("data", "tweets_media", dataFileName)

				file, err := fsys.Open(targetFileInArchive)
				if err != nil {
					return nil, fmt.Errorf("opening data file in archive: %s: %v", targetFileInArchive, err)
				}
				m.readCloser = file

			case "api":
				mediaURL := m.getURL()
				if m.Type == "photo" {
					mediaURL += ":orig" // get original file, with metadata
				}
				resp, err := http.Get(mediaURL)
				if err != nil {
					return nil, fmt.Errorf("getting media resource %s: %v", m.MediaURLHTTPS, err)
				}
				if resp.StatusCode != http.StatusOK {
					return nil, fmt.Errorf("media resource returned HTTP status %s: %s", resp.Status, m.MediaURLHTTPS)
				}
				m.readCloser = resp.Body

			default:
				return nil, fmt.Errorf("unrecognized source value: must be api or archive: %s", t.source)
			}

			// if !oneMediaItem {
			item := &timeline.Item{
				ID:             m.MediaIDStr,
				Classification: timeline.ClassSocial,
				Timestamp:      m.parent.createdAtParsed,
				Location:       m.parent.location(),
				Owner:          m.owner(),
				Content:        m.content(),
			}
			ig.ToItem(timeline.RelAttachment, item)
			// collItems = append(collItems, timeline.CollectionItem{
			// 	Item:     item,
			// 	Position: i,
			// })
			// }
		}

		// if len(collItems) > 0 {
		// 	ig.Collections = append(ig.Collections, timeline.Collection{
		// 		OriginalID: "tweet_" + t.id(),
		// 		Items:      collItems,
		// 	})
		// }
	}

	// if we're using the API, go ahead and get the
	// 'parent' tweet to which this tweet is a reply
	if t.source == "api" && t.InReplyToStatusIDStr != "" {
		inReplyToTweet, err := c.getTweetFromAPI(t.InReplyToStatusIDStr)
		if err != nil {
			return nil, fmt.Errorf("getting tweet that this tweet (%s) is in reply to (%s): %v",
				t.id(), t.InReplyToStatusIDStr, err)
		}
		skip, err := c.prepareTweet(ctx, &inReplyToTweet, "api", opt)
		if err != nil {
			return nil, fmt.Errorf("preparing reply-parent tweet: %v", err)
		}
		if !skip {
			repIG, err := c.makeItemGraphFromTweet(ctx, inReplyToTweet, fsys, opt)
			if err != nil {
				return nil, fmt.Errorf("making item from tweet that this tweet (%s) is in reply to (%s): %v",
					t.id(), inReplyToTweet.id(), err)
			}
			// TODO: is this relation backwards?
			ig.Edges = append(ig.Edges, timeline.Relationship{
				Relation: timeline.RelReply,
				To:       repIG,
			})
		}
	}

	// if this tweet embeds/quotes/links to other tweets,
	// we should establish those relationships as well
	if t.source == "api" && t.Entities != nil {
		for _, urlEnt := range t.Entities.URLs {
			embeddedTweetID := getLinkedTweetID(urlEnt.ExpandedURL)
			if embeddedTweetID == "" {
				continue
			}
			embeddedTweet, err := c.getTweetFromAPI(embeddedTweetID)
			if err != nil {
				return nil, fmt.Errorf("getting tweet that this tweet (%s) embeds (%s): %v",
					t.id(), t.InReplyToStatusIDStr, err)
			}
			skip, err := c.prepareTweet(ctx, &embeddedTweet, "api", opt)
			if err != nil {
				return nil, fmt.Errorf("preparing embedded tweet: %v", err)
			}
			if !skip {
				embIG, err := c.makeItemGraphFromTweet(ctx, embeddedTweet, fsys, opt)
				if err != nil {
					return nil, fmt.Errorf("making item from tweet that this tweet (%s) embeds (%s): %v",
						t.id(), embeddedTweet.id(), err)
				}
				ig.Edges = append(ig.Edges, timeline.Relationship{
					Relation: timeline.RelQuotes,
					To:       embIG,
				})
			}
		}
	}

	return ig, nil
}

// Options customizes item listings.
type Options struct {
	Retweets bool `json:"retweets"` // whether to include retweets
	Replies  bool `json:"replies"`  // whether to include replies to tweets that are not our own; i.e. are not a continuation of thought
}

type checkpoint struct {
	NextToken string
}

// // save records the checkpoint.
// func (ch *checkpoint) save(ctx context.Context) {
// 	gobBytes, err := timeline.MarshalGob(ch)
// 	if err != nil {
// 		// TODO: proper logger
// 		// log.Printf("[ERROR][%s] Encoding checkpoint: %v", DataSourceID, err)
// 	}
// 	timeline.Checkpoint(ctx, gobBytes)
// }

// // load decodes the checkpoint.
// // TODO: see if we can just get the unmarshaled value and type-assert it
// // TODO: getting "gob: type mismatch: no fields matched compiling decoder for checkpoint" -- also, see if we can just save an any, why are we encoding as bytes first then marshaling as gob later?
// func (ch *checkpoint) load(checkpointGob []byte) {
// 	if len(checkpointGob) == 0 {
// 		return
// 	}
// 	err := timeline.UnmarshalGob(checkpointGob, ch)
// 	if err != nil {
// 		log.Printf("[ERROR][%s] Decoding checkpoint: %v", DataSourceID, err)
// 	}
// }

// // maxTweetID returns the higher of the two tweet IDs.
// // Errors parsing the strings as integers are ignored.
// // Empty string inputs are ignored so the other value
// // will win automatically. If both are empty, an empty
// // string is returned.
// func maxTweetID(id1, id2 string) string {
// 	if id1 == "" {
// 		return id2
// 	}
// 	if id2 == "" {
// 		return id1
// 	}
// 	id1int, _ := strconv.ParseInt(id1, 10, 64)
// 	id2int, _ := strconv.ParseInt(id2, 10, 64)
// 	if id1int > id2int {
// 		return id1
// 	}
// 	return id2
// }

// getLinkedTweetID returns the ID of the tweet in
// a link to a tweet, for example:
// "https://twitter.com/foo/status/12345"
// returns "12345". If the tweet ID cannot be found
// or the URL does not match the right format,
// an empty string is returned.
func getLinkedTweetID(urlToTweet string) string {
	if !linkToTweetRE.MatchString(urlToTweet) {
		return ""
	}
	u, err := url.Parse(urlToTweet)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

var linkToTweetRE = regexp.MustCompile(`https?://twitter\.com/.*/status/[0-9]+`)
