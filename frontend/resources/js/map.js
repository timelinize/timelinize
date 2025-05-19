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

// TODO: un-global these
var mapData = {};
let coordsToItems = {};
const coordPrecision = 6;

function addMapItemToCoordinate(mapItem) {
	if (mapItem.item && !coordsToItems[mapItem.coordsStr].find(elem => elem.id == mapItem.item.id)) {
		coordsToItems[mapItem.coordsStr].push(mapItem.item);
	}
	if (mapItem.adjacent) {
		for (const adjacentItem of mapItem.adjacent) {
			if (adjacentItem && !coordsToItems[mapItem.coordsStr].find(elem => elem.id == adjacentItem.id)) {
				coordsToItems[mapItem.coordsStr].push(adjacentItem);
			}
		}
	}
}

const popup = new mapboxgl.Popup({
	maxWidth: '247px',
	closeButton: false,
	closeOnClick: false,
	className: 'preview'
});

async function loadAndRenderMapData() {
	// like a synchronous forEach (waits for all async funcs to finish), see: https://stackoverflow.com/a/37576787/1048862
	await Promise.all(tlz.openRepos.map(async repo => {
		// asynchronously load and render the heatmap (query could be slow)
		loadAndRenderHeatmap(repo);

		const params = mapPageFilterParams(repo);
		if ($('.date-input').datepicker.selectedDates.length == 0) {
			const presearch = await app.SearchItems({
				repo: repo.instance_id,
				min_latitude: -90,
				max_latitude: 90,
				min_longitude: -180,
				max_longitude: 180,
				limit: 1,
				flat: true
			});
			if (presearch.items && presearch.items[0]?.timestamp) {
				params.start_timestamp = new Date(presearch.items[0].timestamp);
				params.end_timestamp = new Date(presearch.items[0].timestamp);
				params.start_timestamp.setHours(0, 0, 0, 0);
				params.end_timestamp = DateTime.fromJSDate(params.start_timestamp).plus({ days: 1 }).toJSDate();
				$('.date-input').datepicker.selectDate(params.start_timestamp);
			}
		}
		const results = await app.SearchItems(params);

		console.log("PARAMS AND RESULTS:", params, results);

		// create list of items that can be rendered on the map; we need to isolate/count
		// them so we can compute actual timeframe bounds for step colors and legend;

		// prepare map data: associate each coordinate with its information & color
		let newMapData = { items: [], params: params, totalDistance: 0 };
		for (let i = 0; i < results.items?.length; i++) {
			const item = results.items[i];
			
			if (!canRenderOnMap(item)) {
				continue;
			}

			// confine items that are (nearly) on top of each other to the same marker;
			// this is a rough approximation that doesn't work near the poles, but keeps
			// items that are very close or exactly the same point from obsctructing
			// others, by grouping them into the same marker
			let lonStr = item.longitude.toString();
			let latStr = item.latitude.toString();
			const lonDigits = lonStr.indexOf('.');
			const latDigits = latStr.indexOf('.');
			lonStr += "0".repeat(Math.max(lonDigits+coordPrecision+1 - lonStr.length, 0))
			latStr += "0".repeat(Math.max(latDigits+coordPrecision+1 - latStr.length, 0))
			lonStr = lonStr.substring(0, lonDigits+coordPrecision+1);
			latStr = latStr.substring(0, latDigits+coordPrecision+1);
			const coordsStr = `${lonStr}, ${latStr}`;

			const mapItem = {
				item,
				coords: [item.longitude, item.latitude],
				coordsStr: coordsStr,
				ts: DateTime.fromISO(item.timestamp)
			};

			if (newMapData.items.length > 0) {
				const prev = newMapData.items[newMapData.items.length-1];
				const distanceFromPrev = haversineDistance(prev.coords, mapItem.coords);
				newMapData.totalDistance += distanceFromPrev;
				mapItem.distanceFromStart = newMapData.totalDistance;
			} else {
				mapItem.distanceFromStart = 0;
			}

			newMapData.items.push(mapItem);
		}

		// compute actual time range (time between first and last item)
		let actualTimeframeSeconds = 0, firstItemTimestamp, lastItemTimestamp;
		if (newMapData.items.length > 0) {
			firstItemTimestamp = newMapData.items[0].ts;
			lastItemTimestamp = newMapData.items[newMapData.items.length - 1].ts;
			actualTimeframeSeconds = Math.abs(firstItemTimestamp.diff(lastItemTimestamp).toFormat('s'));
		}

		for (let i = 0; i < newMapData.items.length; i++) {
			const mapItem = newMapData.items[i];

			// we compute the number of seconds into the actual timeframe of the map
			// so that we can get the color scaled correctly
			const itemSecondsIntoActualTimeframe = Number(mapItem.ts.diff(firstItemTimestamp).toFormat('s'));

			// Hue calculation is based on simple linear y = mx+b formula,
			// where y is the hue, m is the step size (hue change rate; this is
			// of course "rise/run", so with hue being on our y-axis, that's "rise",
			// and with time being on x-axis, that's "run", so it's hue range over
			// time range), x is the current step (offset from start of timeframe)
			// and b is our starting hue (y-intercept). Note that our hue range is
			// NOT the difference from start to end -- that would be if we take
			// the shortest path -- but actually, our gradient takes us through
			// maxHue, which is 360 (or 0), so we are careful to factor that into
			// our hueRange calculation; otherwise we either go out of bounds or do
			// not use full range (first item should be startHue, last should be
			// endHue, having gone through maxHue, or 0, along the way).
			// Since we need to count DOWN from startHue to stay in range, we start at
			// maxHue and subtract the current progress then add the startHue.
			// i.e: "hue = maxHue - ((hueRange/timeRange) * timeOfs) + startHue" (modulo maxHue)
			const startHue = 55;
			const maxHue = 360;
			const endHue = 240;
			const hueRange = startHue + (maxHue - endHue); // distance traveled from startHue to 0/360, then from 0/360 to endHue
			const scaledProgress = ((hueRange / actualTimeframeSeconds) * itemSecondsIntoActualTimeframe)
			const unboundedHue = maxHue - scaledProgress + startHue;
			const hue = unboundedHue % maxHue;
			const color = `hsl(${hue}deg, 100%, 55%)`;
			mapItem.color = color;
			
			makeMarker(repo, mapItem);
		}

		// update legend labels
		if (newMapData.items.length > 0) {
			$('.color-legend-start').innerText = DateTime.fromISO(newMapData.items[0].item.timestamp).toLocaleString(DateTime.DATETIME_FULL);
			$('.color-legend-end').innerText = DateTime.fromISO(newMapData.items[newMapData.items.length-1].item.timestamp).toLocaleString(DateTime.DATETIME_FULL);
		}

		// TODO: I don't think this will work cleanly if there's more than 1 open repo
		await renderMapData(newMapData);
	}));
}

