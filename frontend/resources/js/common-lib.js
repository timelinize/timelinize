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

// Commmon JS library code for the whole application

// Make luxon library class more easily accessible
const DateTime = luxon.DateTime;
const Interval = luxon.Interval;
const Duration = luxon.Duration;


// // Tabler 1.2.0 moved/hid the bootstrap variable and the prior tabler variable into a new global... see https://github.com/tabler/tabler/issues/2273#issuecomment-2816833153
// // It broke quite a few things, so I'm holding out on Tabler 1.2 until some things are sorted out.
// var bootstrap = tabler.bootstrap;
// var tabler = tabler.tabler;



// TODO: application vars can go in here instead of the global scope
const tlz = {
	openRepos: load('open_repos') || [],

	itemClassIconPaths: {
		email: `
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M3 7a2 2 0 0 1 2 -2h14a2 2 0 0 1 2 2v10a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2 -2v-10z"></path>
			<path d="M3 7l9 6l9 -6"></path>`,
		message: `
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M11 3h10v8h-3l-4 2v-2h-3z"></path>
			<path d="M15 16v4a1 1 0 0 1 -1 1h-8a1 1 0 0 1 -1 -1v-14a1 1 0 0 1 1 -1h2"></path>
			<path d="M10 18v.01"></path>`,
		social: `
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<circle cx="12" cy="5" r="2"></circle>
			<circle cx="5" cy="19" r="2"></circle>
			<circle cx="19" cy="19" r="2"></circle>
			<circle cx="12" cy="14" r="3"></circle>
			<line x1="12" y1="7" x2="12" y2="11"></line>
			<line x1="6.7" y1="17.8" x2="9.5" y2="15.8"></line>
			<line x1="17.3" y1="17.8" x2="14.5" y2="15.8"></line>`,
		location: `
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<circle cx="12" cy="11" r="3"></circle>
			<path d="M17.657 16.657l-4.243 4.243a2 2 0 0 1 -2.827 0l-4.244 -4.243a8 8 0 1 1 11.314 0z"></path>`,
		collection: `
			<path stroke="none" d="M0 0h24v24H0z" fill="none" />
			<path d="M19 4v16h-12a2 2 0 0 1 -2 -2v-12a2 2 0 0 1 2 -2h12z" />
			<path d="M19 16h-12a2 2 0 0 0 -2 2" />
			<path d="M9 8h6" />`,
		"": `
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M14 3v4a1 1 0 0 0 1 1h4"></path>
			<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z"></path>
			<path d="M12 17v.01"></path>
			<path d="M12 14a1.5 1.5 0 1 0 -1.14 -2.474"></path>`,

		// because the "media" classification is so broad, we can show a more specific
		// icon based on the data_type column (mime type) of the item
		media_image: `<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<line x1="15" y1="8" x2="15.01" y2="8"></line>
				<rect x="4" y="4" width="16" height="16" rx="3"></rect>
				<path d="M4 15l4 -4a3 5 0 0 1 3 0l5 5"></path>
				<path d="M14 14l1 -1a3 5 0 0 1 3 0l2 2"></path>`,
		media_video: `<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<path d="M15 10l4.553 -2.276a1 1 0 0 1 1.447 .894v6.764a1 1 0 0 1 -1.447 .894l-4.553 -2.276v-4z"></path>
				<rect x="3" y="6" width="12" height="12" rx="2"></rect>`,
		media_audio: `<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<circle cx="6" cy="17" r="3"></circle>
				<circle cx="16" cy="17" r="3"></circle>
				<polyline points="9 17 9 4 19 4 19 17"></polyline>
				<line x1="9" y1="8" x2="19" y2="8"></line>`,

		// generic fallback if we don't recognize the specific type of media... a camera is the best I can think of
		// since cameras can do pictures, videos, and audio
		media: `<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M5 7h1a2 2 0 0 0 2 -2a1 1 0 0 1 1 -1h6a1 1 0 0 1 1 1a2 2 0 0 0 2 2h1a2 2 0 0 1 2 2v9a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2"></path>
			<path d="M12 13m-3 0a3 3 0 1 0 6 0a3 3 0 1 0 -6 0"></path>`,

		bookmark: `<path stroke="none" d="M0 0h24v24H0z" fill="none"/>
		<path d="M14 2a5 5 0 0 1 5 5v14a1 1 0 0 1 -1.555 .832l-5.445 -3.63l-5.444 3.63a1 1 0 0 1 -1.55 -.72l-.006 -.112v-14a5 5 0 0 1 5 -5h4z" />
		`,

		page_view: `<path stroke="none" d="M0 0h24v24H0z" fill="none"/>
		<path d="M12 8l0 4l2 2" /><path d="M3.05 11a9 9 0 1 1 .5 4m-.5 5v-5h5" />
		`
	},

	itemClassIconAndLabel(item, pathOnly) {
		const info = classInfo(item.classification);
		const iconMap = pathOnly ? tlz.itemClassIconPaths : tlz.itemClassIcons;
		if (item.classification == "media") {
			if (item?.data_type?.startsWith("image/")) {
				return {
					icon: iconMap.media_image,
					label: info.labels[0] + " (image)"
				};
			} else if (item?.data_type?.startsWith("video/")) {
				return {
					icon: iconMap.media_video,
					label: info.labels[0] + " (video)"
				};
			} else if (item?.data_type?.startsWith("audio/")) {
				return {
					icon: iconMap.media_audio,
					label: info.labels[0] + " (audio)"
				};
			}
		}

		return {
			icon: iconMap[item.classification],
			label: info.labels[0]
		};
	},

	attributeLabels: {
		"_entity": "Entity ID",
		"email_address": "Email address",
		"phone_number": "Phone number",
		"facebook_username": "Facebook",
		"facebook_name": "Name on Facebook",
		"google_photos_name": "Name on Google Photos",
		"instagram_username": "Instagram",
		"instagram_name": "Name on Instagram",
		"instagram_bio": "Instagram bio",
		"strava_athlete_id": "Strava",
		"telegram_id": "Telegram",
		"twitter_username": "Twitter",
		"google_location_device": "Google Location Device",
		"url": "Website",
		"twitter_id": "Twitter ID",
		"twitter_location": "Location in Twitter bio",
	},

	colorClasses: [
		"blue",
		"azure",
		"indigo",
		"purple",
		"pink",
		"red",
		"orange",
		"yellow",
		"lime",
		"green",
		"teal",
		"cyan",
		"blue-lt",
		"azure-lt",
		"indigo-lt",
		"purple-lt",
		"pink-lt",
		"red-lt",
		"orange-lt",
		"yellow-lt",
		"lime-lt",
		"green-lt",
		"teal-lt",
		"cyan-lt"
	],

	// map of filepicker names to last settings/state (like path)
	filePickers: {},

	// counter for IDs of collapsable regions which may be dynamically created
	collapseCounter: 0,

	// keeps statistics for active jobs, keyed by job ID
	jobStats: {},

	// this will hold the connection to the server's real-time logger WebSocket and related state
	loggerSocket: {},

	// These intervals are cleared when the page freezes, and restarted when the page unfreezes.
	// The values in this object are objects with this structure:
	//   { set(), interval }
	// where set() returns the result of setInterval(), and interval is the
	// returned interval that can be cleared.
	intervals: {
		// Update the dynamic timestamps (and durations) every second to keep them accurate
		dynamicTime: {
			set() {
				return setInterval(function() {
					for (const elem of $$('.dynamic-time')) {
						elem.innerText = elem._timestamp.toRelative();
					}
					for (const elem of $$('.dynamic-duration')) {
						// don't use diffNow() because it's implemented backwards (durations are always negative)!
						elem.innerText = betterToHuman(DateTime.now().diff(elem._timestamp));
					}
				}, 1000);
			}
		}
	},

	// these values are used for moving the map to containers
	// closest to the cursor; the set makes the distance search
	// more efficient by limiting it to those in the viewport
	mapsInViewport: new Set(),
	nearestMapElem: null
};

// Icons associated with each class of item.
tlz.itemClassIcons = {
	email: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-mail" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.email}
		</svg>`,
	message: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-device-mobile-message" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.message}
		</svg>`,
	social: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-social" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.social}
		</svg>`,
	location: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-map-pin" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.location}
		</svg>`,
	collection: `
		<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="icon icon-tabler icons-tabler-outline icon-tabler-book-2">
			${tlz.itemClassIconPaths.collection}
		</svg>`,
	"": `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-file-unknown" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths[""]}
		</svg>`,

	// because the "media" classification is so broad, we can show a more specific
	// icon based on the data_type column (mime type) of the item
	media_image: `<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-photo" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.media_image}
		</svg>`,
	media_video: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-video" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			${tlz.itemClassIconPaths.media_video}
		</svg>`,
	media_audio: `
	  <svg  xmlns="http://www.w3.org/2000/svg"  width="24"  height="24"  viewBox="0 0 24 24"  fill="currentColor"  class="icon icon-tabler icons-tabler-filled icon-tabler-bookmark">
			${tlz.itemClassIconPaths.media_audio}
		</svg>`,

	// generic fallback if we don't recognize the specific type of media... a camera is the best I can think of
	// since cameras can do pictures, videos, and audio
	media: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-camera" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
		${tlz.itemClassIconPaths.media}
	</svg>`,

	bookmark: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-camera" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
		${tlz.itemClassIconPaths.bookmark}
	</svg>`,

	page_view: `
	  <svg  xmlns="http://www.w3.org/2000/svg"  width="24"  height="24"  viewBox="0 0 24 24"  fill="none"  stroke="currentColor"  stroke-width="2"  stroke-linecap="round"  stroke-linejoin="round"  class="icon icon-tabler icons-tabler-outline icon-tabler-history">
		${tlz.itemClassIconPaths.page_view}
	</svg>`
};

// set all the predefined intervals
for (const key in tlz.intervals) {
	tlz.intervals[key].interval = tlz.intervals[key].set();
}

get('/api/build-info').then(bi => {
	tlz.buildInfo = bi;
});


















// sets up map singleton; should be done after settings but before first page load
function initMapSingleton() {
	const defaultMapboxToken = 'pk.eyJ1IjoiZHlhbmltIiwiYSI6ImNsYXNqcDVrYjF2OGwzcG1xaDB5YmlhZmQifQ.Y6QIKhjU0NeccKS6Rs8YqA';
	mapboxgl.accessToken = tlz.settings?.application?.mapbox_api_key || defaultMapboxToken;

	tlz.map = new mapboxgl.Map({
		container: document.createElement('div'),
		style: `mapbox://styles/mapbox/standard?optimized=true`,
		antialias: true
	});
	tlz.map._container.id = 'map'; // the container element we specified above in the Map constructor is stored at tlz.map._container
	tlz.map.tl_navControl = new mapboxgl.NavigationControl();
	tlz.map.tl_containers = new Map(); // JS map, not geo map
	tlz.map.tl_data = {
		markers: [], // stores markers that are currently on the map
		layers: {},  // layers currently on the map, keyed by ID
		sources: {}  // sources currently on the map, keyed by ID
	};
	tlz.map.tl_addNavControl = function() {
		if (!tlz.map.hasControl(tlz.map.tl_navControl)) {
			tlz.map.addControl(tlz.map.tl_navControl);
		}
	};
	tlz.map.tl_addMarker = function(marker) {
		tlz.map.tl_data.markers.push(marker);
		marker.addTo(tlz.map);
	};
	tlz.map.tl_removeMarker = function(marker) {
		const idx = tlz.map.tl_data.markers.indexOf(marker);
		if (idx > -1) {
			marker.remove();
			tlz.map.tl_data.markers.splice(idx, 1);
		}
	};
	tlz.map.tl_addLayer = function(layer, underLayerID) {
		tlz.map.tl_data.layers[layer.id] = layer;
		if (underLayerID && !tlz.map.getLayer(underLayerID)) {
			underLayerID = null;
		}
		tlz.map.addLayer(layer, underLayerID);
	};
	tlz.map.tl_removeLayer = function(layerID) {
		delete tlz.map.tl_data.layers[layerID];
		if (tlz.map.getLayer(layerID)) {
			tlz.map.removeLayer(layerID);
		}
	};
	tlz.map.tl_addSource = function(id, source) {
		tlz.map.tl_data.sources[id] = source;
		tlz.map.addSource(id, source);
	};
	tlz.map.tl_removeSource = function(sourceID) {
		delete tlz.map.tl_data.sources[sourceID];
		if (tlz.map.getSource(sourceID)) {
			tlz.map.removeSource(sourceID);
		}
	};
	tlz.map.tl_clearMarkers = function() {
		tlz.map.tl_data.markers.forEach(marker => marker.remove());
		tlz.map.tl_data.markers = [];
	}
	tlz.map.tl_clear = function() {
		tlz.map.tl_clearMarkers();
		for (const layerID in tlz.map.tl_data.layers) {
			tlz.map.tl_removeLayer(layerID);
		}
		for (const sourceID in tlz.map.tl_data.sources) {
			tlz.map.tl_removeLayer(sourceID);
		}
		tlz.map.setPadding({left: 0, top: 0});
	};

	// tlz.map.loaded() doesn't always return true after 'load' event fires, not clear why; TODO: maybe create issue to ask?
	tlz.map.tl_isLoaded = false;
	tlz.map.on('load', () => tlz.map.tl_isLoaded = true);

	tlz.map.once('style.load', () => {
		// update the lighting every minute to match the current time of day
		setInterval(updateMapLighting, 60000);
	});

	tlz.map.on('style.load', () => {
		updateMapLighting();

		// add terrain source, but don't set it on the map unless enabled
		if (!tlz.map.getSource('mapbox-dem')) {
			tlz.map.tl_addSource('mapbox-dem', {
				type: 'raster-dem',
				// TODO: what's the difference between these?
				url: 'mapbox://mapbox.terrain-rgb'
				// url: "mapbox://mapbox.mapbox-terrain-dem-v1"
			});
		}

		applyTerrain();

		// changing the style obliterates layers
		tlz.openRepos.forEach(repo => {
			if (mapData.heatmap) {
				renderHeatmap();
			}
			if (mapData.results) {
				renderMapData();
			}
		});
	});


	tlz.map.on('click', e => {
		if ($('#proximity-toggle')?.classList?.contains('active')) {
			$('#proximity').value = `${e.lngLat.lat.toFixed(5)}, ${e.lngLat.lng.toFixed(5)}`;
			$('#proximity').dataset.lat = e.lngLat.lat;
			$('#proximity').dataset.lon = e.lngLat.lng;
			$('#proximity').dispatchEvent(new Event('change', { bubbles: true }));
			$('#proximity-toggle').classList.remove('active');
			$('.mapboxgl-canvas-container').style.cursor = '';
		}
	});


	// Can be useful when troubleshooting zoom-related things
	// tlz.map.on('zoom', function() {
	// 	console.debug('MAP ZOOM:', tlz.map.getZoom());
	// });
}
































