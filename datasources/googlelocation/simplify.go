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
	"math"
)

// A Go implementation of the Ramer-Douglas-Peucker Algorithm.
// Interactive demo and information: https://karthaus.nl/rdp/
//
// Borrowed from github.com/calvinfeng/rdp-path-simplification and
// heavily modified by me.
//
// As explained by Karthaus:
//
//	"The Ramer-Douglas–Peucker algorithm is an algorithm for reducing the
//	number of points in a curve that is approximated by a series of points.
//	It does so by "thinking" of a line between the first and last point in
//	a set of points that form the curve. It checks which point in between
//	is farthest away from this line. If the point (and as follows, all other
//	in-between points) is closer than a given distance 'epsilon', it removes
//	all these in-between points. If on the other hand this 'outlier point'
//	is farther away from our imaginary line than epsilon, the curve is split
//	n two parts:
//
//	1. From the first point up to and including the outlier
//	2. The outlier and the remaining points.
//
//	The function is recursively called on both resulting curves, and the two
//	reduced forms of the curve are put back together."
//
// This implementation was modified to always preserve points that represent
// clusters, which I feel are too important to drop for the purposes of
// simplification. Clusters provide a valuable condensation of information.
func simplifyPath(points []*Location, ep float64) []*Location {
	const dimensions = 2
	if len(points) <= dimensions {
		return points
	}

	l := line{points[0], points[len(points)-1]}

	idx, maxDist := seekMostDistantPoint(l, points)
	if maxDist >= ep || maxDist == -1 {
		left := simplifyPath(points[:idx+1], ep)
		right := simplifyPath(points[idx:], ep)
		return append(left[:len(left)-1], right...)
	}

	// if the most distant point is still too close, then just return the two end points
	return []*Location{points[0], points[len(points)-1]}
}

// seekMostDistancePoint returns the index of the most distant point from the line,
// using perpendicular distance calculation (may not be ideal on a circle...) -- this
// is modified to also return for all cluster points, as we don't want to lose those,
// in that case the maxDist returned will be -1.
func seekMostDistantPoint(l line, points []*Location) (idx int, maxDist float64) {
	// The library I borrowed this from has an infinite recursion bug that I tracked
	// down and patched, but they have not merged it even a year later:
	// https://github.com/calvinfeng/rdp-path-simplification/pull/1/
	// this for loop has the patch (start at 1, and use -1 in the while part,
	// instead of starting at 0 and not having the -1)
	for i := 1; i < len(points)-1; i++ {
		// don't drop clusters; too important
		if !points[i].Timespan.IsZero() || points[i].Significant {
			return i, -1
		}
		if d := l.distanceToPoint(points[i]); d > maxDist {
			maxDist = d
			idx = i
		}
	}
	return idx, maxDist
}

type line struct {
	start, end *Location
}

// distanceToPoint returns the perpendicular distance of a point to the line.
// TODO: probably need to implement some great circle formula here for best results, since our coordinates are not cartesian...
func (l line) distanceToPoint(pt *Location) float64 {
	a, b, c := l.coefficients()
	return math.Abs(float64(a*pt.LatitudeE7+b*pt.LongitudeE7+c)) / math.Sqrt(float64(a*a+b*b))
}

// coefficients returns the three coefficients that define a line.
// A line can represent by the following equation.
//
// ax + by + c = 0
func (l line) coefficients() (a, b, c int64) {
	a = l.start.LongitudeE7 - l.end.LongitudeE7
	b = l.end.LatitudeE7 - l.start.LatitudeE7
	c = l.start.LatitudeE7*l.end.LongitudeE7 - l.end.LatitudeE7*l.start.LongitudeE7
	return a, b, c
}