function makeMarker(repo, mapItem) {
	const item = mapItem.item;

	if (!coordsToItems[mapItem.coordsStr]) {
		coordsToItems[mapItem.coordsStr] = [];
	}
	
	// check if item is only a basic location point (not a cluster),
	// i.e. not an item of substance or interest
	const isBasicLocation = item.classification == "location" && !item.metadata?.["Cluster size"];

	// a "map item" could have two markers: one for the basic
	// location dot, another a balloon for adjacent item(s)
	if (!mapItem.markers) {
		mapItem.markers = [];
	}

	// collect the items from this mapItem, and flatten them into a list for the marker
	addMapItemToCoordinate(mapItem);

	// if item is only a location (and is not representing a cluster of points),
	// it gets only a simple colored dot since there's no real substance
	if (isBasicLocation && !mapItem?.adjacent?.length) {
		// if dot marker already exists, simply associate the mapItem with it and return
		if (coordsToItems[mapItem.coordsStr].dotMarker) {
			mapItem.markers.push(coordsToItems[mapItem.coordsStr].dotMarker);
			return;
		}

		const markerElem = document.createElement('div');
		markerElem.innerHTML = `<svg
			class="location-dot"
			width="12"
			height="12"
			viewBox="0 0 100 100"
			version="1.1"
			xmlns="http://www.w3.org/2000/svg">
				<circle cx="50" cy="50" r="50" style="fill: ${mapItem.color};"/>
			</svg>`;
		coordsToItems[mapItem.coordsStr].dotMarker = new mapboxgl.Marker(markerElem).setLngLat(mapItem.coords);
		mapItem.markers.push(coordsToItems[mapItem.coordsStr].dotMarker);

		// show timestamp on hover
		markerElem.addEventListener('mouseenter', e => {
			popup.setLngLat(mapItem.coords)
				.setHTML(`${mapItem.ts.toLocaleString(DateTime.DATETIME_FULL)}`)
				.setOffset(0)
				.addTo(tlz.map);
		});
		markerElem.addEventListener('mouseleave', e => {
			popup.remove();
		});

		return;
	}

	// show a "balloon" pin since the item is worthy of being clicked on
	// (item has substance/content, or if it is a basic location, we know it
	// has at least one adjacent/sidecar item to display)

	// if the marker already exists, simply associate the marker with the mapItem and return
	if (coordsToItems[mapItem.coordsStr].balloonMarker) {
		mapItem.markers.push(coordsToItems[mapItem.coordsStr].balloonMarker);
		return;
	}

	// the marker does not yet exist, so create it

	const itemOfInterest = isBasicLocation ? mapItem.adjacent[0] : item; // TODO: marker may represent multiple geolocated items, but just choose one for an icon
	const balloonColor = isBasicLocation ? 'rgb(190,190,190)' : mapItem.color;

	const markerElem = cloneTemplate('#tpl-map-marker');
	$('.pin', markerElem).style.fill = balloonColor;
	$('.marker-icon', markerElem).innerHTML = tlz.itemClassIconAndLabel(itemOfInterest, true).icon;

	coordsToItems[mapItem.coordsStr].balloonMarker = new mapboxgl.Marker({
		element: markerElem,
		offset: [0, -20]
	}).setLngLat(mapItem.coords);
	mapItem.markers.push(coordsToItems[mapItem.coordsStr].balloonMarker);


	markerElem.addEventListener('click', e => {
		// if already selected, simply deselect this item
		if (markerElem.classList.contains('active')) {
			hideMapPageInfoCard();
			return;
		}
		
		// deselect any other active item and select this one
		$$('.mapboxgl-marker.active').forEach(el => {
			if (el == markerElem) return;
			el.classList.remove('active');
		});
		markerElem.classList.add('active');
		
		// fill in the preview box
		$('#infocard .date').innerText = mapItem.ts.toLocaleString(DateTime.DATE_HUGE);
		$('#infocard .time').innerText = mapItem.ts.toLocaleString(DateTime.TIME_WITH_SECONDS);

		// display items in a timeline, careful to not use a map :)
		$('#infocard .map-preview-content').replaceChildren(itemsAsTimeline(coordsToItems[mapItem.coordsStr], { noMap: true, autoplay: true }));

		$('#infocard .view-details').href = `/items/${repo.instance_id}/${item.id}`;

		// when ready, display!
		$('#infocard').classList.remove('d-none');

		tlz.map.easeTo({
			center: mapItem.coords,
			padding: {"left": window.innerWidth > 1000 ? $('#infocard').offsetWidth : 0 },
			duration: 500
		});
	});

	// quick preview on hover
	markerElem.addEventListener('mouseenter', e => {
		const itemsForPreview = coordsToItems[mapItem.coordsStr].filter(item => item.classification != 'location');
		const previewEl = itemPreviews(itemsForPreview);

		const containerEl = cloneTemplate('#tpl-map-popup');
		const markerCoords = coordsToItems[mapItem.coordsStr].balloonMarker.getLngLat();
		$('.map-preview-coordinates', containerEl).innerText = `${markerCoords.lat.toFixed(coordPrecision)}, ${markerCoords.lng.toFixed(coordPrecision)}`;
		$('.map-preview-timestamp', containerEl).innerText = `${mapItem.ts.toLocaleString(DateTime.DATETIME_FULL)}`;
		containerEl.append(previewEl);

		popup.setLngLat(mapItem.coords)
			.setDOMContent(containerEl)
			.setOffset({
				'bottom': [0, -40],
				'bottom-left': [0, -40],
				'bottom-right': [0, -40],
				'left': [15, -20],
				'right': [15, -20]
			})
			.addTo(tlz.map);
	});
	markerElem.addEventListener('mouseleave', e => {
		popup.remove();
	});
}

