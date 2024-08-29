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
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// PeriodicStats represents period statistics.
// TODO: This is very much an experimental, WIP type... might need to generalize it or make way more of these
type PeriodicStats struct {
	Period string `json:"period"`
	Count  int    `json:"count"`
}

// RecentItemStats returns period statistics about recent items.
func (tl *Timeline) RecentItemStats(params url.Values) ([]PeriodicStats, error) {
	period := params.Get("period")

	var dateAdjust, startOf, periodColumn string
	switch period {
	default:
		fallthrough
	case "monthly":
		dateAdjust = "-11 months"
		startOf = "month"
		periodColumn = "strftime('%m', date(timestamp/1000, 'unixepoch'))"
	case "daily":
		dateAdjust = "-28 days"
		startOf = "day"
		periodColumn = "date(timestamp/1000, 'unixepoch')"
	case "yearly":
		dateAdjust = "-10 years"
		startOf = "year"
		periodColumn = "strftime('%Y', date(timestamp/1000, 'unixepoch'))"
	}

	// this query uses a CTE (common table expression) to find the most recent item timestamp in the database
	// and then subtract the time period we want to visualize... so like, if we want to view the last 12 months
	// including this one, we subtract 11 months and go to the start of that month (so if it's October, we
	// go back 11 months to the start of the last November, since we don't want to include partial October
	// that happened 12 months ago) -- with that value, we can then select the count of items in each period
	// that are newer than the cutoff date we calculated in the CTE.
	//nolint:gosec // all our values are hard-coded
	query := fmt.Sprintf(`
	WITH bounds AS (
		SELECT unixepoch(
			(SELECT date(timestamp/1000, 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1),
			'%s',
			'start of %s'
			)*1000 AS earliest_timestamp
			) SELECT
			%s AS period,
			count()
			FROM items, bounds
			WHERE timestamp > earliest_timestamp
			GROUP BY period
			ORDER BY timestamp`, dateAdjust, startOf, periodColumn)

	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	rows, err := tl.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []PeriodicStats
	for rows.Next() {
		var group string
		var count int
		err := rows.Scan(&group, &count)
		if err != nil {
			return nil, err
		}
		if period == "monthly" {
			monthInt, err := strconv.Atoi(group)
			if err != nil {
				return nil, fmt.Errorf("bad month group: %s: %w", group, err)
			}
			group = time.Month(monthInt).String()
		}
		results = append(results, PeriodicStats{group, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// ClassificationStat holds counts of item classes.
type ClassificationStat struct {
	ClassificationName string `json:"x"`
	Count              int    `json:"y"`
}

// ItemTypeStats returns info about items by their classifications.
func (tl *Timeline) ItemTypeStats() ([]ClassificationStat, error) {
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	rows, err := tl.db.Query("SELECT classification_name, count() FROM extended_items GROUP BY classification_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ClassificationStat
	for rows.Next() {
		var className *string
		var count int
		err := rows.Scan(&className, &count)
		if err != nil {
			return nil, err
		}
		if className == nil {
			classNameStr := "unknown"
			className = &classNameStr
		}
		results = append(results, ClassificationStat{*className, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

/*
SELECT
	date(timestamp/1000, 'unixepoch') as date,
	strftime('%H', time(timestamp/1000, 'unixepoch')) as hour,
	data_source_name,
	count()
FROM extended_items
-- WHERE timestamp > unixepoch((SELECT date(timestamp/1000, 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1), '-30 days')*1000
WHERE id % 1 = 0
	-- AND timestamp IS NOT NULL
	AND timestamp > unixepoch((SELECT date(timestamp/1000, 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1), '-365 days')*1000
GROUP BY date, hour
ORDER BY timestamp
LIMIT 1000;
*/

// DSUsageSeries correlates data source name with a series of statistics.
type DSUsageSeries struct {
	Name string        `json:"name"`
	Data []DSUsageStat `json:"data"`
}

// DSUsageStat is a data point containing the date, hour of day, and number of items.
type DSUsageStat struct {
	// DataSource string `json:"data_source"`
	Date  string `json:"x"`
	Hour  int    `json:"y"`
	Count int    `json:"z"`
}

// DataSourceUsageStats returns the counts by data source.
func (tl *Timeline) DataSourceUsageStats() ([]DSUsageSeries, error) {
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	// This query is a bit of a mouthful, but basically, its purpose is to retrieve a sampling of items
	// that are likely representative of the whole repo, and group them by data source and hour so that
	// they can be rendered onto a chart where the X axis is date, Y is time/hour of day, and Z (the size
	// of the bubble) is the number of items in that group. This particular query accounts for timezone
	// offsets where possible/non-NULL, so that items that were experienced at, say, 10AM, don't show up
	// at ridiculous unholy hours of the morning; `timediff(datetime('now', 'localtime'), datetime())`
	// calculates the local timezone offset, which is used as a second-best guess if the item row does
	// not include an offset. This is a reasonable assumption as long as the person's data generally
	// stays in the same timezone.
	//
	// We currently group by week number; this is arbitrary but strikes a decent balance between group
	// size and granularity. Too granular (i.e. daily) and there's too many data points. Groups too large
	// will lead to a sparse and uninteresting/unhelpful chart. We also currently select timestamps that
	// happened up to a year before the last item in the table, but this is also arbitrary and helps form
	// the readability of the chart. Finally, we limit to 1000 results to avoid burdening the frontend.
	// Another idea is to not constrain by timestamp and instead to sample every Nth row, for example
	// (but still would want to ensure timestamp is not null).
	rows, err := tl.db.Query(`
		SELECT
			data_source_name,
			date(timestamp/1000, 'unixepoch') AS date,
			CASE
				WHEN time_offset THEN cast(strftime('%k', time(timestamp/1000, 'unixepoch', time_offset||' seconds')) AS INTEGER)
				ELSE                  cast(strftime('%k', time(timestamp/1000, 'unixepoch', timediff(datetime('now', 'localtime'), datetime()))) AS INTEGER)
			END AS hour,
			count()
		FROM extended_items
		WHERE timestamp > unixepoch((SELECT date(timestamp/1000, 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1), '-365 days')*1000
		GROUP BY strftime('%W', date(timestamp/1000, 'unixepoch')), hour
		ORDER BY data_source_name, timestamp
		LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []DSUsageSeries

	// var results []dsUsageStat
	for rows.Next() {
		var date, dsName string
		var hour, count int
		err := rows.Scan(&dsName, &date, &hour, &count)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 || all[len(all)-1].Name != dsName {
			all = append(all, DSUsageSeries{Name: dsName})
		}
		all[len(all)-1].Data = append(all[len(all)-1].Data, DSUsageStat{date, hour, count})
		// results = append(results, dsUsageStat{date, hour, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return all, nil
}
