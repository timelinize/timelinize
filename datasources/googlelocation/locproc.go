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

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

// LocationSource is a type that can get the next location to process.
type LocationSource interface {
	// NextLocation returns the next location to process.
	// When there are no more locations, it should return
	// (nil, nil).
	//
	// Implementations must honor context cancellation.
	NextLocation(ctx context.Context) (*Location, error)
}

// NewLocationProcessor returns a new location source that filters, refines, clusters,
// and possibly simplifies the locations it reads from source.
//
// To enable simplification, set to a value 1-10. 10 means very aggressive
// simplification (skip many points, leave practically only clusters or endpoints)
// and 1 means to only drop points on the straightest paths. (My preferred is ~2
// when scaled to between 1000 and 50000; i.e. about epsilon=6-7k).
//
// The returned locations may have new or different values/information from what
// the input values were. The fields on the outputted locations should be preferred
// over the inputted locations for filling in item structs. Of special note are
// timespan and metadata fields: timespan and metadata will be set if the point
// represents a cluster, so those fields should be added to the resulting item.
func NewLocationProcessor(source LocationSource, simplificationLevel float64) (LocationSource, error) {
	if simplificationLevel < 0 || simplificationLevel > 10 {
		return nil, fmt.Errorf("invalid simplification factor; must be in [1,10]: %f", simplificationLevel)
	}

	locProc := &locationProcessor{source: source}

	if simplificationLevel != 0 {
		// To scale a number x into range [a,b]:
		// x_scaled = (b-a) * ((x - x_min) / (x_max - x_min)) + a
		const xMin, xMax = 1.0, 10.0
		const epsMin, epsMax = 1000.0, 50000.0
		locProc.epsilon = (epsMax-epsMin)*((simplificationLevel-xMin)/(xMax-xMin)) + epsMin
	}

	return locProc, nil
}

// locationProcessor sets up a pipeline for processing location points/items:
//
//	decoding -> deduplicating -> denoising -> clustering -> simplifying (optional)
//
// The output of the last phase can be used as the next data point.
//
// Assign one locationProcessor per goroutine.
type locationProcessor struct {
	source LocationSource

	// de-duplicating
	previous *Location

	// denoising
	denoiseWindow []*Location

	// clustering
	clusterWindow, cluster []*Location
	windowDistance         float64
	avgChangeInMean        float64
	changeInMeanVar        float64
	latAvg, lonAvg         int64

	// simplifiying
	epsilon float64 // enables RDP path simplification (https://en.wikipedia.org/wiki/Ramer%E2%80%93Douglas%E2%80%93Peucker_algorithm)
	batch   []*Location
}

// NextLocation returns the next location point that can be stored as an item.
func (lp *locationProcessor) NextLocation(ctx context.Context) (*Location, error) {
	if lp.epsilon > 0 {
		return lp.simplifiedNext(ctx)
	}
	return lp.clusteredNext(ctx)
}

func (lp *locationProcessor) simplifiedNext(ctx context.Context) (*Location, error) {
	// ensure simplification is only performed on points coming from the same device/path!
	// mixing points from different devices is asking for bad results

	if len(lp.batch) == 0 {
		const batchSize = 1000
		lp.batch = make([]*Location, 0, batchSize)

		for len(lp.batch) < batchSize {
			next, err := lp.clusteredNext(ctx)
			if err != nil {
				return nil, err
			}
			if next == nil {
				break
			}
			lp.batch = append(lp.batch, next)
		}

		lp.batch = simplifyPath(lp.batch, lp.epsilon)
	}

	if len(lp.batch) > 0 {
		next := lp.batch[0]
		lp.batch = lp.batch[1:]
		return next, nil
	}

	return nil, nil
}