async function renderMapData(newMapData) {
	const dataToRender = newMapData || mapData.results;

	if (!dataToRender) return;

	// show basic nav controls if not already present
	if (!tlz.map.hasControl(tlz.map.tl_navControl)) {
		tlz.map.addControl(tlz.map.tl_navControl);
		if ($('#toggle-3d').checked) {
			tlz.map.dragRotate.enable();
		} else {
			tlz.map.dragRotate.disable();
		}
	}

	// remove previous layers/sources
	// if (tlz.map.getLayer('route')) {
		tlz.map.tl_removeLayer('route');
	// }
	// if (tlz.map.getSource('journey')) {
		tlz.map.tl_removeSource('journey');
	// };

	// only draw connecting lines if it makes sense to
	if (temporallyConsistent(dataToRender.params) && dataToRender.items.length) {
		// connect markers with lines
		// EXAMPLE: https://docs.mapbox.com/mapbox-gl-js/example/line-gradient/
		const geojsonLines = {
			'type': 'FeatureCollection',
			'features': [
				{
					'type': 'Feature',
					'properties': {},
					'geometry': {
						'coordinates': dataToRender.items.map(item => item.coords),
						'type': 'LineString'
					}
				}
			]
		};
		const lineGradient = function () {
			let arr = []; // (gradient stop, color) pairs
			for (let i = 0; i < dataToRender.items.length; i++) {
				// We add a very small amount to ensure that items with equal distanceFromStart values still
				// appear in a "strictly ascending order" as required for interpolation by the mapbox lib,
				// even if we may very slightly overshoot 1.0.
				const distanceProgress = Math.min((dataToRender.items[i].distanceFromStart / dataToRender.totalDistance) + i/1000000000, 100.0);
				arr.push(distanceProgress, dataToRender.items[i].color);
			}
			return arr;
		}

		const addSource = function() {
			tlz.map.tl_addSource('journey', {
				type: 'geojson',
				lineMetrics: true,
				data: geojsonLines
			});
		};
		if (tlz.map.isStyleLoaded()) {
			addSource();
		} else {
			tlz.map.on('load', addSource);
		}
		tlz.map.tl_addLayer({
			id: "route",
			type: "line",
			source: "journey",
			paint: {
				"line-width": 5,
				'line-gradient': [
					'interpolate',
					['linear'],
					['line-progress'],
					...lineGradient()
				]
			},
			layout: {
				'line-cap': 'round',
				'line-join': 'round'
			}
		});
	}

	// remove old markers, add new markers
	tlz.map.tl_clearMarkers();
	dataToRender.items.forEach(mapItem => mapItem.markers.forEach(marker => tlz.map.tl_addMarker(marker)));

	// fly to bounding box of data if requisite
	if (newMapData && dataToRender.items.length) {
		let bounds = new mapboxgl.LngLatBounds();
		dataToRender.items.forEach(item => bounds.extend(item.coords));
		tlz.map.fitBounds(bounds, { padding: 100, maxZoom: 16 });
	}

	// replace the current map data with what is rendered
	mapData.results = dataToRender;


	if (newMapData) {
		// show temporally-adjacent data - TODO: experimental! This is an attempt to show related items by temporal locality
		let features = [];
		await Promise.all(newMapData.items.map(async (dataPoint, idx) => {
			// get timestamp of previous item and next item: the halfway point
			// to either side determines our time range:
			//
			// [prev item]-----<min TS>-----[this item]-----<max TS>-----[next item]
			let prevTs = newMapData.items?.[idx - 1]?.item.timestamp;
			let nextTs = newMapData.items?.[idx + 1]?.item.timestamp;

			// if we're at one end of the list, just use the current
			// timestamp for the other side
			prevTs = prevTs || dataPoint.item.timestamp;
			nextTs = nextTs || dataPoint.item.timestamp;

			const dt = DateTime.fromISO(dataPoint.item.timestamp);

			// as a last resort, choose an arbitrary span of time if that's all we can do;
			// and convert to luxon DT values so we can split the differences...
			let prevDT = prevTs ? DateTime.fromISO(prevTs) : dt.minus({ minutes: 30 });
			let nextDT = nextTs ? DateTime.fromISO(nextTs) : dt.plus({ minutes: 30 });

			// if location data is too far apart in time, arbitrarily cap the time span
			prevDT = DateTime.max(prevDT, dt.minus({ hours: 12 }));
			nextDT = DateTime.min(nextDT, dt.plus({ hours: 12 }));

			// we're looking for the min and max timestamp for our search, which is
			// halfway the time to either prev or next item
			const itvlFullBefore = Interval.fromDateTimes(DateTime.min(prevDT, dt), DateTime.max(prevDT, dt));
			const itvlFullAfter = Interval.fromDateTimes(DateTime.min(dt, nextDT), DateTime.max(dt, nextDT));
			const minDT = itvlFullBefore.divideEqually(2)[1]?.start || dt;
			const maxDT = itvlFullAfter.divideEqually(2)[0]?.end || dt;

			const results = await app.SearchItems({
				repo: tlz.openRepos[0].instance_id,
				// TODO: we need a way to search for entity IDs involved regardless of how they're involved (entity_id, from_entity_id, to_entity_id...)
				// entity_id: [dataPoint.item.entity.id],
				// to_entity_id: [dataPoint.item.entity.id],
				start_timestamp: minDT.toISO(),
				end_timestamp: maxDT.toISO(),
				no_location: true,
				limit: 1 // TODO: what should this be?
			});
			dataPoint.adjacent = results.items;

			if (!dataPoint.adjacent?.length) {
				return;
			}

			// augment known item(s) with new adjacent items
			addMapItemToCoordinate(dataPoint);

			const moreDotElem = $('.more-dot', dataPoint.markers[0].getElement());
			if (moreDotElem) {
				// marker is a balloon pin with a dot we can simply show
				moreDotElem.classList.remove('d-none');
			} else {
				// marker is just a location dot, so we can't really show a dot over a dot;
				// instead, add a new gray balloon to this point
				makeMarker(tlz.openRepos[0], dataPoint);

				// show the new marker on the map
				tlz.map.tl_addMarker(dataPoint.markers[dataPoint.markers.length-1]);
			}

		}));



	}
}






















