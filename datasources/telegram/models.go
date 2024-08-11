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

package telegram

type desktopExportHeader struct {
	About               string              `json:"about"`
	PersonalInformation personalInformation `json:"personal_information"`
}

type personalInformation struct {
	UserID      int64  `json:"user_id"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	PhoneNumber string `json:"phone_number"`
}

type contact struct {
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	PhoneNumber  string `json:"phone_number"`
	Date         string `json:"date"`
	DateUnixtime string `json:"date_unixtime"`
}

type desktopExport struct {
	desktopExportHeader
	ProfilePictures []any `json:"profile_pictures"`
	Contacts        struct {
		About string    `json:"about"`
		List  []contact `json:"list"`
	} `json:"contacts"`
	FrequentContacts struct {
		About string `json:"about"`
		List  []any  `json:"list"`
	} `json:"frequent_contacts"`
	Sessions struct {
		About string `json:"about"`
		List  []struct {
			LastActive         string `json:"last_active"`
			LastActiveUnixtime string `json:"last_active_unixtime"`
			LastIP             string `json:"last_ip"`
			LastCountry        string `json:"last_country"`
			LastRegion         string `json:"last_region"`
			ApplicationName    string `json:"application_name"`
			ApplicationVersion string `json:"application_version"`
			DeviceModel        string `json:"device_model"`
			Platform           string `json:"platform"`
			SystemVersion      string `json:"system_version"`
			Created            string `json:"created"`
			CreatedUnixtime    string `json:"created_unixtime"`
		} `json:"list"`
	} `json:"sessions"`
	WebSessions struct {
		About string `json:"about"`
		List  []any  `json:"list"`
	} `json:"web_sessions"`
	OtherData struct {
		AboutMeta         string `json:"about_meta"`
		ChangesLog        []any  `json:"changes_log"`
		Help              string `json:"help"`
		InstalledStickers []struct {
			URL string `json:"url"`
		} `json:"installed_stickers"`
		IPs []struct {
			IP string `json:"ip"`
		} `json:"ips"`
	} `json:"other_data"`
	Chats struct {
		About string `json:"about"`
		List  []chat `json:"list"`
	} `json:"chats"`
}

type chat struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	ID       int       `json:"id"`
	Messages []message `json:"messages"`
}

type message struct {
	ID           int    `json:"id"`
	Type         string `json:"type"`
	Date         string `json:"date"`
	DateUnixtime string `json:"date_unixtime"`
	From         string `json:"from"`
	FromID       string `json:"from_id"`
	Text         any    `json:"text"` // this is an array of mixed types if the message has any rich content
	TextEntities []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"text_entities"`
	Edited         string `json:"edited,omitempty"`
	EditedUnixtime string `json:"edited_unixtime,omitempty"`

	// for some reason they prepend the user ID with "user"
	fromID string
}
