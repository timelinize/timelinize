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
	"slices"
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

type LocationProcessingOptions struct {
	// Set to a value 1-10 to enable path simplification.
	// 10 means very aggressive simplification (skip many
	// points, leave practically only clusters or endpoints)
	// and 1 means to only drop points on the straightest paths.
	// (My preferred is ~2 when scaled to between 1000 and 50000; i.e. about epsilon=6-7k)
	Simplification float64 `json:"simplification,omitempty"`

	// Points are clustered together if the mean change of the centroid of
	// points in a sliding window is less than the standard deviation of
	// the points times some coefficient based on the size of the window.
	// This coefficient can be scaled up or down to make clustering more
	// or less aggressive. Must be a non-negative value. Zero disables
	// clustering; the default behavior is enabled with a value of 1.0.
	// TODO: Experimental. Clustering algorithm WIP. Likely to change.
	ClusteringCoefficient float64 `json:"clustering_coefficient,omitempty"`
}

// NewLocationProcessor returns a new location source that filters, refines, clusters,
// and possibly simplifies the locations it reads from source.
//
// The returned locations may have new or different values/information from what
// the input values were. The fields on the outputted locations should be preferred
// over the inputted locations for filling in item structs. Of special note are
// timespan and metadata fields: timespan and metadata will be set if the point
// represents a cluster, so those fields should be added to the resulting item.
func NewLocationProcessor(source LocationSource, options LocationProcessingOptions) (LocationSource, error) {
	if options.Simplification < 0 || options.Simplification > 10 {
		return nil, fmt.Errorf("invalid simplification factor; must be in [1,10]: %f", options.Simplification)
	}
	if options.ClusteringCoefficient < 0 {
		return nil, fmt.Errorf("invalid clustering coefficient; must be >= 0: %f", options.ClusteringCoefficient)
	}

	locProc := &locationProcessor{
		source:               source,
		clusteringBuffer:     make([]*Location, 0, clusterBufferSize),
		clusteringCoeffScale: options.ClusteringCoefficient,
	}

	if options.Simplification != 0 {
		// To scale a number x into range [a,b]:
		// x_scaled = (b-a) * ((x - x_min) / (x_max - x_min)) + a
		//
		// 10,000 is quite a high epsilon and, indeed, it does thin out the path quite significantly,
		// but the algorithm is quite amazing in that it does preserve the essence of the path.
		// I recommend no higher than ~1000 for a default epsilon, for most data sets. That'd be
		// an input simplification of ~1.0. Users can set this lower if they want more points
		// or higher if they want less. The high range is somewhat necessary to accommodate
		// different spreads of data.
		const xMin, xMax = 0.0, 10.0
		const epsMin, epsMax = 10.0, 10000.0
		locProc.epsilon = (epsMax-epsMin)*((options.Simplification-xMin)/(xMax-xMin)) + epsMin
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
	source        LocationSource
	sourceDrained bool // if true, no more input points remain from the source

	// de-duplicating (TODO: Make more customizable?)
	previous *Location
	// minTemporalSpacing time.Duration
	// minDistanceMeters  int

	// denoising
	denoiseWindow []*Location

	// clustering
	clusteringBuffer     []*Location
	startOfNextTrack     *Location
	clusteringCoeffScale float64

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

func (lp *locationProcessor) clusteredNext(ctx context.Context) (*Location, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// if we have processed points left in the buffer, pop off the next one and return it
	// (leave at least clusterWindowSize points in the buffer to roll-over to the next
	// batch, so that we don't break clusters across arbitrary batch boundaries - but if
	// there are no more points, or if there is a new track, then we must completely drain
	// the buffer)
	if len(lp.clusteringBuffer) > clusterWindowSize ||
		((lp.sourceDrained || lp.startOfNextTrack != nil) && len(lp.clusteringBuffer) > 0) {
		next := lp.clusteringBuffer[0]
		lp.clusteringBuffer = lp.clusteringBuffer[1:]
		return next, nil
	}

	// no more points here or there, no more points anywhere!
	if lp.sourceDrained && lp.startOfNextTrack == nil {
		return nil, nil
	}

	// refill buffer and process new batch
	// (start with the first point of new/next track, if any)
	if lp.startOfNextTrack != nil {
		lp.clusteringBuffer = append(lp.clusteringBuffer, lp.startOfNextTrack)
		lp.startOfNextTrack = nil
	}
	for len(lp.clusteringBuffer) < clusterBufferSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nextPoint, err := lp.denoiseNext(ctx)
		if err != nil {
			return nil, err
		}
		if nextPoint == nil {
			break
		}
		if nextPoint.NewTrack {
			// we just read a point that must not be clustered with any of the previous ones,
			// so instead of appending it to the buffer to be processed along with them, we
			// hold onto it and it will be the first point in the next buffer that we process
			lp.startOfNextTrack = nextPoint
			break
		}
		lp.clusteringBuffer = append(lp.clusteringBuffer, nextPoint)
	}
	lp.clusteringBuffer = lp.clusterBatch(ctx, lp.clusteringBuffer)

	// buffer has been refilled and processed, so pop its first point off
	return lp.clusteredNext(ctx)
}

// clusterBatch processes the points in the batch into clusters. It returns the processed batch.
func (lp *locationProcessor) clusterBatch(ctx context.Context, batch []*Location) []*Location {
	if len(batch) == 0 {
		return batch
	}

	// Our clustering algorithm is "recursive" in that it can add clusters to clusters.
	// We slide a window across the batch and make clusters as we go, then repeat once
	// we reach the end of the batch until this loop has an iteration where no new
	// clusters have been created. In practice it tends to converge quite quickly.
	// However, I have found some cases where, even though it converges quickly, it
	// creates clusters that are TOO big. So we limit the "recursion" (iterations of
	// this loop). Otherwise, I've seen it create clusters of clusters of clusters,
	// which end up being hundreds of points big, and spanning an entire week...
	// that's not usually necessary/useful. In practice, I have found that two scans
	// over the batch is sufficient to cluster effectively.
	for range 2 {
		// We keep track of the centroid of the sliding window.
		var latAvg, lonAvg int64

		// As we slide along, if we are in a cluster, we move points into this list.
		// We keep track of where the cluster started because the resulting cluster
		// point will replace all the points in the batch starting at that same spot
		// the cluster started. The cluster must consist of consecutive points.
		var cluster []*Location
		var clusterStart int
		var hadCluster bool

		// finalizeCluster just creates the cluster point, resets the cluster, and
		// inserts the cluster point into the batch.
		finalizeCluster := func() {
			clusterPoint := lp.endClusterFunc(cluster)
			cluster = nil
			batch = slices.Insert(batch, clusterStart, clusterPoint)
		}

		// slide the window over the batch;  the current index is the "latest" point,
		// which is the end of the clustering window (the beginning is i-clusterWindowSize
		// or 0, whichever is greater).
		for i := 0; i < len(batch); i++ {
			if ctx.Err() != nil {
				return batch
			}

			latest := batch[i] // end of the sliding window

			// for the first point in a batch, we need to initialize our mean for
			// the statistical analysis
			if i == 0 {
				latAvg = latest.LatitudeE7
				lonAvg = latest.LongitudeE7
				continue // this prevents NaNs at the beginning due to not having a starting lat/lon
			}

			// compute the centroid of the current window, and compare to centroid of previous window

			oldLatAvg, oldLonAvg := latAvg, lonAvg

			oldestIdx := max(i-clusterWindowSize, 0) // beginning of the clustering window (i is the end of it)
			oldest := batch[oldestIdx]
			oldestLat, oldestLon := oldest.LatitudeE7, oldest.LongitudeE7
			latAvg += (latest.LatitudeE7 - oldestLat) / clusterWindowSize
			lonAvg += (latest.LongitudeE7 - oldestLon) / clusterWindowSize

			// convert to meters to reduce sensitivity to floating point imprecision
			latest.changeToCentroid = haversineDistanceEarth(oldLatAvg, oldLonAvg, latAvg, lonAvg) * kmToMeters

			// size of the window may be smaller at the beginning
			count := min(clusterWindowSize, i+1)

			// we don't actually use Welford's online algorithm here, because
			// it is not numerically stable when applied to a sliding window
			// (thankfully, our window size is usually small that these
			// inner loops aren't that expensive in practice)

			window := batch[oldestIdx : i+1]

			var windowDistance float64

			// First pass: compute mean (and distance traveled within the window)
			// (Note that our mean is not the centroid itself but the average DELTA/CHANGE of the centroid in the window.)
			var meanChangeToCentroid float64
			for i, point := range window {
				if i > 0 { // skip first point in window since the distance between it and its previous is not in the window
					windowDistance += point.distanceFromPrev
				}
				meanChangeToCentroid += point.changeToCentroid
			}
			meanChangeToCentroid /= float64(count)

			// Second pass: compute sum of squared differences
			var M2 float64
			for _, point := range window {
				diff := point.changeToCentroid - meanChangeToCentroid
				M2 += diff * diff
			}

			// sample variance (population variance just doesn't have the -1)
			variance := M2 / (float64(count) - 1)

			// compute distance from the previous point to this one
			prev := batch[i-1]
			latest.distanceFromPrev = haversineDistanceEarth(prev.LatitudeE7, prev.LongitudeE7, latest.LatitudeE7, latest.LongitudeE7) * kmToMeters

			// standard deviation is in same units as the data
			stdDev := math.Sqrt(variance)

			// We need to decide a coefficient at which a point is part of a cluster, based on what we observe
			// in the window. A simple way to do this is if the mean change of the centroid is less than a
			// certain amount (i.e. there isn't much overall movement within the window), then it's part of
			// a cluster. The question is, what amount should that be? Well this used to be a hard-coded value
			// of 20 meters, but that doesn't work well with different kinds of data (e.g. walking data, too
			// many of the points get eaten up into clusters because walking has higher data density than driving).
			// So we make it relative to the data set by computing its standard deviation and then seeing if
			// that mean centroid delta is less than some multiple of the standard deviation. (Remember that
			// std dev is in the same units as the data.) This works better, but the coefficient now becomes a
			// parameter to tune. Because again, we may want to express dense data differently than sparse data.
			// After some fiddling I've landed on setting the coefficient based on the distance traveled in the
			// window. Based on some sample data, it FEELS LIKE the more distance that is traveled, the tighter
			// the threshold should be for a cluster (i.e. more distance traveled == centroid needs to move less).
			// I don't have a smooth formula for this coefficient yet, so I chose some discrete points at which
			// we determine the coefficient. It's kind of a sensitive parameter. This approach could probably be
			// improved.
			var coefficient float64
			switch {
			case windowDistance < 75:
				coefficient = 2.5 // seems to work well when points are close together
			case windowDistance < 300:
				coefficient = 1.9 // 1.9 seemed good on sample Arc tracks from urban walks
			default:
				coefficient = 1.15 // 1.15 seems good for most driving+destination data
			}
			coefficient *= lp.clusteringCoeffScale

			// it's important that we're at least a window-size into the buffer, because the oldest
			// point in the window is appended to the cluster; if we did this before we were one full
			// window into the buffer, the oldest point would repeat multiple times as the window grows!
			if count >= clusterWindowSize && meanChangeToCentroid < coefficient*stdDev {
				// we are in a cluster!

				// if this is the start of a cluster, we need to mark where it started
				// so we know where to put the cluster point
				if len(cluster) == 0 {
					clusterStart = oldestIdx
				} else {
					// a cluster of size 1 ends up just going back into the buffer as itself,
					// not actually as a cluster, so don't count cluster sizes of 1, otherwise
					// it would loop infinitely
					hadCluster = true
				}

				// add this point to the cluster, remove it from the batch,
				// and decrement the index since we deleted it from the batch
				cluster = append(cluster, oldest)
				batch = slices.Delete(batch, oldestIdx, oldestIdx+1)
				i--

				// while in a cluster, we need to continue looping until we find
				// the end of the cluster
				//
				// "what about if we're at the end of the batch and we're still
				// in the cluster -- i.e. the cluster goes into the next batch?"
				// you may ask -- well, the simplest thing, which seems to work,
				// is to just end the cluster like normal after the loop finishes,
				// and since we roll-over 1 window size of overlap between batches,
				// that cluster point will be considered at the start of the next
				// batch, so it can be appended to then
				continue
			} else if clusterSize := len(cluster); clusterSize > 0 {
				// not in cluster, but cluster points have accumulated, so must be end of cluster
				finalizeCluster()
				i++ // since we added a cluster point to the batch, increment i to skip over it
				continue
			}
		}

		// if we finished the batch in a cluster, make sure to create the cluster point
		if len(cluster) > 0 {
			finalizeCluster()
		}

		// if this scan of the batch had no new clusters, we're done!
		if !hadCluster {
			break
		}
	}

	return batch
}

// endClusterFunc ends the cluster by combining all the clustered
// points into a single location and returns it.
func (lp *locationProcessor) endClusterFunc(cluster []*Location) *Location {
	if len(cluster) == 1 {
		return cluster[0]
	}

	// expand cluster points (points that are already clusters themselves)
	for i := 0; i < len(cluster); i++ {
		clusterPoints := cluster[i].clusterPoints
		cluster = slices.Insert(cluster, i, clusterPoints...)
		i += len(clusterPoints)
	}

	clusterSize := float64(len(cluster))

	// combine all the points in the cluster into a single point;
	// to do this, aggregate some statistics and other values
	// as part of the reduction
	var uncertaintySum, centerLat, centerLon, m2lat, m2lon float64

	for i, l := range cluster {
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

	first, last := cluster[0], cluster[len(cluster)-1]

	clusterPoint := &Location{
		Original:    first.Original, // not sure if this makes sense, but anyway...
		LatitudeE7:  int64(centerLat),
		LongitudeE7: int64(centerLon),
		Timestamp:   first.Timestamp,
		Timespan:    last.Timestamp,
		Uncertainty: meanUncertainty,
		Metadata: timeline.Metadata{
			"Cluster size": len(cluster),
			// convert to actual lat/lon units; I don't think we need to stay at 1e7 integers
			"Standard deviation (latitude)":  math.Sqrt(latVariance) / placesMult,
			"Standard deviation (longitude)": math.Sqrt(lonVariance) / placesMult,
		},
		clusterPoints: cluster,
	}

	return clusterPoint
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
			lp.sourceDrained = true
			break
		}
		if next.Significant {
			lp.previous = next
			return next, nil
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
	if l.NewTrack {
		lp.previous = nil
	}
	if lp.previous == nil {
		return false
	}

	if l.Timestamp.Sub(lp.previous.Timestamp) < 5*time.Second {
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
	clusterBufferSize = 1000 // size of each batch of points when clustering
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
				(mean, variance, sampleVariance) = (mean, M2 / count, M2 / (count-1))
				return (mean, variance, sampleVariance)

	^ This assumes a calculation over the whole data set.
	If computing only over a sliding window, replace count
	with the windowSize, and delta is newValue-oldestValue
	instead of newValue-mean. However, this is numerically
	unstable. Negative variances will likely appear.

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
	Significant bool      // if true, this point will not be filtered out (may still be clustered, so if you need it, call ClusterPoints() to get it)

	// These fields are not read by the processor (you do not need to set them in your
	// implementation of NextLocation), but they will be set on the output if this
	// location becomes a cluster. Copy these output values into the final resulting item.
	Timespan time.Time
	Metadata timeline.Metadata

	// Typically, locations must be processed in chronological order in order to filter
	// noise, duplicates, etc. And locations may be clustered into a single point if
	// they look like a cluster. But if the stream of data has multiple time series or
	// "starts over" one or more times, set this to true when the stream has gone back
	// in time or when a new track has begun. As long as subsequent data points after
	// this are still in ascending chronological order, it's as if starting a new stream.
	// Points will not be clustered across different tracks.
	NewTrack bool

	distanceFromPrev float64 // distance from the location before this one in meters
	changeToCentroid float64 // how much this point changed the mean
	clusterPoints    []*Location
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

// ClusterPoints returns a slice of the original/input points that were
// aggregated to make the cluster, if this point is a cluster. A nil
// value is returned if this point is not a cluster. The slice should
// not be modified if you want to call this to get the same values again
// later.
func (lc Location) ClusterPoints() []*Location {
	return lc.clusterPoints
}

const (
	kmToMeters = 1000
	places     = 7
	placesMult = 1e7
)