function updateMapLighting() {
	const currentHour = new Date().getHours();
	if (currentHour >= 5 && currentHour < 9) {
		tlz.map.setConfigProperty('basemap', 'lightPreset', 'dawn');
	} else if (currentHour >= 9 && currentHour < 18) {
		tlz.map.setConfigProperty('basemap', 'lightPreset', 'day');
	} else if (currentHour >= 18 && currentHour < 21) {
		tlz.map.setConfigProperty('basemap', 'lightPreset', 'dusk');
	} else if (currentHour >= 21 || currentHour < 5) {
		tlz.map.setConfigProperty('basemap', 'lightPreset', 'night');
	}
}



// TODO: this adds buildings, if we want that...
function addBuildings() {
	// Insert the layer beneath any symbol layer.
	const layers = tlz.map.getStyle().layers;
	const labelLayerId = layers.find(
		(layer) => layer.type === 'symbol' && layer.layout['text-field']
	).id;

	// The 'building' layer in the Mapbox Streets
	// vector tileset contains building height data
	// from OpenStreetMap.
	tlz.map.tl_addLayer(
		{
			'id': 'add-3d-buildings',
			'source': 'composite',
			'source-layer': 'building',
			'filter': ['==', 'extrude', 'true'],
			'type': 'fill-extrusion',
			'minzoom': 15,
			'paint': {
				'fill-extrusion-color': '#aaa',

				// Use an 'interpolate' expression to
				// add a smooth transition effect to
				// the buildings as the user zooms in.
				'fill-extrusion-height': [
					'interpolate', ['linear'], ['zoom'],
					15, 0, 15.05,
					['get', 'height']
				],
				'fill-extrusion-base': [
					'interpolate', ['linear'], ['zoom'],
					15, 0, 15.05,
					['get', 'min_height']
				],
				'fill-extrusion-opacity': 0.6
			}
		},
		labelLayerId
	);
}