// Computes the Luxon Duration difference between ts1 and ts2 (ISO dates)
// so that the difference is always positive.
function durationBetween(ts1, ts2) {
	const dt1 = DateTime.fromISO(ts1);
	const dt2 = DateTime.fromISO(ts2);
	return DateTime.max(dt1, dt2).diff(DateTime.min(dt1, dt2));
}

// Converts a JS Date object to Unix timestamp in seconds.
function dateToUnixSec(d) {
	return Math.floor(d.getTime() / 1000);
}

// Converts Unix timestamp in seconds to a JS Date.
function unixSecToDate(ts) {
	return new Date(ts * 1000);
}



// Renders a dropdown for a filter input inside containerEl, with the given
// title, loading data from storage using loadKey.
function renderFilterDropdown(containerEl, title, loadKey) {
	const staticMenu = containerEl.classList.contains('static-menu')
	const tplSel = staticMenu ? '#tpl-filter-dropdown-static' : '#tpl-filter-dropdown';
	const tpl = cloneTemplate(tplSel);

	const titleElem = staticMenu ? $('h6', tpl) : $('button', tpl);
	titleElem.innerText = title;

	const menu = staticMenu ? tpl : $('.dropdown-menu', tpl);

	// append select-all/none toggles
	menu.append(cloneTemplate('#tpl-dropdown-toggles'));

	// append the data sources
	const data = load(loadKey);
	if (title == "Types") {
		data[""] = {title: "Unknown"};
	}
	for (const key in data) {
		const elem = cloneTemplate('#tpl-dropdown-checkbox');
		$('input', elem).value = key;
		elem.append(document.createTextNode(data[key].title || data[key].labels[0]));
		menu.append(elem);
	}

	containerEl.replaceChildren(tpl);
}


async function newDataSourceSelect(selectEl, options) {
	if ($(selectEl).tomselect) {
		return $(selectEl).tomselect;
	}

	var dsList = await app.DataSources();

	for (const ds of dsList) {
		const optEl = document.createElement('option');
		optEl.value = ds.name;
		optEl.innerText = ds.title;
		optEl.dataset.customProperties = `<img src="/ds-image/${ds.name}">`;
		$(selectEl).append(optEl);
	}

	function renderTomSelectItemAndOption(data, escape) {
		if (data.customProperties) {
			return `<div><span class="dropdown-item-indicator">${data.customProperties}</span>${escape(data.text)}</div>`;
		}
		return `<div>${escape(data.text)}</div>`;
	}

	const ts = new TomSelect($(selectEl), {
		maxItems: options?.maxItems,
		render: {
			item: renderTomSelectItemAndOption,
			option: renderTomSelectItemAndOption
		}
	});

	// for a single-select control, it usually makes sense to
	// initialize empty rather than the first option
	if (options?.maxItems == 1) {
		ts.clear(true);
	}

	// Clear input after selecting matching option from list
	// (I have no idea why this isn't the default behavior)
	ts.on('item_add', () => ts.control_input.value = '' );

	return ts;
}






function setDateInputPlaceholder(containerEl) {
	const dateInput = $('.date-input', containerEl);
	if (dateInput.datepicker.opts.range) {
		dateInput.placeholder = "Date range";
	} else {
		dateInput.placeholder = "Date";
	}
	const sortInput = $('.date-sort', containerEl);
	if (sortInput?.value == "NEAR") {
		dateInput.placeholder = "Target date";
	}
}

function newDatePicker(opts) {
	const tpl = cloneTemplate('#tpl-datepicker');

	if (!opts.vertical) {
		$('label', tpl).remove();
		tpl.classList.remove('mb-3');
		$$('.mb-3', tpl).forEach(el => el.classList.remove('mb-3'));
	}

	if (!opts.proximity) {
		$('option[value="NEAR"]', tpl).remove();
	}

	if (opts.sort === false) {
		$('.sort-container', tpl).closest('.input-group').classList.remove('input-group', 'flex-nowrap');
		$('.sort-container', tpl).remove();
	} else {
		if (!opts.defaultSort) {
			opts.defaultSort = opts.range ? "DESC" : "ASC";
		}
		if (opts.defaultSort) {
			$('.date-sort', tpl).value = opts.defaultSort;
		}
	}


	// used for firing change events
	const dateInputElem = $('.date-input', tpl)
	let lastVal = dateInputElem.value;

	// set up the AirDatepicker options
	const dpOpts = {
		...opts.passthru,
		container: opts?.passthru?.container || tpl, // this is necessary if the <body> tag is fixed (doesn't scroll), so we choose a container that will scroll with the input element
		locale: {
			days: ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'],
			daysShort: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
			daysMin: ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'],
			months: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
			monthsShort: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
			today: 'Today',
			clear: 'Clear',
			dateFormat: 'MM/dd/yyyy',
			timeFormat: 'hh:mm aa',
			firstDay: 0
		},
		buttons: opts?.passthru?.buttons || [],
		multipleDatesSeparator: opts?.passthru?.multipleDatesSeparator || ' - ',
		navTitles: opts?.passthru?.navTitles || {
			days: '<span><strong>MMMM</strong> <span class="text-secondary">yyyy</span></span>',
			months: '<strong>yyyy</strong>'
		},
		onShow(isFinished) {
			if (isFinished) return false;
			lastVal = dateInputElem.value;
		},
		onHide(isFinished) {
			// gets called twice apparently (!?) once when it has started
			// being hidden and once when it has finished being hidden
			if (isFinished) return;

			if (dateInputElem.value != lastVal) {
				lastVal = dateInputElem.value;
				trigger(dateInputElem, 'change');
			}
		}
	};

	if (opts.time) {
		dpOpts.timepicker = true;
	}
	if (opts.timeToggle) {
		dpOpts.buttons.push({
			className: 'datepicker-time-toggle',
			content(dp) {
				return dp.opts.timepicker ? 'Disable time' : 'Enable time'
			},
			onClick(dp) {
				let viewDate = dp.viewDate;
				let today = new Date();

				// Since timepicker takes initial time from 'viewDate', set up time here,
				// otherwise time will be equal to 00:00 if user navigated through datepicker
				viewDate.setHours(today.getHours());
				viewDate.setMinutes(today.getMinutes());

				dp.update({
					timepicker: !dp.opts.timepicker,
					viewDate
				});
			}
		});
	}

	if (opts.rangeToggle) {
		dpOpts.buttons.push({
			content(dp) {
				return dp.opts.range ? 'Disable range' : 'Enable range';
			},
			onClick(dp) {
				// toggle range support in date picker, and deselect second date
				// if range is now disabled but had both start and end date selected
				dp.update({ range: !dp.opts.range });
				if (!dp.opts.range && dp.selectedDates.length > 1) {
					dp.unselectDate(dp.selectedDates[1]);
				}

				// update sort input, as we can't do NEAR for ranges
				const sortEl = $('.date-sort', tpl);
				if (sortEl) {
					if (sortEl.value == "NEAR") {
						sortEl.value = "DESC";
					}
					if (!dp.opts.range) {
						sortEl.value = "ASC";
					}

					if ($('[value="NEAR"]', sortEl)) {
						$('[value="NEAR"]', sortEl).disabled = !!dp.opts.range;
					}

					trigger(sortEl, 'change');
				}
			}
		});
	}

	// prefer the "Clear" and "Apply" buttons to go at the end
	dpOpts.buttons.push(
		'clear',
	);

	if (!opts.noApply) {
		dpOpts.buttons.push({
			content() {
				return '<b>Apply</b>';
			},
			onClick(dp) {
				dp.hide();
			}
		});
	}

	$('.date-input', tpl).datepicker = new AirDatepicker($('.date-input', tpl), dpOpts);

	setDateInputPlaceholder(tpl);

	return tpl;
}


















///////////////////
// FILE PICKER
///////////////////

