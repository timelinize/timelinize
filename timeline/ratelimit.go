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
	"net/http"
	"time"
)

// RateLimit describes a rate limit.
type RateLimit struct {
	RequestsPerHour int `json:"requests_per_hour,omitempty"`
	BurstSize       int `json:"burst_size,omitempty"`

	ticker *time.Ticker
	token  chan struct{}
}

// NewRateLimitedRoundTripper adds rate limiting to rt based on the rate limiting policy.
func (acc Account) NewRateLimitedRoundTripper(rt http.RoundTripper, rl RateLimit) http.RoundTripper {
	rl, ok := acc.t.rateLimiters[acc.ID]

	if !ok && rl.RequestsPerHour > 0 {
		secondsBetweenReqs := 60.0 / (float64(rl.RequestsPerHour) / 60.0)
		millisBetweenReqs := secondsBetweenReqs * 1000.0
		reqInterval := time.Duration(millisBetweenReqs) * time.Millisecond
		if reqInterval < minInterval {
			reqInterval = minInterval
		}

		rl.ticker = time.NewTicker(reqInterval)
		rl.token = make(chan struct{}, rl.BurstSize)

		for i := 0; i < cap(rl.token); i++ {
			rl.token <- struct{}{}
		}
		go func() {
			for range rl.ticker.C {
				rl.token <- struct{}{}
			}
		}()

		acc.t.rateLimiters[acc.ID] = rl
	}

	return rateLimitedRoundTripper{
		RoundTripper: rt,
		token:        rl.token,
	}
}

type rateLimitedRoundTripper struct {
	http.RoundTripper
	token <-chan struct{}
}

func (rt rateLimitedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	<-rt.token
	return rt.RoundTripper.RoundTrip(req)
}

// var rateLimiters = make(map[string]RateLimit)

const minInterval = 100 * time.Millisecond
