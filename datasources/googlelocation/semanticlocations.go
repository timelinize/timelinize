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

package googlelocation

type semanticLocationTimelineObject struct {
	Activitysegment struct {
		Activities []struct {
			ActivityType string  `json:"activityType"`
			Probability  float64 `json:"probability"`
		} `json:"activities"`
		ActivityType string `json:"activityType"`
		Confidence   string `json:"confidence"`
		Distance     int    `json:"distance"`
		Duration     struct {
			EndTimestampMs   string `json:"endTimestampMs"`
			StartTimestampMs string `json:"startTimestampMs"`
		} `json:"duration"`
		Endlocation struct {
			LatitudeE7  int `json:"latitudeE7"`
			LongitudeE7 int `json:"longitudeE7"`
			SourceInfo  struct {
				DeviceTag int `json:"deviceTag"`
			} `json:"sourceInfo"`
		} `json:"endLocation"`
		ParkingEvent struct {
			Location struct {
				AccuracyMetres int `json:"accuracyMetres"`
				LatitudeE7     int `json:"latitudeE7"`
				LongitudeE7    int `json:"longitudeE7"`
			} `json:"location"`
			TimestampMs string `json:"timestampMs"`
		} `json:"parkingEvent"`
		SimplifiedRawPath struct {
			Points []struct {
				AccuracyMeters int    `json:"accuracyMeters"`
				LatE7          int    `json:"latE7"`
				LngE7          int    `json:"lngE7"`
				TimestampMs    string `json:"timestampMs"`
			} `json:"points"`
		} `json:"simplifiedRawPath"`
		StartLocation struct {
			LatitudeE7  int `json:"latitudeE7"`
			LongitudeE7 int `json:"longitudeE7"`
			SourceInfo  struct {
				DeviceTag int `json:"deviceTag"`
			} `json:"sourceInfo"`
		} `json:"startLocation"`
		TransitPath struct {
			HexRgbColor  string `json:"hexRgbColor"`
			Name         string `json:"name"`
			TransitStops []struct {
				LatitudeE7  int    `json:"latitudeE7"`
				LongitudeE7 int    `json:"longitudeE7"`
				Name        string `json:"name"`
				PlaceId     string `json:"placeId"`
			} `json:"transitStops"`
		} `json:"transitPath"`
		WaypointPath struct {
			Waypoints []struct {
				LatE7 int `json:"latE7"`
				LngE7 int `json:"lngE7"`
			} `json:"waypoints"`
		} `json:"waypointPath"`
	} `json:"activitySegment"`
	PlaceVisit struct {
		CenterLatE7 int `json:"centerLatE7"`
		CenterLngE7 int `json:"centerLngE7"`
		ChildVisits []struct {
			CenterLatE7 int `json:"centerLatE7"`
			CenterLngE7 int `json:"centerLngE7"`
			Duration    struct {
				EndTimestampMs   string `json:"endTimestampMs"`
				StartTimestampMs string `json:"startTimestampMs"`
			} `json:"duration"`
			EditConfirmationStatus string `json:"editConfirmationStatus"`
			Location               struct {
				Address            string  `json:"address"`
				LatitudeE7         int     `json:"latitudeE7"`
				LongitudeE7        int     `json:"longitudeE7"`
				LocationConfidence float64 `json:"locationConfidence"`
				Name               string  `json:"name"`
				PlaceId            string  `json:"placeId"`
				SourceInfo         struct {
					DeviceTag int `json:"deviceTag"`
				} `json:"sourceInfo"`
			} `json:"location"`
			OtherCandidateLocations []struct {
				LatitudeE7         int     `json:"latitudeE7"`
				LongitudeE7        int     `json:"longitudeE7"`
				LocationConfidence float64 `json:"locationConfidence"`
				PlaceId            string  `json:"placeId"`
			} `json:"otherCandidateLocations"`
			PlaceConfidence string `json:"placeConfidence"`
			VisitConfidence int    `json:"visitConfidence"`
		} `json:"childVisits"`
		Duration struct {
			EndTimestampMs   string `json:"endTimestampMs"`
			StartTimestampMs string `json:"startTimestampMs"`
		} `json:"duration"`
		Editconfirmationstatus string `json:"editConfirmationStatus"`
		Location               struct {
			Address            string  `json:"address"`
			LatitudeE7         int     `json:"latitudeE7"`
			LongitudeE7        int     `json:"longitudeE7"`
			LocationConfidence float64 `json:"locationConfidence"`
			Name               string  `json:"name"`
			PlaceId            string  `json:"placeId"`
			SemanticType       string  `json:"semanticType"`
			SourceInfo         struct {
				DeviceTag int `json:"deviceTag"`
			} `json:"sourceInfo"`
		} `json:"location"`
		OtherCandidateLocations []struct {
			LatitudeE7         int     `json:"latitudeE7"`
			LongitudeE7        int     `json:"longitudeE7"`
			LocationConfidence float64 `json:"locationConfidence"`
			PlaceId            string  `json:"placeId"`
			SemanticType       string  `json:"semanticType"`
		} `json:"otherCandidateLocations"`
		Placeconfidence   string `json:"placeConfidence"`
		Simplifiedrawpath struct {
			Points []struct {
				AccuracyMeters int    `json:"accuracyMeters"`
				LatE7          int    `json:"latE7"`
				LngE7          int    `json:"lngE7"`
				TimestampMs    string `json:"timestampMs"`
			} `json:"points"`
		} `json:"simplifiedRawPath"`
		VisitConfidence int `json:"visitConfidence"`
	} `json:"placeVisit"`
}