function applyTerrain(changed3D) {
	if ($('#toggle-3d')?.checked) {
		tlz.map.setTerrain({
			source: 'mapbox-dem',
			exaggeration: 1.0
		});
		if (changed3D) {
			tlz.map.dragRotate.enable();
		}
	} else {
		tlz.map.setTerrain();
		if (changed3D) {
			tlz.map.dragRotate.disable();
			tlz.map.easeTo({
				duration: 1000,
				bearing: 0,
				pitch: 0,
			});
		}
	}
}



on('change', '#toggle-3d', event => {
	applyTerrain(true);
});


// TODO: Would love to make box zooming simpler. See https://github.com/mapbox/mapbox-gl-js/issues/12405

on('click', '#bbox-toggle', event => {
	if ($('#bbox-toggle').classList.contains('active')) {
		// disable panning and box zooming (so as not to conflict with our custom box drawing)
		tlz.map.boxZoom.disable();
		tlz.map.dragPan.disable();

		// show useful cursor indicative of creating a box
		$('.mapboxgl-canvas-container').style.cursor = 'crosshair';

		// when user clicks down on map, start drawing process
		tlz.map.on('mousedown', onMouseDown);

		const canvas = tlz.map.getCanvasContainer();

		// Variable to hold the starting xy point and
		// coordinates when mousedown occured.
		let startPt, startCoord;

		// Variable for the draw box element.
		let box;

		function onMouseDown(e) {
			// TODO: if space is held down (keyCode 32 -- probably needed in keyDown event), don't draw the box: pan instead

			tlz.map.on('mousemove', onMouseMove);
			tlz.map.on('mouseup', onMouseUp);

			// for cancellation
			document.addEventListener('keydown', onKeyDown);

			// Capture the first xy coordinates
			startPt = e.point;
			startCoord = e.lngLat;
		}

		function onMouseMove(e) {
			// Capture the ongoing xy coordinates
			let current = e.point;

			if (!box) {
				box = document.createElement('div');
				box.classList.add('boxdraw');
				canvas.appendChild(box);
			}

			const minX = Math.min(startPt.x, current.x),
				maxX = Math.max(startPt.x, current.x),
				minY = Math.min(startPt.y, current.y),
				maxY = Math.max(startPt.y, current.y);

			// Adjust width and xy position of the box element ongoing
			const pos = `translate(${minX}px, ${minY}px)`;
			box.style.transform = pos;
			box.style.width = maxX - minX + 'px';
			box.style.height = maxY - minY + 'px';
		}

		function onMouseUp(e) {
			finish([startCoord, e.lngLat]);
		}

		function onKeyDown(e) {
			if (e.keyCode === 27) finish();
		}

		function finish(bbox) {
			// remove the box polygon (literally "poly-gone")
			if (box) {
				box.remove();
				box = null;
			}

			if (bbox) {
				const bboxEl = $('#bbox');
				bboxEl.value = `(${bbox[0].lat.toFixed(4)}, ${bbox[0].lng.toFixed(4)}) (${bbox[1].lat.toFixed(4)}, ${bbox[1].lat.toFixed(4)})`
				bboxEl.dataset.lat1 = bbox[0].lat;
				bboxEl.dataset.lon1 = bbox[0].lng;
				bboxEl.dataset.lat2 = bbox[1].lat;
				bboxEl.dataset.lon2 = bbox[1].lng;
				trigger('#bbox', 'change');
			}

			tlz.map.dragPan.enable();
			tlz.map.boxZoom.enable();

			tlz.map.off('mousedown', onMouseDown);
			tlz.map.off('mousemove', onMouseMove);
			tlz.map.off('mouseup', onMouseUp);

			$('.mapboxgl-canvas-container').style.cursor = '';

			$('#bbox-toggle').classList.remove('active');
		}
	}
});