async function newFilePicker(name, options) {
	const filePicker = cloneTemplate('#tpl-file-picker');
	filePicker.options = options; // keeps track of its configuration
	filePicker.filepaths = {}; // keeps track of which files are currently selected
	filePicker.selected = function() { return Object.keys(filePicker.filepaths); };
	filePicker.lastPathInput = ""; // the value of the path textbox, used to debounce the file listing updates

	// restore the hidden files preference, if set
	$('.file-picker-hidden-files', filePicker).checked = tlz.filePickers?.[name]?.show_hidden;

	// we don't calculate sizes of directories, so hide size column if we only show dirs
	if (filePicker.options?.only_dirs) {
		$('.file-picker-col-size', filePicker).remove();
	}

	$('.file-picker-mount-points', filePicker).innerHTML = '<span class="dropdown-header">Mount points</span>';

	// populate file picker roots dropdown
	const roots = await app.FileSelectorRoots();
	for (const root of roots) {
		const a = document.createElement('a');
		a.classList.add('dropdown-item', 'd-flex');

		// insert an appropriate icon based on the root type
		const icons = {
			"home": `
				<svg xmlns="http://www.w3.org/2000/svg" class="icon dropdown-item-icon icon-tabler icon-tabler-home me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
					<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
					<path d="M5 12l-2 0l9 -9l9 9l-2 0"></path>
					<path d="M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2 -2v-7"></path>
					<path d="M9 21v-6a2 2 0 0 1 2 -2h2a2 2 0 0 1 2 2v6"></path>
				</svg>`,
			"root": `
				<svg xmlns="http://www.w3.org/2000/svg" class="icon dropdown-item-icon icon-tabler icon-tabler-devices-pc me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
					<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
					<path d="M3 5h6v14h-6z"></path>
					<path d="M12 9h10v7h-10z"></path>
					<path d="M14 19h6"></path>
					<path d="M17 16v3"></path>
					<path d="M6 13v.01"></path>
					<path d="M6 16v.01"></path>
				</svg>`,
			"mount": `
				<svg xmlns="http://www.w3.org/2000/svg" class="icon dropdown-item-icon icon-tabler icon-tabler-folders me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
					<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
					<path d="M9 4h3l2 2h5a2 2 0 0 1 2 2v7a2 2 0 0 1 -2 2h-10a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2"></path>
					<path d="M17 17v2a2 2 0 0 1 -2 2h-10a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2h2"></path>
				</svg>`,
			"logical": `
				<svg xmlns="http://www.w3.org/2000/svg" class="icon dropdown-item-icon icon-tabler icon-tabler-folders me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
					<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
					<path d="M9 4h3l2 2h5a2 2 0 0 1 2 2v7a2 2 0 0 1 -2 2h-10a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2"></path>
					<path d="M17 17v2a2 2 0 0 1 -2 2h-10a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2h2"></path>
				</svg>`,
		};
		a.innerHTML = icons[root.type] || `<svg xmlns="http://www.w3.org/2000/svg" class="icon dropdown-item-icon icon-tabler icon-tabler-folder-question me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
				<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<path d="M15 19h-10a2 2 0 0 1 -2 -2v-11a2 2 0 0 1 2 -2h4l3 3h7a2 2 0 0 1 2 2v2.5"></path>
				<path d="M19 22v.01"></path>
				<path d="M19 19a2.003 2.003 0 0 0 .914 -3.782a1.98 1.98 0 0 0 -2.414 .483"></path>
			</svg>`;

		const b = document.createElement('b');
		const secondary = document.createElement('div');
		b.classList.add('me-2');
		b.innerText = root.label;
		secondary.classList.add('text-secondary', 'small');
		secondary.innerText = root.path;
		a.append(b);
		a.append(secondary);
		a.dataset.filepath = root.path;

		$('.file-picker-mount-points', filePicker).append(a);
	}

	filePicker.navigate = async function (dir = tlz.filePickers?.[name]?.dir, options) {
		// merge navigate options with those specified for the file picker
		options = {...filePicker.options, ...options}

		// as a special case, show_hidden is an option that is preserved
		// across instances of this file picker, so if it's not explicitly
		// set, use the preserved value
		if (!("show_hidden" in options) && tlz.filePickers?.[name]?.show_hidden) {
			options.show_hidden = true;
		}

		// don't get new file listing if it's the same dir and refresh isn't forced
		if (filePicker.dir && dir == filePicker.dir && !options?.refresh) {
			return;
		}

		const listing = await app.FileListing(dir, options);

		// only navigate if the location is different or refresh is forced
		if (filePicker.dir && listing.dir == filePicker.dir && !options?.refresh) {
			return;
		}

		filePicker.dir = listing.dir;
		tlz.filePickers[name] = {
			dir: listing.dir,
			show_hidden: options?.show_hidden
		}

		// let listeners know we are navigating
		const event = new CustomEvent("navigate", {
			bubbles: true,
			detail: {
				dir: listing.dir,
				options: options,
				selectedItem: $('.file-picker-table .file-picker-item.selected', filePicker)
			}
		});
		filePicker.dispatchEvent(event);

		// reset the filepath box, listing table, and selected path(s),
		// then emit event (intentionally named uniquely from standard events)
		if (!options?.autocomplete) {
			$('.file-picker-path').value = listing.dir;
		}
		$('.file-picker-table tbody', filePicker).innerHTML = '';
		filePicker.filepaths = {};
		filePicker.dispatchEvent(new CustomEvent("selection", { bubbles: true }));

		// show "Up" at the top if that's doable
		if (listing.up) {
			$('.file-picker-up', filePicker).style.display = '';
			$('.file-picker-up', filePicker).dataset.filepath = listing.up; // HTML-safe
		} else {
			$('.file-picker-up', filePicker).style.display = 'none';
		}

		// render each file to the listing
		let selectedItem;
		for (const item of listing.files) {
			const modDate = DateTime.fromISO(item.mod_time);

			const row = cloneTemplate('#tpl-file-picker-item');

			if (options?.only_dirs) {
				$('.file-picker-col-size', row).remove();
			} else if (!item.is_dir) {
				$('.sort-size', row).innerText = humanizeBytes(item.size);
			}
			$('.sort-name', row).innerHTML = item.is_dir ?
					`<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-folder-filled me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
						<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
						<path d="M9 3a1 1 0 0 1 .608 .206l.1 .087l2.706 2.707h6.586a3 3 0 0 1 2.995 2.824l.005 .176v8a3 3 0 0 1 -2.824 2.995l-.176 .005h-14a3 3 0 0 1 -2.995 -2.824l-.005 -.176v-11a3 3 0 0 1 2.824 -2.995l.176 -.005h4z" stroke-width="0" fill="currentColor"></path>
					</svg>` :
					`<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-file me-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
						<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
						<path d="M14 3v4a1 1 0 0 0 1 1h4"></path>
						<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z"></path>
					</svg>`;
			$('.sort-name', row).append(document.createTextNode(item.name));
			$('.sort-modified', row).innerText = modDate.toRelative(); //modDate.toLocaleString(DateTime.DATETIME_SHORT_WITH_SECONDS);
			row.dataset.filepath = item.full_name;
			row.classList.add(item.is_dir ? "file-picker-item-dir" : "file-picker-item-file");

			// add data attributes for reliable sorting that doesn't depend on displayed contents
			$('.sort-name', row).dataset.name = item.name || "";
			if ($('.sort-size', row)) {
				$('.sort-size', row).dataset.size = item.is_dir ? "" : (item.size || "");
			}
			$('.sort-modified', row).dataset.modified = modDate.toUnixInteger() || "";

			// add row to list
			$('.file-picker-table tbody', filePicker).append(row);

			// keep track of selected item, if this is it
			if (item.name == listing.selected) {
				selectedItem = row;
			}
		}

		// scroll to selected item, if any; otherwise reset scroll to top
		if (selectedItem) {
			selectedItem.click();
			selectedItem.scrollIntoView(true);
		} else {
			$('.file-picker-table', filePicker).scrollTop = 0;
		}

		// enable sorting: first reset previous sort, then initialize or re-index list
		$$('.file-picker-table .table-sort', filePicker).forEach(elem => {
			elem.classList.remove('desc', 'asc');
		});
		if (!filePicker.listjs) {
			// this binds event handlers; only do once per file picker
			filePicker.listjs = new List($('.file-picker-table', filePicker), {
				listClass: 'table-tbody',
				sortClass: 'table-sort',
				valueNames: [
					{ name: 'sort-name',     attr: 'data-name' },
					{ name: 'sort-size',     attr: 'data-size' },
					{ name: 'sort-modified', attr: 'data-modified' }
				]
			});
		} else {
			filePicker.listjs.reIndex();
		}
	};

	filePicker.navigate();

	return filePicker;
}

// when an item in the file picker is clicked
on('click', '.file-picker-table .file-picker-item', event => {
	// ignore after first click
	if (event.detail > 1) {
		return;
	}

	// TODO: multi-select (if enabled) - use event.shiftKey or event.ctrlKey to detect

	const fp = event.target.closest('.file-picker');

	// select the clicked one, if not already selected
	const item = event.target.closest('.file-picker-item');
	if (item.classList.contains('selected')) {
		return;
	}

	// deselect any previous items
	$$('.selected', fp).forEach(el => el.classList.remove('selected'));
	fp.filepaths = {};

	item.classList.toggle('selected');
	fp.filepaths[item.dataset.filepath] = true;

	// event intentionally named differently from standard events like 'change' and 'select' to disambiguate
	fp.dispatchEvent(new CustomEvent("selection", { bubbles: true }));
});

// as the user types or pastes a path, navigate to what they've typed, and filter results too
on('keyup change paste', '.file-picker-path', async event => {
	const fp = event.target.closest('.file-picker');
	const pathInput = event.target.value;
	if (pathInput == fp.lastPathInput) {
		return;
	}
	fp.lastPathInput =  event.target.value;
	console.log("EVENT:", event, pathInput);
	fp.navigate(event.target.value, { autocomplete: true })
});

// navigate when double-clicking a folder
on('dblclick', '.file-picker-table .file-picker-item-dir', event => {
	const item = event.target.closest('.file-picker-item');
	// TODO: if dir, nav; if file, select...look for "file-picker-item-dir|file" classes
	const dir = item.dataset.filepath;
	event.target.closest('.file-picker').navigate(dir);
});

// go up a folder
on('click', '.file-picker-table .file-picker-up', event => {
	const dir = event.target.closest('.file-picker-up').dataset.filepath;
	event.target.closest('.file-picker').navigate(dir);
});

// choosing a mount
on('click', '.file-picker-mount-points .dropdown-item', event => {
	event.target.closest('.file-picker').navigate(event.target.closest('.dropdown-item').dataset.filepath);
});

// toggle hidden files/folders
on('change', '.file-picker-hidden-files', event => {
	const fp = event.target.closest('.file-picker');
	if (!fp.options) {
		fp.options = {};
	}
	fp.options.refresh = true; // dir isn't changing, but we need to update the listing
	fp.options.show_hidden = event.target.checked;
	event.target.closest('.file-picker').navigate();
});

























function entityPicture(entity) {
	if (entity.picture.startsWith("http://") || entity.picture.startsWith("https://")) {
		return entity.picture;
	}
	let entityPicture = `/repo/${tlz.openRepos[0].instance_id}/${entity.picture}`;
	if (entity.forceUpdate) {
		entityPicture += `?nocache=${new Date().getTime()}`; // classic cachebuster trick
	}
	return entityPicture;
}