// clusteredNext runs the clustering phase of the algorithm and returns the
// next data point, which might be an aggregate that represents a cluster.
func (lp *locationProcessor) clusteredNext(ctx context.Context) (*Location, error) {
	// any point we return must first pass through the cluster window,
	// so first fill up the cluster buffer and compute updated stats
	// every time we add an element and effectively "shift" the window
	for len(lp.clusterWindow) < clusterWindowSize {
		next, err := lp.denoiseNext(ctx)
		if err != nil {
			return nil, err
		}
		if next == nil {
			break
		}

		lp.clusterWindow = append(lp.clusterWindow, next)

		// for our slightly modified Welford's algorithm to compute the mean as we go,
		// we need to initialize the means with the first values
		if len(lp.clusterWindow) == 1 {
			lp.latAvg = next.LatitudeE7
			lp.lonAvg = next.LongitudeE7
			continue
		}

		prev := lp.clusterWindow[len(lp.clusterWindow)-2]
		next.distanceFromPrev = haversineDistanceEarth(prev.LatitudeE7, prev.LongitudeE7, next.LatitudeE7, next.LongitudeE7) * kmToMeters

		oldLatAvg, oldLonAvg := lp.latAvg, lp.lonAvg

		// TODO: I might be totally wrong on this; verify this!
		// our online average calculation is rolling over a moving window; i.e.
		// values that contribute to the mean will be removed, so this is a slightly
		// modified Welford's algorithm; in Welford's, the delta would be computed
		// between the new value and the current mean; but in ours, since it is
		// rolling, we compute the delta between the new value and the oldest value
		// that is "going away" or being removed from our window (hence why we
		// have to initialize the mean with the first value above; otherwise we'd
		// be subtracting the ...)
		oldest := lp.clusterWindow[0]
		oldestLat, oldestLon := oldest.LatitudeE7, oldest.LongitudeE7
		lp.latAvg += (next.LatitudeE7 - oldestLat) / clusterWindowSize
		lp.lonAvg += (next.LongitudeE7 - oldestLon) / clusterWindowSize

		next.changeInMean = haversineDistanceEarth(oldLatAvg, oldLonAvg, lp.latAvg, lp.lonAvg) * kmToMeters

		lp.windowDistance += next.distanceFromPrev
		lp.windowDistance -= oldest.distanceFromPrev

		// compute the new center (mean) of the points in the window, and also how much
		// this point contributed to the new center; i.e. how much it moved the center; if
		// the center is, on average, not moving very much in this window, the start of the
		// window is likely the start of a cluster (slightly modified Welford's algorithm)
		oldAvgChangeInMean := lp.avgChangeInMean
		goingAway := oldest.changeInMean
		lp.avgChangeInMean += (next.changeInMean - goingAway) / float64(clusterWindowSize)

		// TODO: the next line computes a sample variance, not population variance. to compute population variance, change clusterWindowSize-1 to just clusterWindowSize
		lp.changeInMeanVar += (next.changeInMean - goingAway) * ((next.changeInMean - lp.avgChangeInMean) + (goingAway - oldAvgChangeInMean)) / (clusterWindowSize - 1)
		// TODO: maybe need more conditions to define a cluster? like temporal constraints or threshold for change in mean relative to distance traveled in window?
		// changeInMeanStdDev := math.Sqrt(lp.changeInMeanVar)

		// if l.changeInMean < lp.windowDistance/windowSize {
		// TODO: instead of requiring cluster window to be full, make threshold relative to window length?
		if len(lp.clusterWindow) >= clusterWindowSize && lp.avgChangeInMean < 20 { // TODO: && lp.windowDistance > lp.avgChangeInMean*float64(windowSize) ?
			// we are in a cluster! could be the start or a continuation; doesn't matter
			lp.cluster = append(lp.cluster, oldest)

			// since we added the oldest (first) point to the cluster, we need
			// to pop it from the window, otherwise we'll also doubly treat it
			// as its own point; oops
			lp.clusterWindow = lp.clusterWindow[1:]

			// while in a cluster, we need to continue looping until we find the end
			// of the cluster, as the point we will return represents the cluster
			continue
		} else if len(lp.cluster) > 0 { // TODO: && l.changeInMean > changeInMeanStdDev ?
			// the average change in mean is high enough, and the cluster
			// has non-zero length, so we have found the end of the cluster
			return lp.endClusterFunc(), nil
		}
	}

	// TODO: what if there is an active cluster at the end; should that even happen?

	// pop the oldest point that has passed through the clustering filter as the newest point for the next phase
	if len(lp.clusterWindow) > 0 {
		next := lp.clusterWindow[0]
		lp.clusterWindow = lp.clusterWindow[1:]
		return next, nil
	}

	return nil, nil
}

