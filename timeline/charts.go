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
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
)

func (tl *Timeline) Chart(ctx context.Context, chartName string, params url.Values) (any, error) {
	switch chartName {
	case "periodical":
		return tl.chartRecentItems(ctx, params)
	case "classifications":
		return tl.chartItemTypes(ctx)
	case "datasources":
		return tl.chartDataSourceUsage(ctx)
	case "attributes_stacked_area":
		return tl.AttributeStats(ctx, params)
	case "recent_data_sources":
		return tl.chartRecentDaysItemCount(ctx, params)
	default:
		return nil, fmt.Errorf("unknown chart name: %s", chartName)
	}
}

func (tl *Timeline) chartRecentItems(ctx context.Context, params url.Values) (any, error) {
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
	// (The `date(min(timestamp/1000, unixepoch()), 'unixepoch')` bit is because the highest item timestamp
	// in the DB might be in the future (calendar events, for example), and may often be wrong (bad data),
	// so we only go from the last item OR today's timestamp, whichever is earlier.)
	//nolint:gosec // all our values are hard-coded
	query := fmt.Sprintf(`
	WITH bounds AS (
		SELECT unixepoch(
			(SELECT date(min(timestamp/1000, unixepoch()), 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1),
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

	rows, err := tl.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type periodicStats struct {
		Period string `json:"period"`
		Count  int    `json:"count"`
	}

	var results []periodicStats
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
		results = append(results, periodicStats{group, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func (tl *Timeline) chartItemTypes(ctx context.Context) (any, error) {
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	rows, err := tl.db.QueryContext(ctx, "SELECT classification_name, count() FROM extended_items GROUP BY classification_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type classificationStat struct {
		ClassificationName string `json:"name"`
		Count              int    `json:"value"`
	}

	var results []classificationStat
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
		results = append(results, classificationStat{*className, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func (tl *Timeline) chartRecentDaysItemCount(ctx context.Context, params url.Values) (any, error) {
	days, err := strconv.Atoi(params.Get("days"))
	if err != nil {
		return nil, err
	}

	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	const msPerDay = int(24 * time.Hour / time.Millisecond)

	minTime := msPerDay * days

	// the LIMIT is arbitrary, just to prevent an accidentally huge resultset
	// (the `date(min(timestamp/1000, unixepoch()), 'unixepoch'))` bit is the same as similar in another chart query,
	// to prevent future-timestampped items, which are usually wrong or from calendars, from crowding out all
	// the other data on the chart; so the latest timestamp we accept is today's date)
	rows, err := tl.db.QueryContext(ctx, `
		SELECT
			strftime('%Y-%m-%d', date(min(timestamp/1000, unixepoch()), 'unixepoch')) AS date,
			data_source_name,
			count()
		FROM extended_items
		WHERE timestamp > unixepoch()*1000 - ?
		GROUP BY date, data_source_name
		LIMIT 2000`, minTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type recentDaysCount struct {
		Date           string `json:"date"`
		DataSourceName string `json:"data_source_name"`
		Count          int    `json:"count"`
	}

	var results []recentDaysCount
	for rows.Next() {
		var date, dsName string
		var count int
		err := rows.Scan(&date, &dsName, &count)
		if err != nil {
			return nil, err
		}
		results = append(results, recentDaysCount{date, dsName, count})
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

// chartDataSourceUsage returns the counts by data source.
func (tl *Timeline) chartDataSourceUsage(ctx context.Context) (any, error) {
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
	//
	// Oh, and we make sure to set the end date to the last item, or today's date if the last item is in
	// the future, since future items are outliers and throw off the whole chart.
	rows, err := tl.db.QueryContext(ctx, `
		SELECT
			data_source_name,
			date(timestamp/1000, 'unixepoch') AS date,
			CASE
				WHEN time_offset THEN cast(strftime('%k', time(timestamp/1000, 'unixepoch', time_offset||' seconds')) AS INTEGER)
				ELSE                  cast(strftime('%k', time(timestamp/1000, 'unixepoch', timediff(datetime('now', 'localtime'), datetime()))) AS INTEGER)
			END AS hour,
			count()
		FROM extended_items
		WHERE timestamp > unixepoch((SELECT date(min(timestamp/1000, unixepoch()), 'unixepoch') FROM items ORDER BY timestamp DESC LIMIT 1), '-730 days')*1000
			AND timestamp <= unixepoch()*1000
		GROUP BY strftime('%Y %W', date(timestamp/1000, 'unixepoch')), hour
		ORDER BY data_source_name, timestamp
		LIMIT 2000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dsUsageSeries struct {
		Name string  `json:"name"`
		Data [][]any `json:"data"` // inner dimension is [x, y, z] or, specifically, [date, hour, count]
	}

	var all []dsUsageSeries

	for rows.Next() {
		var date, dsName string
		var hour, count int
		err := rows.Scan(&dsName, &date, &hour, &count)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 || all[len(all)-1].Name != dsName {
			all = append(all, dsUsageSeries{Name: dsName})
		}
		all[len(all)-1].Data = append(all[len(all)-1].Data, []any{date, hour, count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return all, nil
}

// AttributeStats returns the counts of items by month for attributes of an entity.
func (tl *Timeline) AttributeStats(ctx context.Context, params url.Values) (any, error) {
	entityID, err := strconv.Atoi(params.Get("entity_id"))
	if err != nil {
		return nil, fmt.Errorf("invalid entity ID; must be integer: %w", err)
	}

	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	rows, err := tl.db.QueryContext(ctx, `
		SELECT
			strftime('%Y', date(items.timestamp/1000, 'unixepoch')) AS year,
			strftime('%m', date(items.timestamp/1000, 'unixepoch')) AS month,
			attributes.name,
			attributes.value,
			count(items.id) AS num
		FROM entity_attributes
		JOIN attributes ON attributes.id = entity_attributes.attribute_id
		JOIN items ON items.attribute_id = attributes.id
		WHERE entity_attributes.entity_id = ?
			AND items.timestamp IS NOT NULL
			AND year IS NOT NULL
			AND month IS NOT NULL
		GROUP BY year, month, entity_attributes.attribute_id
		ORDER BY year, month, attributes.value`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type attributeStatsSeries struct {
		Name string    `json:"name"` // attribute value
		Data [][]int64 `json:"data"`

		AttributeName string `json:"attribute_name"` // attribute name is not used by the chart lib, but by our script
	}

	seriesMap := make(map[string]attributeStatsSeries)

	var earliest, latest time.Time

	for rows.Next() {
		var year, month int
		var count int64
		var attrName, attrValue string
		err := rows.Scan(&year, &month, &attrName, &attrValue, &count)
		if err != nil {
			return nil, err
		}
		if _, ok := seriesMap[attrValue]; !ok {
			seriesMap[attrValue] = attributeStatsSeries{Name: attrValue, AttributeName: attrName}
		}
		monthYear := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		if monthYear.Before(earliest) || earliest.IsZero() {
			earliest = monthYear
		}
		if monthYear.After(latest) || latest.IsZero() {
			latest = monthYear
		}
		series := seriesMap[attrValue]
		series.Data = append(series.Data, []int64{monthYear.UnixMilli(), count})
		seriesMap[attrValue] = series
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	///////
	for k, series := range seriesMap {
		var seriesIdx int
		for ts := earliest; !ts.After(latest); ts = ts.AddDate(0, 1, 0) {
			if seriesIdx >= len(series.Data) {
				series.Data = append(series.Data, []int64{ts.UnixMilli(), 0})
			}
			if ts.UnixMilli() < series.Data[seriesIdx][0] {
				series.Data = slices.Insert(series.Data, seriesIdx, []int64{ts.UnixMilli(), 0})
			}
			seriesIdx++
		}
		seriesMap[k] = series
	}
	///////

	results := make([]attributeStatsSeries, 0, len(seriesMap))
	for _, series := range seriesMap {
		results = append(results, series)
	}

	return results, nil
}