// TODO: consider changing second param to preview=false, so that by default
// we show the thumbnail (it's more common), and you turn on showing the more
// expensive preview image
// TODO: rename this to something else -- also used for thumbnail videos...
function itemImgSrc(item, thumbnail = false) {
	if (!item.data_file) {
		return "";
	}
	if (item.data_file.startsWith("http://") || item.data_file.startsWith("https://")) {
		return item.data_file;
	}
	if (item.data_type == "image/gif") {
		thumbnail = false; // just show the actual gif
	}
	const params = new URLSearchParams({
		data_id: item.data_id || "",
		data_file: item.data_file,
		data_type: item.data_type
	});
	const ext = thumbnail ? "" : ".avif"; // thumbnail extension is dictated by server
	return `/repo/${item.repo_id}/${thumbnail ? "thumbnail" : "image"}/${item.id}${ext}?${params.toString()}`;
}

function entityDisplayNameAndAttr(entity) {
	const unknown = "(unknown)";
	const attrValue = entity?.attribute?.alt_value || entity?.attribute?.value;
	const result = {
		name: entity?.name || attrValue || unknown,
		attribute: entity?.attribute?.name == "_entity" ? "" : attrValue
	};
	if (result.attribute == result.name) {
		// this happens sometimes if a data source ONLY provides a name,
		// then generally the attribute will have the same value as name
		result.attribute = "";
	}
	return result;
}

function avatarColorClasses(i) {
	const colorClass = tlz.colorClasses[i % tlz.colorClasses.length];
	const classes = ["bg-"+colorClass];
	if (colorClass && !colorClass?.endsWith("-lt")) {
		classes.push("text-white");
	}
	return classes;
}

function avatar(colored, entity, classes) {
	if (entity?.picture) {
		return `<span class="avatar ${classes}" style="background-image: url('${entityPicture(entity)}')"></span>`;
	}

	if (colored) {
		classes += " " + avatarColorClasses(entity?.id).join(" ");
	}

	const userIcon = `<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-user" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
		<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
		<circle cx="12" cy="7" r="4"></circle>
		<path d="M6 21v-2a4 4 0 0 1 4 -4h4a4 4 0 0 1 4 4v2"></path>
	</svg>`;

	return `<span class="avatar ${classes}">${initials(entity?.name) || userIcon}</span>`;
}

function initials(name) {
	if (!name) return "";

	// ignore any parenthetical suffix
	const regex = /^(.+)\(.+\)$/;
	let matches = regex.exec(name);
	if (matches) {
		name = matches[1].trim();
	}

	// TODO: maybe do this server-side?
	// strip emojis... this is cutting-edge, apparently (March 2023) -- not yet supported in Firefox :(
	// Emoji-aware split(): https://stackoverflow.com/a/71619350/1048862
	// Detecting emoji: https://stackoverflow.com/a/64007175/1048862
	if (Intl.Segmenter) {
		const splitEmoji = (string) => [...new Intl.Segmenter().segment(string)].map(x => x.segment);
		const runes = splitEmoji(name);
		const nonEmojiRunes = runes.filter(rune => !/\p{Extended_Pictographic}/u.test(rune));
		name = nonEmojiRunes.join("").trim();
	}

	let initials = name.split(" ").map((n) => n[0]).join("") || "?";
	if (initials.length > 2) {
		initials = initials[0] + initials[initials.length-1];
	}

	return initials.trim();
}

function maxlenStr(str, maxLen) {
	return (str || "").length > maxLen ? str.substring(0, maxLen).trim() + "..." : str;
}


function humanizeBytes(size) {
	if (size == null) {
		return "";
	}
	var i = size == 0 ? 0 : Math.floor(Math.log(size) / Math.log(1024));
	return (size / Math.pow(1024, i)).toFixed(2) * 1 + ' ' + ['bytes', 'KB', 'MB', 'GB', 'TB', 'PB'][i];
}


// for pages like /[entities|items|jobs]/uuid/rowID
function parseURIPath() {
	let parts = window.location.pathname.split('/');
	return {
		repoID: parts[2],
		rowID: Number(parts[3])
	};
}

// itemTimestampDisplay returns timestamp display for the item,
// taking into account timestamp, timespan, and timeframe fields.
// If endItem is specified, its time info is used to create a time
// span from item through endItem. Both items must have a timestamp.
function itemTimestampDisplay(item, endItem) {
	let result = {};

	if (!item.timestamp) {
		return result;
	}
	if (endItem == item) {
		endItem = undefined;
	}

	const dt = DateTime.fromISO(item.timestamp);
	result.relative = dt.toRelative();

	if (item.timeframe) {
		// find the level of precision by looking at each major compontent of the timestamps

		const tf = DateTime.fromISO(item.timeframe);
		const itvl = Interval.fromDateTimes(dt, tf);

		// hasSame() is a weirdly-named function, since it seems to actually return true
		// at the first unit that DIFFERS from start to end.
		if (itvl.hasSame('hour')) {
			result = {
				dateTime: `${dt.toLocaleString(DateTime.DATE_MED_WITH_WEEKDAY)} ${dt.toLocaleString(DateTime.TIME_WITH_SECONDS)}`,
				weekdayDate: `${dt.weekdayLong}, ${dt.toLocaleString(DateTime.DATE_MED)}`,
				dateWithWeekday: `${dt.toLocaleString(DateTime.DATE_MED)} (${dt.weekdayLong})`,
				time: `${dt.hour % 12} o'clock` // TODO: have it be like "1pm" instead of "13 o'clock"
			};
		} else {
			if (itvl.hasSame('day')) {
				result.dateTime = `${dt.toLocaleString(DateTime.DATE_MED_WITH_WEEKDAY)}`;
				result.weekdayDate = `${dt.weekdayLong}, ${dt.toLocaleString(DateTime.DATE_MED)}`,
				result.dateWithWeekday = `${dt.toLocaleString(DateTime.DATE_MED)} (${dt.weekdayLong})`;
			} else if (itvl.hasSame('month')) {
				result.dateTime = `${dt.monthLong} ${dt.year}`;
				result.weekdayDate = `${dt.monthLong} ${dt.year}`,
				result.dateWithWeekday = `${dt.monthLong} ${dt.year}`;
			} else if (itvl.hasSame('year')) {
				result.dateTime = `${dt.year}`;
				result.weekdayDate = `${dt.year}`;
				result.dateWithWeekday = `${dt.year}`;
			}
		}
	} else {
		result = {
			dateTime: `${dt.toLocaleString(DateTime.DATE_MED_WITH_WEEKDAY)} ${dt.toLocaleString(DateTime.TIME_WITH_SECONDS)}`,
			weekdayDate: `${dt.weekdayLong}, ${dt.toLocaleString(DateTime.DATE_MED)}`,
			dateWithWeekday: `${dt.toLocaleString(DateTime.DATE_MED)} (${dt.weekdayLong})`,
			time: dt.toLocaleString(DateTime.TIME_SIMPLE)
		};
	}

	const timespan = endItem?.timespan || endItem?.timestamp || item.timespan;
	if (timespan) {
		const tspanDt = DateTime.fromISO(timespan);
		const itvl = Interval.fromDateTimes(dt, tspanDt);
		result.dateTime = itvl.toLocaleString(DateTime.DATETIME_MED_WITH_WEEKDAY);
		result.time = itvl.toLocaleString(DateTime.TIME_SIMPLE);
		// TODO: calculate the duration... then also make sure the returned values indicate a timespan
	}

	return result;
}




























////////////////////////////////////////////////////
// Item display & rendering
////////////////////////////////////////////////////