// endClusterFunc ends the cluster by combining all the clustered
// points into a single location and returns it.
func (lp *locationProcessor) endClusterFunc() *Location {
	defer func() {
		lp.cluster = nil
	}()

	if len(lp.cluster) == 1 {
		return lp.cluster[0]
	}

	clusterSize := float64(len(lp.cluster))

	// combine all the points in the cluster into a single point;
	// to do this, aggregate some statistics and other values
	// as part of the reduction
	var uncertaintySum, centerLat, centerLon, m2lat, m2lon float64
	// var deviceTag int64

	for i, l := range lp.cluster {
		// // there's no great way to do this; hopefully at least one item
		// // in the cluster accurately represents the correct device,
		// // and that there aren't multiple devices in this cluster, but
		// // I'm pretty sure this is the best we can do
		// if deviceTag == 0 {
		// 	deviceTag = l.DeviceTag
		// }

		uncertaintySum += float64(l.Uncertainty)

		deltaLat := float64(l.LatitudeE7) - centerLat
		centerLat += deltaLat / float64(i+1)
		deltaLat2 := float64(l.LatitudeE7) - centerLat
		m2lat += deltaLat * deltaLat2

		deltaLon := float64(l.LongitudeE7) - centerLon
		centerLon += deltaLon / float64(i+1)
		deltaLon2 := float64(l.LongitudeE7) - centerLon
		m2lon += deltaLon * deltaLon2
	}

	meanUncertainty := uncertaintySum / clusterSize
	latVariance := m2lat / clusterSize
	lonVariance := m2lon / clusterSize

	first, last := lp.cluster[0], lp.cluster[len(lp.cluster)-1]

	return &Location{
		Original:    first.Original, // not sure if this makes sense, but
		LatitudeE7:  int64(centerLat),
		LongitudeE7: int64(centerLon),
		Timestamp:   first.Timestamp,
		Timespan:    last.Timestamp,
		Uncertainty: meanUncertainty,
		Metadata: timeline.Metadata{
			"Cluster size": len(lp.cluster),
			// convert to actual lat/lon units; I don't think we need to stay at 1e7 integers
			"Standard deviation (latitude)":  math.Sqrt(latVariance) / placesMult,
			"Standard deviation (longitude)": math.Sqrt(lonVariance) / placesMult,
		},
	}
}