// TODO: This is too "eager" in my experience on macOS
// // when clicking "off" a marker, un-classify it as active and hide item info
// on('click', '#map-page #map', event => {
// 	if (event.target.closest('.mapboxgl-marker')) return;
// 	hideMapPageInfoCard();
// });

on('click', '#map-page #infocard .btn-close', e => {
	hideMapPageInfoCard();
});

function hideMapPageInfoCard() {
	tlz.map.easeTo({
		padding: { left: 0 },
		duration: 500
	});
	$('#infocard').classList.add('d-none');
	$$('.mapboxgl-marker.active').forEach(el => {
		el.classList.remove('active');
	});
}

on('click', '#proximity-toggle', event => {
	$('.mapboxgl-canvas-container').style.cursor =
		$('#proximity-toggle').classList.contains('active') ? 'crosshair' : '';
});

on('change', '#bbox', e => {
	if (e.target.value) {
		$('#bbox-clear').classList.remove('d-none');
	} else {
		$('#bbox-clear').classList.add('d-none');
	}
});

on('keyup, paste', '#proximity', e => {
	if (!e.target.value) {
		delete e.target.dataset.lat;
		delete e.target.dataset.lon;
		e.target.classList.remove('is-invalid');
		return;
	}
	const regex = /(-?\d*(\.\d+)?),\s*(-?\d*(\.\d+)?)/;
	const matches = regex.exec(e.target.value);
	if (matches && matches.length > 2) {
		// parsed (lat,lon) is available in matches[1] and matches[3]
		try {
			if ((Number(matches[1]) <= 90 || Number(matches[1]) >= -90)
				&& (Number(matches[3] <= 180) || Number(matches[3] >= 180))) {
				e.target.classList.remove('is-invalid');
				e.target.dataset.lat = matches[1];
				e.target.dataset.lon = matches[3];
			} else {
				throw "out of bounds";
			}
		} catch {
			e.target.classList.remove('is-invalid');
		}
	} else {
		e.target.classList.add('is-invalid');
		delete e.target.dataset.lat;
		delete e.target.dataset.lon;
	}
});


on('change', 'input[name=map-style]', e => {
	tlz.map.setStyle('mapbox://styles/mapbox/' + e.target.value);
});