function itemContentElement(item, opts) {
	function noContentElem() {
		let noContent = document.createElement('div');
		noContent.innerText = "Item has no content";
		noContent.classList.add('content', 'd-flex', 'justify-content-center', 'align-items-center', 'bg-muted-lt', 'py-5');
		return noContent;
	}

	if (item?.data_type?.startsWith("text/"))
	{
		const container = document.createElement('div');
		container.classList.add('content');
		container.dataset.contentType = "text";

		function renderTextData(elem, text) {
			if (opts?.maxLength) {
				text = maxlenStr(text, opts.maxLength);
			}

			if (item.data_type.startsWith("text/plain")) {
				elem.innerText = text;
			} else if (item.data_type.startsWith("text/markdown")) {
				elem.innerHTML = DOMPurify.sanitize(marked.parse(text));
			} else if (item.data_type.startsWith("text/html")) {
				const iframe = document.createElement('iframe');
				if (item.data_file) {
					iframe.src = `/repo/${item.repo_id}/${item.data_file}`;
				} else if (item.data_text) {
					iframe.srcdoc = DOMPurify.sanitize(truncated);
				}
				elem.append(iframe);
			}
		}

		if (item.data_text) {
			renderTextData(container, item.data_text);
		} else if (item.data_file) {
			// render HTML files directly into an iframe using its URL,
			// but other text data we download and truncate/parse
			if (item.data_type.startsWith("text/html")) {
				renderTextData(container);
			} else {
				fetch(`/repo/${item.repo_id}/${item.data_file}`)
					.then((response) => response.text())
					.then((text) => {
						renderTextData(container, text);
					});
			}
		} else {
			return noContentElem();
		}

		return container;
	}
	else if (item.data_file || item.data_hash)
	{
		if (item.data_type.startsWith("image/"))
		{
			// initial image tag (or placeholder element, if processing) to return
			const makeImgTag = function(src) {
				if (opts?.avatar) {
					const span = document.createElement('span');
					span.dataset.contentType = "image";
					span.classList.add("avatar", "avatar-xl", "m-1");
					span.style.backgroundImage = `url('${src}')`;
					return span;
				} else {
					const imgTag = new Image;
					imgTag.dataset.contentType = "image";
					imgTag.classList.add("content");
					if (src) {
						// there won't be a src if app is in obfuscation mode
						imgTag.src = src;
					}
					if (!opts?.noLazyLoading) {
						imgTag.loading = "lazy";
					}

					// TODO: handle image errors better
					imgTag.addEventListener('error', function(err) {
						console.error("Loading image failed:", err);
					});

					return imgTag;
				}
			};

			// make the img tag that will display the image; depending on some things
			// we may need to show a thumbhash or a loading indicator first, however,
			// as we want to support a lot of formats even if browsers don't natively;
			// this conversion can take some time - Note: HEIF images are extremely
			// common with iPhones, yet even Apple's own browser can't display them.
			// (2023 UPDATE: That may be resolved in 2024)
			// Note that an img tag can have an empty src attribute if obfuscation is
			// enabled in the app; in which case we just show the thumbhash instead
			const imgSrc = itemImgSrc(item, opts?.thumbnail);
			let imgTag;
			if (imgSrc) {
				imgTag = makeImgTag(imgSrc);
			}

			// remember, the actual img tag might not be used if the app is in obfuscation mode,
			// in which case we might just display the thumbhash image
			//
			// if the image has been cached or already loaded recently, it might already
			// be available; if so, skip the complicated stuff
			if (imgTag && ((imgTag.src && imgTag.complete) || opts?.avatar)) {
				return imgTag;
			}

			// the container will hold either the thumbhash and the final image, or
			// the loading indicator. If it holds the the loading indicator, it gets
			// replaced with the img tag when the image is done loading
			const container = document.createElement('div');

			// if a thumbhash is available, render it while we load the full image/thumbnail
			if (item.thumb_hash) {
				const thumbHashWithAspectRatio = decodeBase64ToBytes(item.thumb_hash);
				const aspectRatioBytes = thumbHashWithAspectRatio.subarray(0, 4);
				const thumbHash = thumbHashWithAspectRatio.subarray(4);
				const aspectRatio = new DataView(aspectRatioBytes.buffer).getFloat32(0);

				const thumbhashImgTag = makeImgTag(thumbHashToDataURL(thumbHash));
				thumbhashImgTag.classList.add('thumbhash');
				thumbhashImgTag.style.aspectRatio = aspectRatio;

				container.classList.add('thumbhash-container', 'rounded');

				container.append(thumbhashImgTag);

				if (imgTag) {
					// getting the preview image will take some time: hide the img tag until it's
					// done loading, and when it's done, swap the placeholder for, or show, the image
					imgTag.classList.add('invisible');

					// make final image appear directly over the thumbhash image
					imgTag.classList.add('absolute');

					// when the image has loaded, fade in the picture over the thumbhash
					imgTag.addEventListener('load', function() {
						imgTag.classList.add('fade-in');
						imgTag.classList.remove('invisible');
						// TODO: This is used on the item page where the image may be replaced by the motionpic...
						setTimeout(function() {
							imgTag.classList.remove('fade-in');
						}, 1000);
					});

					container.append(imgTag);
				}
			} else {
				const loaderSupercontainer = cloneTemplate('#loader-container');
				$('.loading-message', loaderSupercontainer).innerText = "Rendering preview";

				// prepend the img tag to the supercontainer since it's (probably)
				// lazy-loaded, and it has to be on the DOM to try loading -- then
				// we can fade out the loader 
				// TODO: Make videos have the same effect
				loaderSupercontainer.prepend(imgTag);

				container.append(loaderSupercontainer);

				// when the image has loaded, replace loader element with the image
				// (imgTag might be unset if obfuscation is enabled)
				imgTag?.addEventListener('load', function() {
					// TODO: is this still true now that we don't replace the container? (TODO: actually, we probably should, to keep things tidy, no need to keep the "supercontainer" around. just the content)
					// In case the caller added classes to the returned element (the container),
					// we will need to add those to the imgTag since it will replace the container.
					container.classList.forEach(name => imgTag.classList.add(name));

					$('.loader-container', loaderSupercontainer).classList.add('fade-out');
					setTimeout(function() {
						$('.loader-container', loaderSupercontainer).remove();
					}, 1000);
					
				});
			}

			return container;
		}
		else if (item.data_type.startsWith("video/"))
		{
			// TODO: thumbhashes for videos too? (we'd have to generate an image thumbnail in the process)
			const makeVideoTag = function(sources) {
				const videoTag = document.createElement('video');
				videoTag.dataset.contentType = "video";
				videoTag.classList.add('content', 'rounded');
				if (opts?.thumbnail) {
					videoTag.classList.add('video-thumbnail');
				}
				if (opts?.controls !== false && !opts?.thumbnail) {
					videoTag.controls = true;
				}
				if (opts?.autoplay) {
					videoTag.muted = true;
					videoTag.autoplay = true;
				}
				videoTag.loop = true;
				// videoTag.src = src; // TODO: this breaks Safari with our method! Safari sends a Range request for 2 bytes to probe the Content-Type header, but if we use <source> and specify it then it works
				for (const source of sources) {
					const sourceTag = document.createElement('source');
					sourceTag.src = source.src;
					sourceTag.type = source.type;
					videoTag.append(sourceTag);
				}

				// TODO: error handling
				// videoTag.addEventListener("error", e => {
				// 	switch (e.target.error.code) {
				// 	case e.target.error.MEDIA_ERR_ABORTED:
				// 		alert('You aborted the video playback.');
				// 		break;
				// 	case e.target.error.MEDIA_ERR_NETWORK:
				// 		alert('A network error caused the video download to fail part-way.');
				// 		break;
				// 	case e.target.error.MEDIA_ERR_DECODE:
				// 		alert('The video playback was aborted due to a corruption problem or because the video used features your browser did not support.');
				// 		break;
				// 	case e.target.error.MEDIA_ERR_SRC_NOT_SUPPORTED:
				// 		alert('The video could not be loaded, either because the server or network failed or because the format is not supported.');
				// 		break;
				// 	default:
				// 		alert('An unknown error occurred.');
				// 		break;
				// 	}
				// });

				return videoTag;
			};

			// // 3GPP files are common among text messages.
			// if (item.data_type == "video/3gpp") {
			// 	const loader = cloneTemplate('#loader-container');
			// 	$('.loading-message', loader).innerText = "Transcoding";
			// 	// TODO: if conversion fails, we should show or do something productive
			// 	app.VideoToMP4(item.repo_id, item.data_file).then(mp4 => {
			// 		loader.replaceWith(makeVideoTag("data:video/mp4;base64,"+mp4));
			// 	});
			// 	return loader;
			// }
			if (item.data_file) {
				if (opts?.thumbnail) {
					return makeVideoTag([ {src: itemImgSrc(item, true), type: 'video/webm'} ]);
				} else {
					// prefer original video if browser supports it, otherwise they will have to choose the transcode
					return makeVideoTag([
						{src: `/repo/${item.repo_id}/${item.data_file}`, type: item.data_type},
						{src: `/repo/${item.repo_id}/transcode/${item.data_file}`, type: 'video/webm'}
					]);
				}
			} else {
				// TODO: this means that obfuscation mode is enabled: how to blur the video?? (we could use CSS I suppose... but I don't like leaving it up to the front-end)
				// TODO: blur the video with this ffmpeg flag: -vf "boxblur=50:5" (box blur, value 50, applied 5 times)
				return noContentElem();
			}
		}
		else if (item.data_type.startsWith("audio/") || item.data_type == "application/ogg")
		{
			const audioTag = document.createElement('audio');
			audioTag.classList.add('content');
			audioTag.dataset.contentType = "audio";
			audioTag.setAttribute('controls', '');
			audioTag.src = `/repo/${item.repo_id}/${item.data_file}`;
			return audioTag;
		}
		else
		{
			const elem = noContentElem();
			elem.innerText = "Unsupported format";
			if (item.data_type) {
				elem.innerText += ` (${item.data_type})`;
			}
			return elem;
		}
	}
	else if (item.latitude && item.longitude)
	{
		const container = cloneTemplate('#map-container');
		container.dataset.contentType = "location";

		// size the container because the map won't stretch it, it will only expand to fill space
		container.classList.add('ratio-16x9');

		tlz.map.tl_containers.set(container, function() {
			tlz.map.tl_addNavControl();
			const marker = new mapboxgl.Marker().setLngLat([item.longitude, item.latitude]);
			tlz.map.tl_addMarker(marker);
			tlz.map.flyTo({center: marker.getLngLat(), zoom: 16});
		});

		return container;
	}
	else
	{
		return noContentElem();
	}
}


// itemMiniDisplay returns a populated, ready-to-render DOM element
// that comprises a mini-display or preview of the items, which must
// be an array of items that are all the same classification; or at
// least, that are to be rendered the same way as the first item.
function itemMiniDisplay(items, options) {
	let representative = items[0];

	switch (representative.classification) {
		case 'message':
		case 'email':
		case 'social':
			return miniDisplayMessages(items);
		case 'media':
			return miniDisplayMedia(items, options);
		case 'location':
			return miniDisplayLocations(items, options);
		case 'document':
		case 'note':
		case 'bookmark':
			return miniDisplayBookmark(items);
		case 'page_view':
				return miniDisplayPageView(items);
		default:
			console.warn("TODO: UNSUPPORTED ITEM CLASS:", representative.classification, items);
			return miniDisplayMisc(items);
	}
}

function miniDisplayLocations(items, options) {
	const minidisp = {
		icon: `<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-live-view" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
				<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<path d="M4 8v-2a2 2 0 0 1 2 -2h2"></path>
				<path d="M4 16v2a2 2 0 0 0 2 2h2"></path>
				<path d="M16 4h2a2 2 0 0 1 2 2v2"></path>
				<path d="M16 20h2a2 2 0 0 0 2 -2v-2"></path>
				<line x1="12" y1="11" x2="12" y2="11.01"></line>
				<path d="M12 18l-3.5 -5a4 4 0 1 1 7 0l-3.5 5"></path>
			</svg>`,
		iconColor: 'cyan'
	}

	if (options?.noMap) {
		return minidisp;
	}

	const ratioElem = cloneTemplate('#map-container');
	ratioElem.classList.add('ratio-16x9', 'rounded', 'overflow-hidden');

	const mapRenderFn = function () {
		tlz.map.tl_addNavControl();

		// add each marker to the map and build bounding box as we go
		const bounds = new mapboxgl.LngLatBounds();
		for (const item of items) {
			const marker = new mapboxgl.Marker()
				.setLngLat([item.longitude, item.latitude]);
			// TODO: make useful popups
			// .setPopup(
			// 	new mapboxgl.Popup({ offset: 25 }).setHTML(info)
			// )
			tlz.map.tl_addMarker(marker);

			bounds.extend([item.longitude, item.latitude]);
		}

		tlz.map.fitBounds(bounds, { padding: 75, maxZoom: 16 }); // set `duration: 0` in the options to disable animation/easing
	};

	tlz.map.tl_containers.set(ratioElem, mapRenderFn);

	minidisp.element = ratioElem;

	return minidisp;
}