// denoiseNext runs the denoising phase of the algorithm.
// It returns the next point that is not considered noise.
func (lp *locationProcessor) denoiseNext(ctx context.Context) (*Location, error) {
	// all returned points must have passed through the denoise window,
	// which filters noise; so first, ensure there are points in the buffer
	for len(lp.denoiseWindow) < denoiseWindowSize {
		next, err := lp.source.NextLocation(ctx)
		if err != nil {
			return nil, err
		}
		if next == nil {
			break
		}

		if lp.sameAsPrevious(next) {
			continue
		}

		lp.previous = next

		// our method is simple: if the candidate point has a much lower
		// accuracy (higher value; i.e. higher error) than the points on
		// both sides, consider it noise (we could probably do more
		// advanced statistics like what clustering does, across more
		// points, but I haven't found it to be necessary yet)

		// at least 3 points required to check; this is a loop because there
		// maybe multiple noisy points consecutively, and each one removes an
		// element from the window; the larger the window the more consecutive
		// noisy points we can filter out (but in practice I think only a few
		// extra slots are beneficial)
		for len(lp.denoiseWindow) >= 3 {
			before, candidate, after := lp.denoiseWindow[len(lp.denoiseWindow)-2], lp.denoiseWindow[len(lp.denoiseWindow)-1], next

			// hard to differentiate noise with higher accuracy values; maybe statistics similar to
			// our clustering phase can do better, but so far I haven't seen much need to denoise
			// points that are within ~100m accurate, esp. with clustering
			const thresholdMeters = 115
			if candidate.Uncertainty > thresholdMeters {
				avgSurroundingUncertainty := float64(before.Uncertainty+after.Uncertainty) / 2 //nolint:mnd

				// TODO: maybe should consider temporal locality as well (are the points close enough in time? -- might need to be relative to time deltas)
				if candidate.Uncertainty > avgSurroundingUncertainty*1.5 {
					// pop the last element, which is the candidate in the middle, as it looks like noise
					lp.denoiseWindow = lp.denoiseWindow[:len(lp.denoiseWindow)-1]

					// must loop to converge: the removal of the middle point from
					// our window means that two new points are now adjacent that
					// were never adjacent before; every time we pop we need to
					// "rewind" and bring back the last point that, at the time,
					// didn't look like noise, and now see how it compares with
					// its new neighbor; this is why the window size is generally
					// larger than 3: the extra spaces are to catch all noise when
					// multiple noisy points are consecutive
					continue
				}
			}

			// quick sanity check before we let it through: is it obviously wrong?
			elapsedSincePrev := candidate.Timestamp.Sub(before.Timestamp).Seconds()
			if elapsedSincePrev > 0 {
				velocity := candidate.distanceFromPrev / elapsedSincePrev
				const fasterThanCommercialJet = 350 // 350 m/s ~= 800 mph (commercial jets fly at < 600 mph)
				if velocity > fasterThanCommercialJet {
					// this person is likely NOT using their phone in the SR-71 Blackbird
					lp.denoiseWindow = lp.denoiseWindow[:len(lp.denoiseWindow)-1]
					continue
				}
			}

			// point appears useful; no need to discard an element and re-check
			break
		}

		lp.denoiseWindow = append(lp.denoiseWindow, next)
	}

	// pop the oldest point that has passed through the denoise filter as the newest point for the next phase
	if len(lp.denoiseWindow) > 0 {
		denoised := lp.denoiseWindow[0]
		lp.denoiseWindow = lp.denoiseWindow[1:]
		return denoised, nil
	}

	return nil, nil
}

// TODO: after decoding, skip if sameAsPrevious()

// sameAsPrevious returns true if the location l is deemed to be identical to
// or very similar to the previously-decoded point. Should be called just after
// being read from the source input.
func (lp locationProcessor) sameAsPrevious(l *Location) bool {
	if l.ResetTrack {
		lp.previous = nil
	}
	if lp.previous == nil {
		return false
	}

	if l.Timestamp.Sub(lp.previous.Timestamp) < 15*time.Second {
		return true
	}

	if l.LatitudeE7 == lp.previous.LatitudeE7 && l.LongitudeE7 == lp.previous.LongitudeE7 {
		return true
	}

	distanceKM := haversineDistanceEarth(lp.previous.LatitudeE7, lp.previous.LongitudeE7, l.LatitudeE7, l.LongitudeE7)
	distanceM := distanceKM * kmToMeters
	l.distanceFromPrev = distanceM // memoize (keep temporarily) even though we may need to change this if previous point changes (during denoising)
	const threshold = 5
	return distanceM < threshold // TODO: subtract accuracy as well? TODO: consider time as well? (like 12 hours or something)
}

const (
	denoiseWindowSize = 5
	clusterWindowSize = 4
)