on('click', '#bbox-clear', event => {
	const bboxEl = $('#bbox');
	bboxEl.value = '';
	delete bboxEl.dataset.lat1;
	delete bboxEl.dataset.lat2;
	delete bboxEl.dataset.lon1;
	delete bboxEl.dataset.lon2;
	trigger('#bbox', 'change');
});



// noUiSlider.create($('#range-nearby'), {
// 	start: 0,
// 	connect: [true, false],
// 	// step: 10,
// 	range: {
// 		min: 0,
// 		max: 100
// 	}
// });





function mapPageFilterParams(repo) {
	const params = {
		repo: repo.instance_id,
		min_latitude: -90,
		max_latitude: 90,
		min_longitude: -180,
		max_longitude: 180,
		related: 1,
		limit: 500, // TODO: ... paginate?
		flat: true,
		relations: [
			// don't show motion pictures / live photos, since they are not
			// considered their own item in a gallery sense, and perhaps
			// more importantly, we don't want to have to generate a thumbnail
			// for them (literally no need for a thumbnail of those, just
			// wasted CPU time and storage space)
			{
				"not": true,
				"relation_label": "motion"
			}
		]
	};
	commonFilterSearchParams(params);

	// date range
	// if one date is selected, set the range to that day;
	// if two dates are selected, make the range inclusive
	const pickerDates = $('.date-input').datepicker.selectedDates;
	if (pickerDates.length > 0) {
		params.start_timestamp = pickerDates[0];
		if (pickerDates.length == 1) {
			params.end_timestamp = DateTime.fromJSDate(pickerDates[0]).plus({ days: 1 }).toJSDate();
		} else if (pickerDates.length == 2) {
			params.end_timestamp = DateTime.fromJSDate(pickerDates[1]).plus({ days: 1 }).toJSDate();
		}
	}

	// location proximity
	const prox = $('#proximity');
	if (prox.dataset.lat) {
		params.latitude = Number(prox.dataset.lat);
	}
	if (prox.dataset.lon) {
		params.longitude = Number(prox.dataset.lon);
	}
	if (prox.dataset.lat || prox.dataset.lon) {
		// TODO: is this the right behavior? Proximity search should probably be regardless of date picker, right?
		delete params.start_timestamp;
		delete params.end_timestamp;

		params.limit = 10;
	}

	// bounding box
	const bbox = $('#bbox');
	if (bbox.dataset.lat1 && bbox.dataset.lat2 && bbox.dataset.lon1 && bbox.dataset.lon2) {
		params.min_latitude = Math.min(Number(bbox.dataset.lat1), Number(bbox.dataset.lat2));
		params.max_latitude = Math.max(Number(bbox.dataset.lat1), Number(bbox.dataset.lat2));
		params.min_longitude = Math.min(Number(bbox.dataset.lon1), Number(bbox.dataset.lon2));
		params.max_longitude = Math.max(Number(bbox.dataset.lon1), Number(bbox.dataset.lon2));

		// TODO: same with bounding box as with proximity, right? ignore date picker?
		delete params.start_timestamp;
		delete params.end_timestamp;

		params.limit = 10;
	}

	console.log("PARAMS:", params)

	return params;
}






// temporallyConsistent returns true if searchParams is expected to have temporal locality,
// meaning that the search results can be expected to be adjacent to each other in time; i.e.
// if true, then it may make sense to connect the search results by a line, for example,
// because they are likely to be adjacent in time. If search results are confined to a
// particular spatial (spacial?) region, however, we don't know what points along the time
// continuum may be missing, so it wouldn't make sense to connect them as if someone or
// something went directly from place to place.
//
// Parameters are considered temporally consistent if they do not constrain by a spatial
// bounding box or a spatial proximity; i.e. min/max_lat/lon and lat/lon must be empty
// (or the min/max fields can be set to the full range of lat/lon values, which is useful
// when querying for geolocated items regardless of location).
function temporallyConsistent(searchParams) {
	return ((!searchParams.min_latitude && !searchParams.max_latitude
		&& !searchParams.min_longitude && !searchParams.max_longitude)
		|| (searchParams.min_latitude <= -90 && searchParams.max_latitude >= 90
			&& searchParams.min_longitude <= -180 && searchParams.max_longitude >= 180))
		&& !searchParams.latitude && !searchParams.longitude;
}



function canRenderOnMap(item) {
	// explicitly check whether defined, since 0,0 is a valid coordinate
	return item.latitude !== undefined || item.longitude !== undefined;
}




