function miniDisplayMedia(items, options) {
	const container = cloneTemplate('#tpl-media-card');

	for (const item of items) {
		const a = document.createElement('a');
		a.href = `/items/${item.repo_id}/${item.id}`;
		const elem = itemContentElement(item, { options, thumbnail: true });
		a.append(elem);
		container.append(a);
	}

	if (items.length == 1) {
		container.classList.add('minidisp-media-xl', 'minidisp-media-nocrop');
	} else if (items.length <= 3) {
		container.classList.add('minidisp-media-l', 'minidisp-media-nocrop');
	} else if (items.length >= 4 && items.length < 7) {
		container.classList.add('minidisp-media-m', 'minidisp-media-nocrop');
	}

	return {
		icon: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-camera" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M5 7h1a2 2 0 0 0 2 -2a1 1 0 0 1 1 -1h6a1 1 0 0 1 1 1a2 2 0 0 0 2 2h1a2 2 0 0 1 2 2v9a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2 -2v-9a2 2 0 0 1 2 -2"></path>
			<circle cx="12" cy="13" r="3"></circle>
		</svg>`,
		iconColor: 'pink',
		element: container,
	};
}

function miniDisplayPageView(items) {
	const container = document.createElement('div');
	for (const item of items) {
		container.append(renderPageViewItem(item));
	}

	return {
		icon: `
		  <svg  xmlns="http://www.w3.org/2000/svg"  width="24"  height="24"  viewBox="0 0 24 24"  fill="none"  stroke="currentColor"  stroke-width="2"  stroke-linecap="round"  stroke-linejoin="round"  class="icon icon-tabler icons-tabler-outline icon-tabler-history">
				<path stroke="none" d="M0 0h24v24H0z" fill="none"/>
				<path d="M12 8l0 4l2 2" />
				<path d="M3.05 11a9 9 0 1 1 .5 4m-.5 5v-5h5" />
			</svg>
			`,
		iconColor: 'purple',
		element: container,
	};
}

function renderPageViewItem(item) {
	const el = document.createElement('div');
	el.classList.add('page_view', 'page_view-fold');
	el.append(itemContentElement(item, {
		maxLength: 1024, // we don't want to show an entire big file on a mini-display
	}));
	return el;
}

function miniDisplayBookmark(items) {
	const container = document.createElement('div');
	for (const item of items) {
		container.append(renderBookmarkItem(item));
	}

	return {
		icon: `
		  <svg  xmlns="http://www.w3.org/2000/svg"  width="24"  height="24"  viewBox="0 0 24 24"  fill="none"  stroke="currentColor"  stroke-width="2"  stroke-linecap="round"  stroke-linejoin="round"  class="icon icon-tabler icons-tabler-outline icon-tabler-bookmark">
				<path stroke="none" d="M0 0h24v24H0z" fill="none"/>
				<path d="M18 7v14l-6 -4l-6 4v-14a4 4 0 0 1 4 -4h4a4 4 0 0 1 4 4z" />
			</svg>`,
		iconColor: 'lime',
		element: container,
	};
}

function renderBookmarkItem(item) {
	const container = document.createElement('div');
	container.classList.add('card');
	const cardBody = document.createElement('div');
	cardBody.classList.add('card-body', 'margin-for-ribbon-top-left');
	cardBody.append(itemContentElement(item, {
		maxLength: 1024, // we don't want to show an entire big file on a mini-display
	}));
	const ribbonEl = document.createElement('div');
	ribbonEl.classList.add('ribbon', 'ribbon-top', 'ribbon-start', 'ribbon-bookmark');
	container.append(cardBody, ribbonEl);
	return container;
}



function miniDisplayPaper(items) {
	const container = document.createElement('div');
	for (const item of items) {
		container.append(renderPaperItem(item));
	}

	return {
		icon: `
			<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-file-text">
				<path stroke="none" d="M0 0h24v24H0z" fill="none" />
				<path d="M14 3v4a1 1 0 0 0 1 1h4" />
				<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z" />
				<path d="M9 9l1 0" />
				<path d="M9 13l6 0" />
				<path d="M9 17l6 0" />
			</svg>`,
		iconColor: 'lime',
		element: container,
	};
}

function renderPaperItem(item) {
	const el = document.createElement('div');
	el.classList.add('paper', 'paper-fold');
	el.append(itemContentElement(item, {
		maxLength: 1024, // we don't want to show an entire big file on a mini-display
	}));
	return el;
}


function miniDisplayMisc(items) {
	const container = document.createElement('div');
	for (const item of items) {
		container.append(itemContentElement(item, {
			maxLength: 1024, // we don't want to show an entire big file on a mini-display
		}));
	}
	return {
		icon: `
			<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-file-unknown">
				<path stroke="none" d="M0 0h24v24H0z" fill="none" />
				<path d="M14 3v4a1 1 0 0 0 1 1h4" />
				<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z" />
				<path d="M12 17v.01" />
				<path d="M12 14a1.5 1.5 0 1 0 -1.14 -2.474" />
			</svg>`,
		iconColor: 'gray',
		element: container,
	};
}

function miniDisplayMessages(items) {
	const card = document.createElement('div');
	card.classList.add('card');

	const cardBody = document.createElement('div');
	cardBody.classList.add('chat', 'card-body');

	const chatBubbles = document.createElement('div');
	chatBubbles.classList.add('chat-bubbles');
	
	for (const item of items) {
		chatBubbles.append(renderMessageItem(item, {withToRelations: true}));
	}

	cardBody.append(chatBubbles);
	card.append(cardBody);
	

	return {
		icon: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-messages" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M21 14l-3 -3h-7a1 1 0 0 1 -1 -1v-6a1 1 0 0 1 1 -1h9a1 1 0 0 1 1 1v10"></path>
			<path d="M14 15v2a1 1 0 0 1 -1 1h-7l-3 3v-10a1 1 0 0 1 1 -1h2"></path>
		</svg>`,
		iconColor: 'indigo',
		element: card,
	};
}


function renderMessageItem(item, options) {
	function recipients(item) {
		const sendersContainer = document.createElement('div');
		for (const rel of item.related) {
			if ((rel.label != 'sent' && rel.label != 'cc') || !rel.to_entity)
				continue;
			const entityEl = document.createElement('div');
			entityEl.classList.add('fw-bold');
			entityEl.innerText = rel.to_entity.name || rel.to_entity.attribute.value;
			sendersContainer.append(entityEl);
		}
		return sendersContainer;
	}

	// sometimes, a message may have no content and only have attachments
	// (this is unfortunate, but often seems to be the case with iMessage;
	// we can't just delete the 'parent' item because it has an ID that
	// is referenced in various places)
	// in that case, we can at least promote the first attachment in the
	// display of the message so that it becomes the main content of the
	// message, and any other attachments remain attached, without an
	// empty item cluttering the view; this is what most chat/messaging
	// apps do anyway, including iMessage
	if (!item.data_text && !item.data_file && item.related) {
		for (const [i, rel] of item.related.entries()) {
			if (rel.label == 'attachment' && rel.to_item) {
				// promote first attachment, so remove it from the related items list
				item.related.splice(i, 1);
				// combine any related items of the empty item with that of the related item
				rel.to_item.related = rel.to_item.related ? item.related.concat(rel.to_item.related) : item.related;
				// combine metadata
				if (item.metadata && rel.to_item.metadata) {
					rel.to_item.metadata = {...rel.to_item.metadata, ...item.metadata};
				} else if (item.metadata) {
					rel.to_item.metadata = item.metadata;
				}
				// finally, replace the item with the related item that we spliced out
				item = rel.to_item;
				break;
			}
		}
	}

	const elem = cloneTemplate('#tpl-message');
	$('.message-sender', elem).innerText = item.entity?.name || item.entity?.attribute?.value;
	if (item.entity?.id == 1) {
		// if current user (presumably, entity ID 1 -- though this could change later) is
		// the sender, then put their own chats along the right, I guess, with the avatar
		// on the outside (far right)
		$('.align-items-top', elem).classList.add('flex-row-reverse');
		$('.chat-bubble', elem).classList.add('chat-bubble-me');
	}
	$('.message-timestamp', elem).innerText = DateTime.fromISO(item.timestamp).toLocaleString(DateTime.DATETIME_MED);
	$('.message-avatar', elem).innerHTML = avatar(true, item.entity);
	$('.data-source-icon', elem).style.backgroundImage = `url('/ds-image/${item.data_source_name}')`;
	$('.data-source-icon', elem).title = item.data_source_title;
	$('.data-source-icon', elem).dataset.bsToggle = "tooltip";
	$('.view-item-link', elem).href = `/items/${item.repo_id}/${item.id}`;
	$('.view-entity-link', elem).href = `/entities/${item.repo_id}/${item.entity.id}`;

	if (options?.withToRelations && item.related) {
		const toContainer = document.createElement('div');
		toContainer.classList.add('d-flex');
		toContainer.innerHTML = `
			<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-corner-down-right" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
				<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
				<path d="M6 6v6a3 3 0 0 0 3 3h10l-4 -4m0 8l4 -4"></path>
			</svg>`;
		toContainer.append(recipients(item));
		$('.message-sender', elem).parentNode.insertAdjacentElement('afterend', toContainer);
	}

	if (item.data_text) {
		$('.message-content', elem).innerText = item.data_text;
	} else {
		let mediaElem = itemContentElement(item, { thumbnail: true, noLazyLoading: options?.noLazyLoading });
		mediaElem.classList.add("rounded");
		$('.message-content', elem).appendChild(mediaElem);
	}

	if (item.related) {
		const reactions = {};

		for (const rel of item.related) {
			if (rel.label == 'attachment' && rel.to_item) {
				$('.attachments', elem).classList.remove('d-none');
				if (rel.to_item.data_type.startsWith('image/')) {
					let imgTag = document.createElement('span');
					imgTag.classList.add("avatar", "avatar-xl", "m-1");
					imgTag.style.backgroundImage = `url('${itemImgSrc(rel.to_item, true)}')`;
					$('.attachments', elem).appendChild(imgTag);
				} else if (rel.to_item.data_type.startsWith('video/')) {
					let videoTag = document.createElement('video');
					videoTag.src = `/repo/${item.repo_id}/${rel.to_item.data_file}`;
					videoTag.setAttribute('type', rel.to_item.data_type);
					videoTag.setAttribute('controls', '');
					$('.attachments', elem).appendChild(videoTag);
				}
			} else if (rel.label == 'reacted' && rel.from_entity) {
				if (reactions[rel.value] === undefined) {
					reactions[rel.value] = [];
				}
				reactions[rel.value].push(rel);
			}
		}

		// TODO: put these somewhere more permanent
		const reactionLabels = {
			"\u2764\uFE0F": "Love", // ❤️ but red
			"👍": "Like",
			"👎": "Dislike",
			"😂": "Laugh",
			"\u203C\uFE0F": "Emphasis", // ‼️ but red
			"❓": "Question",
		};

		// render any reactions we accumulated
		if (Object.keys(reactions).length) {
			$('.message-reactions', elem).classList.remove('d-none');

			for (const [reaction, rels] of Object.entries(reactions)) {
				var reactElem = document.createElement('div');
				reactElem.classList.add('message-reaction');
				reactElem.dataset.bsToggle = "tooltip";
				var emojiElem = document.createElement('span');
				emojiElem.classList.add('emoji');
				emojiElem.textContent = reaction;
				reactElem.append(emojiElem);
				if (rels.length > 1) {
					reactElem.innerHTML += ` ${rels.length}`;
				}
				reactElem.title = `${reactionLabels[reaction] || reaction} (${rels.length}): `;
				for (const rel of rels) {
					reactElem.title += `${entityDisplayNameAndAttr(rel.from_entity).name}, `;
				}
				reactElem.title = reactElem.title.slice(0, -2); // trim off trailing comma and space
				$('.message-reactions', elem).appendChild(reactElem);
			}
		}

	}

	return elem;
}


function timelineGroups(items, options) {
	if (!items) return [];
	
	const groups = [];
	let lastLocGroupIdx;
	for (const item of items) {
		if (!item) continue;

		// TODO: only do social as conversation if it has a sent relation...
		item._category = item.classification || "unknown"; // in case item is not classified, still have a category, otherwise no group is made and an error occurs below (see #36)
		if (item.classification == 'message'
			|| item.classification == 'email'
			|| item.classification == 'social') {
				item._category = "conversation";
		}

		if (!options?.noMap && item.latitude && item.longitude && item._category == 'location') {
			if (!lastLocGroupIdx) {
				// it makes more sense to see a map before the items at those locations, so show
				// locations before the current group, i.e. 1 from the end (-2 because 0-based indexing)
				groups.splice(groups.length-2, 0, []);
				// TODO: make sure this group has a category of location...
				lastLocGroupIdx = Math.max(groups.length-2, 0);
			}
			groups[lastLocGroupIdx].push(item);
		}

		if (groups[groups.length-1]?.[0]?._category != item._category) {
			// current item is in different category as previous item, so create new group
			groups.push([]);
		}
		if (item._category == "location") {
			// put locations in the most recent location group
			lastLocGroupIdx = groups.length-1;
		}
		groups[groups.length-1].push(item);
	}

	// split messages into groups by conversation (participants)
	for (let i = 0; i < groups.length; i++) {
		const group = groups[i];

		if (group.length > 0 && group[0]._category != "conversation") {
			continue;
		}

		const convos = {};

		for (const item of group) {
			const participants = [ item.entity?.id ];
			if (item.related) {
				for (const rel of item.related) {
					if ((rel.label == 'sent' || rel.label == 'cc') && rel.to_entity) {
						participants.push(rel.to_entity.id);
					}
				}
			}
			participants.sort();
			const convoKey = participants.join(',');
			if (!convos[convoKey]) {
				convos[convoKey] = [];
			}
			convos[convoKey].push(item);
		}

		const convosArray = Object.values(convos);
		convosArray.sort((a, b) => a[0].timestamp > b[0].timestamp ? 1 : -1 );

		groups.splice(i, 1, ...convosArray);
	}

	// further divide groups by temporal locality and max size
	const newGroups = [];
	const maxGroupSizes = {
		conversation: 50,
		media: 25,
		location: 100
	};

	for (let i = 0; i < groups.length; i++) {
		const group = groups[i];

		let g = [];
		let window = [];
		const windowSize = 3;
		let windowAvgTimeDelta = 0, windowVariance = 0;

		for (let j = 0; j < group.length; j++) {
			const item = group[j];
			if (j == 0) {
				g.push(item);
				continue;
			}
			const prevItem = group[j-1];

			// create new group if max size has been reached, or if it's been a long time since the last item
			if (g.length >= maxGroupSizes[g[0]._category]) {
				newGroups.push(g);
				g = [];
			} else {
				const delta = durationBetween(item.timestamp, prevItem.timestamp).as('seconds');
				window.push(delta);
				if (window.length == 1) {
					windowAvgTimeDelta = delta;
				}

				const oldAvg = windowAvgTimeDelta;
				const goingAway = window[0];
				const changeOfAvgTimeDelta = (delta - goingAway) / windowSize;
				windowAvgTimeDelta += changeOfAvgTimeDelta;
				if (window.length > windowSize) {
					window = window.slice(-windowSize);
				}

				windowVariance += (delta - goingAway) * ((delta - windowAvgTimeDelta) + (goingAway - oldAvg)) / (windowSize - 1);
				const sd = Math.sqrt(windowVariance)

				// console.log("DELTA:", delta, "WINDOW:", window, "SD:", sd, "DIFF:", Math.abs(windowAvgTimeDelta - delta), "ITEM:", item);
				if (window.length == windowSize && Math.abs(windowAvgTimeDelta - delta) > sd * 1.75) {
					// console.log("NEW GROUP:", g)
					newGroups.push(g);
					g = [];
				}
			}

			g.push(item);
		}
		if (g.length) {
			newGroups.push(g);
		}
	}

	console.log("GROUPS:", newGroups);

	return newGroups;

	// TODO: I'm not sure this is a good idea, but:
	////////////////////////
	// display items in ASC order (oldest at top, newest at bottom)
	// if ($('.filter .date-sort').value == "DESC") {
	// 	groups.forEach(group => group.reverse());
	// }
	// console.log("GROUPS:", groups)
	// return groups;
}