//nolint:dupword
/*
	NOTES

	Welford's online algorithm, from: https://en.wikipedia.org/wiki/Algorithms_for_calculating_variance#Welford's_online_algorithm

		# For a new value newValue, compute the new count, new mean, the new M2.
		# mean accumulates the mean of the entire dataset.
		# M2 aggregates the squared distance from the mean.
		# count aggregates the number of samples seen so far.
		def update(existingAggregate, newValue):
			(count, mean, M2) = existingAggregate
			count += 1
			delta = newValue - mean
			mean += delta / count
			delta2 = newValue - mean
			M2 += delta * delta2
			return (count, mean, M2)

		# Retrieve the mean, variance and sample variance from an aggregate
		def finalize(existingAggregate):
			(count, mean, M2) = existingAggregate
			if count < 2:
				return float("nan")
			else:
				(mean, variance, sampleVariance) = (mean, M2 / count, M2 / (count - 1))
				return (mean, variance, sampleVariance)

	^ This assumes a calculation over the whole data set.
	If computing only over a sliding window, replace count
	with the windowSize, and delta is newValue-oldestValue
	instead of newValue-mean.

	Here's a simple Go implementation, though slightly contrived
	as the entire data set is passed in as a slice, but the idea
	works for streams or arbitrarily-long inputs too:

		func meanAndStandardDeviation(data []float64) (float64, float64) {
			if len(data) == 0 {
				return 0, 0
			}
			var mean, m2 float64
			for i, x := range data {
				delta := x - mean
				mean += delta / float64(i+1)
				delta2 := x - mean
				m2 += delta * delta2
			}
			variance := m2 / float64(len(data))
			stdDev := math.Sqrt(variance)
			return mean, stdDev
		}

*/

// Location represents a location data point that is about to be
// processed, is currently being processed, or has just been processed but
// is not yet an item. It is the input value to a LocationProcessor.
type Location struct {
	// Attach the original location value if you'll be needing it after processing.
	// (The location processor does not use this.) If the output ends up being a
	// cluster, only this value from the cluster's first point will be included.
	Original any

	// Populate these values. Coordinates are integer precision to 7 decimal places
	// to preserve arithmetic precision when doing calculations. Accuracy can be
	// omitted if unknown.
	LatitudeE7  int64     // degrees latitude times 1e7 (precision to ~1.1cm at equator)
	LongitudeE7 int64     // degrees longitude times 1e7
	Altitude    float64   // meters
	Uncertainty float64   // meters (must be > 0); higher values are less accurate
	Timestamp   time.Time // old exports used to call this timestampMs, in milliseconds

	// These fields are not read by the processor (you do not need to set them in your
	// implementation of NextLocation), but they will be set on the output if this
	// location becomes a cluster. Copy these output values into the final resulting item.
	Timespan time.Time
	Metadata timeline.Metadata

	// Typically, locations must be processed in chronological order in order to filter
	// noise, duplicates, etc. But if the stream of data has multiple time series and
	// "starts over" one or more times, set this to true when the stream has gone back
	// in time. As long as subsequent data points after this are still in ascending
	// chronological order, it's as if starting a new stream.
	ResetTrack bool

	distanceFromPrev float64 // distance from the location before this one in meters
	changeInMean     float64 // how much this point changed the mean
}

// Location converts the coordinates into a item-processable Location value.
func (lc Location) Location() timeline.Location {
	var l timeline.Location
	if lc.LatitudeE7 != 0 {
		lat := float64(lc.LatitudeE7) / placesMult
		l.Latitude = &lat
	}
	if lc.LongitudeE7 != 0 {
		lon := float64(lc.LongitudeE7) / placesMult
		l.Longitude = &lon
	}
	if lc.Altitude != 0 {
		alt := float64(lc.Altitude)
		l.Altitude = &alt
	}
	if lc.Uncertainty > 0 {
		unc := metersToApproxDegrees(lc.Uncertainty)
		l.CoordinateUncertainty = &unc
	}
	return l
}

const (
	kmToMeters = 1000
	places     = 7
	placesMult = 1e7
)