function loadAndRenderHeatmap(repo) {
	// first, get the count of how many points we should expect
	// (we can speed this up very drastically by sampling the
	// rows and multiplying the count by the sample interval
	// to get close to the true count)
	const intervalForCount = 1000;
	const params = {
		repo: repo.instance_id,
		min_latitude: -90,
		max_latitude: 90,
		min_longitude: -180,
		max_longitude: 180,
		geojson: true,
		limit: 1, // will discard; we just want count first
		sample: intervalForCount,
		with_total: true,
		flat: true
	};

	app.SearchItems(params).then(results => {
		// now that we have the count, update the search parameters
		// until we get more controls over the heatmap implemented, set
		// an approximate hard-limit on the number of items the heatmap
		// is comprised of to keep performance of the map decent
		const desiredItemCount = 100000;
		params.sample = Math.ceil(results.total*intervalForCount / desiredItemCount);
		params.limit = -1;
		delete params.with_total;

		app.SearchItems(params).then(results => {
			mapData.heatmap = JSON.parse(results.geojson);
			renderHeatmap();
		});
	});
}

function renderHeatmap() {
	tlz.map.tl_removeLayer('items-sample');
	tlz.map.tl_removeSource('all-items');
	const addSource = function() { // source can't be added until style loads, apparently: https://stackoverflow.com/q/40557070
		tlz.map.tl_addSource('all-items', {
			type: 'geojson',
			data: mapData.heatmap
		});
	};
	if (tlz.map.isStyleLoaded()) {
		addSource();
	} else {
		tlz.map.on('load', addSource);
	}
	// Docs: https://docs.mapbox.com/mapbox-gl-js/style-spec/layers/#heatmap
	tlz.map.tl_addLayer(
		{
			'id': 'items-sample',
			'type': 'heatmap',
			'source': 'all-items',
			// 'maxzoom': 16,
			'paint': {
				// Increase the heatmap weight based on frequency and property magnitude
				// 'heatmap-weight': [
				// 	'interpolate', ['linear'], ['get', 'mag'],
				// 	0, 0,
				// 	6, 1
				// ],
				// Increase the heatmap color weight weight by zoom level
				// heatmap-intensity is a multiplier on top of heatmap-weight
				'heatmap-intensity': [
					'interpolate', ['linear'], ['zoom'],
					0, 0.1,
					12, 2
				],
				// Color ramp for heatmap.  Domain is 0 (low) to 1 (high).
				// Begin color ramp at 0-stop with a 0-transparancy color
				// to create a blur-like effect.
				// 'heatmap-color': [
				// 	'interpolate', ['linear'], ['heatmap-density'],
				// 	0,   'rgba(33,102,172,0)',
				// 	0.2, 'rgb(103,169,207)',
				// 	0.4, 'rgb(209,229,240)',
				// 	0.6, 'rgb(253,219,199)',
				// 	0.8, 'rgb(239,138,98)',
				// 	1,   'rgb(178,24,43)'
				// ],
				// Adjust the heatmap radius by zoom level
				// TODO: the radii should be increased/decreased  when the sampling interval is increased/decreased, respectively
				'heatmap-radius': [
					'interpolate', ['linear'], ['zoom'],
					0, 8,
					6, 14
				],
				'heatmap-opacity': [
					'interpolate', ['linear'], ['zoom'],
					7,  .5,
					14, .25,
					16, .25,
					18, 0
				]
			}
		}, 'route'
	);
}


// Returns the distance between two coordinates in kilometers.
// from https://stackoverflow.com/a/48805273/1048862
// NOTE: We use [lon, lat] order because that's what Mapbox uses, so the rest of our code uses this order too.
function haversineDistance([lon1, lat1], [lon2, lat2], miles = false) {
	const toRadian = angle => (Math.PI / 180) * angle;
	const distance = (a, b) => (Math.PI / 180) * (a - b);
	const RADIUS_OF_EARTH_IN_KM = 6371;

	const dLat = distance(lat2, lat1);
	const dLon = distance(lon2, lon1);

	lat1 = toRadian(lat1);
	lat2 = toRadian(lat2);

	// Haversine Formula
	const a =
		Math.pow(Math.sin(dLat / 2), 2) +
		Math.pow(Math.sin(dLon / 2), 2) * Math.cos(lat1) * Math.cos(lat2);
	const c = 2 * Math.asin(Math.min(1, Math.sqrt(a))); // min() protects against roundoff errors the two points are nearly antipodal

	let finalDistance = RADIUS_OF_EARTH_IN_KM * c;

	if (miles) {
		finalDistance /= 1.60934;
	}

	return finalDistance;
}