function renderTimelineGroups(groups, options) {
	const timelineEl = cloneTemplate('#tpl-timeline');

	groups.forEach((group, i) => {
		if (!group.length) return;

		const prevGroup = groups[i-1];
		const groupDate = new Date(group[0].timestamp);
		const prevGroupDate = new Date(prevGroup?.[prevGroup?.length-1]?.timestamp);

		let sortVal = $('.date-sort').value;
		const firstItem = sortVal == 'ASC' ? group[0] : group[group.length-1];
		const lastItem = sortVal == 'ASC' ? group[group.length-1] : group[0];

		const tsDisplay = itemTimestampDisplay(firstItem, lastItem);

		if (prevGroupDate?.getDate() != groupDate.getDate()
			|| prevGroupDate?.getMonth() != groupDate.getMonth()
			|| prevGroupDate?.getFullYear() != groupDate.getFullYear()) {
				const dateEl = cloneTemplate('#tpl-tl-item-date-card');
				$('.list-timeline-date', dateEl).innerText = tsDisplay.weekdayDate;
				timelineEl.append(dateEl);
		}

		const itemEl = cloneTemplate('#tpl-tl-item-card');

		$('.list-timeline-time', itemEl).innerText = tsDisplay.time;

		const display = itemMiniDisplay(group, options);

		if (display.element) {
			$('.list-timeline-icon', itemEl).innerHTML = display.icon;
			$('.list-timeline-icon', itemEl).classList.add(`bg-${display.iconColor}`);
			$('.list-timeline-content-container', itemEl).append(display.element);

			$('.list-timeline-date-anchor:last-of-type', timelineEl).append(itemEl);
		}
	});

	return timelineEl;
}

function itemsAsTimeline(items, options) {
	const groups = timelineGroups(items, options);
	return renderTimelineGroups(groups, options);
}








////////////////////////////////////////////////////
// Item preview modal
////////////////////////////////////////////////////

const PreviewModal = (function() {
	// private state
	const private = {
		reset: function() {
			$('#modal-preview-content').replaceChildren();
			$('#modal-preview .media-owner-name').replaceChildren();
			$('#modal-preview .media-owner-avatar').replaceChildren();
		}
	};

	// public interface
	const public = {
		// prev, next functions should be set by the pages that use this modal
		renderItem: async function(item) {
			private.reset();

			$('#modal-preview .media-owner-avatar').innerHTML = avatar(true, item.entity, "me-3");
			$('#modal-preview .media-owner-name').innerText = entityDisplayNameAndAttr(item.entity).name;
			$('#modal-preview .text-secondary').innerText = DateTime.fromISO(item.timestamp).toLocaleString(DateTime.DATETIME_MED);

			$('#modal-preview .modal-title').innerHTML = `<span class="avatar avatar-xs rounded me-2" style="background-image: url('/ds-image/${item.data_source_name}')"></span>`;
			$('#modal-preview .modal-title').appendChild(document.createTextNode(item?.filename || baseFilename(item?.data_file)));
			$('#modal-preview .subheader').innerText = `# ${item.id}`;

			const mediaElem = itemContentElement(item);
			$('#modal-preview-content').append(mediaElem);

			// toggle prev/next buttons
			private.prevItem = await this.prev(item);
			private.nextItem = await this.next(item);
			if (private.prevItem) {
				$$('#modal-preview .btn-prev').forEach(elem => elem.classList.remove('disabled'));
			} else {
				$$('#modal-preview .btn-prev').forEach(elem => elem.classList.add('disabled'));
			}

			if (private.nextItem) {
				$$('#modal-preview .btn-next').forEach(elem => elem.classList.remove('disabled'));
			} else {
				$$('#modal-preview .btn-next').forEach(elem => elem.classList.add('disabled'));
			}

			$('#modal-preview .btn-primary').href = `/items/${item.repo_id}/${item.id}`;

			// TODO: is this used/needed?
			private.currentItem = item;
		}
	};

	// wire up previous/next buttons
	on('click', '#modal-preview .btn-prev', async() => {
		 await public.renderItem(private.prevItem);
	});
	on('click', '#modal-preview .btn-next', async() => {
		await public.renderItem(private.nextItem);
   });

	// when starting to hide the modal, stop any videos that are playing
	on('hide.bs.modal', '#modal-preview', () => {
		$('#modal-preview video')?.pause();
	});

	// when modal is fully hidden, clear it out
	on('hidden.bs.modal', '#modal-preview', private.reset);

	return public;
})();


function itemPreviews(items) {
	// only show a handful of items in the preview
	const maxItems = 5;
	const more = items.length - maxItems;
	items = items.slice(0, maxItems);

	const container = cloneTemplate('#tpl-compact-preview');
	for (const item of items) {
		const contentEl = itemContentElement(item, {
			thumbnail: true,
			maxLength: 100,
			autoplay: true
		});
		container.append(contentEl);
	}

	if (more > 0) {
		container.innerHTML += `<div class="more">+${more} more</div>`;
	}

	return container;
}



////////////////////////////////////////////////////
// Entity select dropdown
////////////////////////////////////////////////////

function newEntitySelect(element, maxItems, noWrap) {
	if ($(element).tomselect) {
		return $(element).tomselect;
	}

	function tomSelectRenderItemAndOption(entity, escape) {
		const {name, attribute} = entityDisplayNameAndAttr(entity);
		return `<div class="d-flex">
			<div class="dropdown-item-indicator">
				${avatar(true, entity, "avatar-xs")}
			</div>
			<div>
				<div>${escape(name)}</div>
				<div class="text-secondary">${escape(attribute || "")}</div>
			</div>
		</div>`;
	}

	const ts = new TomSelect(element, {
		valueField: "id",
		maxItems: maxItems,
		searchField: [
			{
				field: "id",
				weight: 2
			},
			{
				field: "name",
				weight: 1
			},
			// TomSelect isn't capable of filtering nested elements, so we flatten attributes for it...
			{
				field: "ts_attribute_0",
				weight: 0.5
			},
			{
				field: "ts_attribute_1",
				weight: 0.5
			},
			{
				field: "ts_attribute_2",
				weight: 0.5
			},
			{
				field: "ts_attribute_3",
				weight: 0.5
			},
			{
				field: "ts_attribute_4",
				weight: 0.5
			},
		],
		load: function(query, callback) {
			const params = {
				repo: tlz.openRepos[0].instance_id,
				"or_fields": true
			};

			if (query.startsWith("id:")) {
				query = query.slice(3);
				params.row_id = [Number(query)];
			} else {
				params.name = [`%${query}%`];
				params.attributes = [
					{
						"value": `%${query}%`
					}
				];
			}

			app.SearchEntities(params).then((results) => {
				// I don't think TomSelect is capable of filtering results based on
				// complex structures / nested fields... so purely for the benefit of
				// TomSelect, flatten attribute values into the top level of each result
				for (let i = 0; i < results?.length; i++) {
					for (let j = 0; j < results[i].attributes.length; j++) {
						results[i][`ts_attribute_${j}`] = results[i].attributes[j].value;
					}
				}
				callback(results);
			}).catch((err)=>{
				// TODO: handle errors here....
				console.error(err);
				callback();
			});
		},
		render: {
			item: tomSelectRenderItemAndOption,
			option: tomSelectRenderItemAndOption
		}
	});

	if (noWrap) {
		ts.control.classList.add('single-line');
	}

	// Clear input after selecting matching option from list
	// (I have no idea why this isn't the default behavior)
	ts.on('item_add', () => ts.control_input.value = '' );

	return ts;
}

























////////////////////////////////////////////////////
// Filtering stuff
////////////////////////////////////////////////////

function filterToQueryString() {
	var qs = new URLSearchParams(window.location.search);

	// date(s)
	if ($('.date-input')) {
		const pickerDates = $('.date-input').datepicker.selectedDates;
		if (pickerDates.length > 0) {
			qs.set('start', dateToUnixSec(pickerDates[0]));
		} else {
			qs.delete('start');
		}
		if (pickerDates.length == 2) {
			qs.set('end', dateToUnixSec(pickerDates[1]));
		} else {
			qs.delete('end');
		}
	}

	// sort
	// TODO: ASC should be default, yeah?
	if ($('.date-sort')) {
		let sortVal = $('.date-sort').value;
		if (sortVal && sortVal != 'DESC') {
			qs.set('sort', sortVal);
		} else {
			qs.delete('sort');
		}
	}

	// entity
	if ($('.entity-input.tomselected')) {
		const ts = $('.entity-input.tomselected').tomselect;
		const val = ts.getValue();
		if (Array.isArray(val) && val.length) {
			qs.set("entity", val.join(','));
		} else {
			qs.delete("entity");
		}
	}
	if ($('#selected-entities-only')) {
		// whether selected entities are necessary or sufficient;
		// so far, this is exclusive to the conversations page
		const onlySelected = $('#selected-entities-only').checked;
		if (onlySelected) {
			qs.set("only_entity", "true");
		} else {
			qs.delete("only_entity")
		}
	}

	// data sources
	if ($('.tl-data-source.tomselected')) {
		const ts = $('.filter .tl-data-source.tomselected').tomselect;
		const val = ts.getValue();
		if (Array.isArray(val) && val.length) {
			qs.set("data_source", val.join(','));
		} else {
			qs.delete("data_source");
		}
	}

	// item types
	const checkedItemClasses = $$('.tl-item-class-dropdown input:checked');
	const allItemClasses = $$('.tl-item-class-dropdown input')
	if (checkedItemClasses.length < allItemClasses.length) {
		const itemClasses = [];
		for (const elem of checkedItemClasses) {
			itemClasses.push(elem.value);
		}
		qs.set('class', itemClasses);
	} else {
		qs.delete('class');
	}

	if ($('#format-images')) {
		if (!$('#format-images').checked)
			qs.set('images', 0)
		else
			qs.delete('images');
	}
	if ($('#format-videos')) {
		if (!$('#format-videos').checked)
			qs.set('videos', 0)
		else
			qs.delete('videos');
	}
	if ($('#include-attachments')) {
		if (!$('#include-attachments').checked)
			qs.set('attachments', 0)
		else
			qs.delete('attachments');
	}

	// semantic search
	if ($('.semantic-text-search')) {
		if ($('.semantic-text-search').value)
			qs.set('semantic_text', $('.semantic-text-search').value);
		else
			qs.delete('semantic_text');
	}

	// text search for conversations
	if ($('#message-substring')) {
		if ($('#message-substring').value)
			qs.set('text', $('#message-substring').value);
		else
			qs.delete('text');
	}

	return qs;
}

