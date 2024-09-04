//go:generate go run generator.go

package main

import (
	_ "github.com/timelinize/timelinize/datasources/contactlist"
	_ "github.com/timelinize/timelinize/datasources/email"
	_ "github.com/timelinize/timelinize/datasources/facebook"
	_ "github.com/timelinize/timelinize/datasources/generic"
	_ "github.com/timelinize/timelinize/datasources/geojson"
	_ "github.com/timelinize/timelinize/datasources/gmail"
	_ "github.com/timelinize/timelinize/datasources/googlelocation"
	_ "github.com/timelinize/timelinize/datasources/googlephotos"
	_ "github.com/timelinize/timelinize/datasources/gpx"
	_ "github.com/timelinize/timelinize/datasources/icloud"
	_ "github.com/timelinize/timelinize/datasources/imessage"
	_ "github.com/timelinize/timelinize/datasources/instagram"
	_ "github.com/timelinize/timelinize/datasources/iphone"
	_ "github.com/timelinize/timelinize/datasources/kmlgx"
	_ "github.com/timelinize/timelinize/datasources/media"
	_ "github.com/timelinize/timelinize/datasources/nmea"
	_ "github.com/timelinize/timelinize/datasources/smsbackuprestore"
	_ "github.com/timelinize/timelinize/datasources/strava"
	_ "github.com/timelinize/timelinize/datasources/telegram"
	_ "github.com/timelinize/timelinize/datasources/twitter"
	_ "github.com/timelinize/timelinize/datasources/vcard"
)