async function queryStringToFilter() {
	const qs = new URLSearchParams(window.location.search);

	// display page numbers
	$$('.page-number').forEach((el) => el.innerText = `Page ${currentPageNum()}`);

	// date filter
	// TODO: persist date to session storage, so that if there's no date in qs, we can reuse the last date settings the user picked
	if ($('.date-input')) {
		const start = qs.get('start');
		const end = qs.get('end');
		if (start && end) {
			$('.date-input').datepicker.update({range: true});
		}
		if (start || end) {
			const dates = [];
			if (start) dates.push(unixSecToDate(start));
			if (end)   dates.push(unixSecToDate(end));
			$('.date-input').datepicker.selectDate(dates);
		}
	}
	if ($('.date-sort') && qs.get('sort')) {
		$('.date-sort').value = qs.get('sort');
	}

	// entity
	if ($('.entity-input') && qs.get("entity")) {
		const initialEntity = Number(qs.get("entity"))
		const entities = await app.SearchEntities({
			repo: tlz.openRepos[0].instance_id,
			row_id: [initialEntity]
		});
		if (entities?.length) {
			$('.entity-input').tomselect.addOption(entities[0]);
			$('.entity-input').tomselect.addItem(entities[0].id, true); // true = don't fire event (the updated filter gets submitted later)
		}
	}
	if ($('#selected-entities-only') && qs.get("only_entity")) {
		// whether selected entities are necessary or sufficient;
		// so far, this is exclusive to the conversations page
		$('#selected-entities-only').checked = qs.get("only_entity") == "true";
	}

	// data sources
	if ($('.tl-data-source') && qs.get("data_source")) {
		const qsDS = qs.get("data_source");
		const tsControl = $('.tl-data-source').tomselect;
		if ($('.tl-data-source.tomselected')) {
			tsControl.setValue(qsDS.split(','), true); // true = don't fire event (the updated filter gets submitted later)
		} else {
			tsControl.on('initialize', () => {
				tsControl.setValue(qsDS.split(','), true);
			});
		};
	}

	// classes (item types)
	const itemClasses = qs.get("class");
	if (!itemClasses) {
		// check all by default
		for (var elem of $$('.tl-item-class-dropdown input[type=checkbox]')) {
			elem.checked = true;
		}
	} else {
		// check only specified ones
		for (const cl of itemClasses.split(",")) {
			$(`.tl-item-class-dropdown input[type=checkbox][value="${cl}"]`).checked = true;
		}
	}

	if ($('#format-images'))
		$('#format-images').checked = qs.get('images') != "0";
	if ($('#format-videos'))
		$('#format-videos').checked = qs.get('videos') != "0";
	if ($('#include-attachments'))
		$('#include-attachments').checked = qs.get('attachments') != "0";

	// semantic search
	if ($('.semantic-text-search')) {
		$('.semantic-text-search').value = qs.get('semantic_text');
	}

	// text search for conversations
	if ($('#message-substring')) {
		$('#message-substring').value = qs.get('text') || '';
	}
}


// commonFilterSearchParams sets the filter inputs based on the inputted search parameters.
function commonFilterSearchParams(params) {
	params.repo = params.repo || tlz.openRepos[0].instance_id;
	params.sort = params.sort || $('.filter .date-sort')?.value;

	// timestamp
	// TODO: do we want to worry about not overwriting existing values passed in? caller could always set those fields afterward and overwrite us instead
	// if ($('.filter .date-input') && (!params.timestamp || (!params.start_timestamp && !params.end_timestamp))) {
	if ($('.filter .date-input')) {
		const dp = $('.filter .date-input').datepicker;
		const pickerDates = dp.selectedDates;

		if (params.sort == "NEAR") {
			// "NEAR" is not actually a real sort direction; just a hint to us
			delete params.sort;

			if (pickerDates.length == 1) {
				if (!dp.timepicker) {
					// choose noon (middle of the day), otherwise it's weird to see
					// results a day earlier that may be closer to 00:00 on the
					// desired day than items on the actual desired day
					pickerDates[0].setHours(12, 0, 0);
				}
				params.timestamp = pickerDates[0];
			}
		} else {
			// Note: It seems that rangeDateTo can have a value even if the user hasn't selected a "to" date explicitly
			const rangeEnabled = dp.rangeDateFrom || dp.rangeDateTo;

			if (rangeEnabled) {
				if (pickerDates.length == 1) {
					if (params.sort == "ASC") {
						if (!dp.timepicker) {
							pickerDates[0].setHours(0, 0, 0); // make whole start day inclusive
						}
						params.start_timestamp = pickerDates[0];
						delete params.end_timestamp;
					} else if (params.sort == "DESC") {
						if (!dp.timepicker) {
							pickerDates[0].setHours(23, 59, 59); // make whole end day inclusive
						}
						params.end_timestamp = pickerDates[0];
						delete params.start_timestamp;
					}
				} else if (pickerDates.length == 2) {
					if (!dp.timepicker) {
						// make both days inclusive
						pickerDates[0].setHours(0, 0, 0);
						pickerDates[1].setHours(23, 59, 59);
					}
					params.start_timestamp = pickerDates[0];
					params.end_timestamp = pickerDates[1];
				}
			} else if (dp.selectedDates.length == 1) {
				// copy dates so we can change them (if necessary) without stepping on each other
				// TODO: what if the timepicker IS enabled? we'd have to change the sort to NEAR; a range doesn't make sense
				const start = new Date(pickerDates[0]),
					end = new Date(pickerDates[0]);
				if (!dp.timepicker) {
					// make selected date inclusive
					start.setHours(0, 0, 0);
					end.setHours(23, 59, 59);
				}
				params.start_timestamp = start;
				params.end_timestamp = end;
			}
		}
	}

	// entity
	if (!params.entity_id && $('.filter .entity-input.tomselected')) {
		const ts = $('.filter .entity-input.tomselected').tomselect;
		const val = ts.getValue();
		if (Array.isArray(val) && val.length) {
			params.entity_id = val.map(Number);
		} else if (val?.length > 0) {
			params.entity_id = [Number(val)];
		}
	}

	// data sources
	if (!params.data_source && $('.filter .tl-data-source.tomselected')) {
		const ts = $('.filter .tl-data-source.tomselected').tomselect;
		const val = ts.getValue();
		if (Array.isArray(val) && val.length) {
			params.data_source = val;
		} else if (val?.length > 0) {
			params.data_source = [val];
		}
	}

	// item classifications
	const checkedItemClasses = $$('.filter .tl-item-class-dropdown input:checked');
	const allItemClasses = $$('.filter .tl-item-class-dropdown input')
	if (!params.classification && checkedItemClasses.length && checkedItemClasses.length < allItemClasses.length) {
		params.classification = [];
		for (const elem of checkedItemClasses) {
			params.classification.push(elem.value);
		}
	}

	// semantic text search
	if (!params.semantic_text && $('.semantic-text-search')) {
		params.semantic_text = $('.semantic-text-search').value;
	}
}





















// TODO: UNUSED.
// Converts an ArrayBuffer directly to base64, without any intermediate 'convert to string then
// use window.btoa' step. According to my tests, this appears to be a faster approach:
// http://jsperf.com/encoding-xhr-image-data/5
// UPDATED BENCHMARKS (Feb 2022): https://jsben.ch/wnaZC
/*
MIT LICENSE

Copyright 2011 Jon Leighton

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/
function base64ArrayBuffer(arrayBuffer) {
	var base64    = ''
	var encodings = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

	var bytes         = new Uint8Array(arrayBuffer)
	var byteLength    = bytes.byteLength
	var byteRemainder = byteLength % 3
	var mainLength    = byteLength - byteRemainder

	var a, b, c, d
	var chunk

	// Main loop deals with bytes in chunks of 3
	for (var i = 0; i < mainLength; i = i + 3) {
	  // Combine the three bytes into a single integer
	  chunk = (bytes[i] << 16) | (bytes[i + 1] << 8) | bytes[i + 2]

	  // Use bitmasks to extract 6-bit segments from the triplet
	  a = (chunk & 16515072) >> 18 // 16515072 = (2^6 - 1) << 18
	  b = (chunk & 258048)   >> 12 // 258048   = (2^6 - 1) << 12
	  c = (chunk & 4032)     >>  6 // 4032     = (2^6 - 1) << 6
	  d = chunk & 63               // 63       = 2^6 - 1

	  // Convert the raw binary segments to the appropriate ASCII encoding
	  base64 += encodings[a] + encodings[b] + encodings[c] + encodings[d]
	}

	// Deal with the remaining bytes and padding
	if (byteRemainder == 1) {
	  chunk = bytes[mainLength]

	  a = (chunk & 252) >> 2 // 252 = (2^6 - 1) << 2

	  // Set the 4 least significant bits to zero
	  b = (chunk & 3)   << 4 // 3   = 2^2 - 1

	  base64 += encodings[a] + encodings[b] + '=='
	} else if (byteRemainder == 2) {
	  chunk = (bytes[mainLength] << 8) | bytes[mainLength + 1]

	  a = (chunk & 64512) >> 10 // 64512 = (2^6 - 1) << 10
	  b = (chunk & 1008)  >>  4 // 1008  = (2^6 - 1) << 4

	  // Set the 2 least significant bits to zero
	  c = (chunk & 15)    <<  2 // 15    = 2^4 - 1

	  base64 += encodings[a] + encodings[b] + encodings[c] + '='
	}

	return base64
}



// libheif utility: draws HEIF onto a canvas.
// Inspired by https://github.com/strukturag/libheif especially
// the gh-pages branch: https://strukturag.github.io/libheif/
function drawHEIF(heicImage, canvasEl) {
	const ctx = canvasEl.getContext('2d');

	var w = heicImage.get_width();
	var h = heicImage.get_height();
	canvasEl.width = w;
	canvasEl.height = h;

	let imageData = ctx.createImageData(w, h);

	let pendingImageData;
	heicImage.display(imageData, function(displayImageData) {
		if (!displayImageData) {
			console.error("HEIC processing error; no image data");
			return;
		}
		if (window.requestAnimationFrame) {
			pendingImageData = displayImageData;
			window.requestAnimationFrame(function() {
				if (pendingImageData) {
					ctx.putImageData(pendingImageData, 0, 0);
					pendingImageData = null;
				}
			}.bind(this));
		} else {
			ctx.putImageData(displayImageData, 0, 0);
		}
	}.bind(this));
}
