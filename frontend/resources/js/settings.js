// Changes the settings tab that is active, and settings page content.
// The target should include the hash (#) prefix and should correspond
// with the href and div IDs.
function changeSettingsTab(target) {
	$$(`.settings-nav .active`).forEach(elem => {
		elem.classList.remove('active');
	});
	$(`.settings-nav a.list-group-item[href="${target}"]`).classList.add('active');

	$$(`.settings-page:not(${target}, .d-none)`).forEach(elem => {
		elem.classList.add('d-none');
	});
	$(target).classList.remove('d-none');

	document.title = `Settings - ${$('h2', target).textContent}`;

	// FIXME: For some reason this causes a duplicate history entry to be added, but after navigating again it disappears
	window.history.pushState(null, null, target);
}

// when the map is moved into the location picker, set up its interactive draw features;
// and when it is removed from the location picker, reset its configuration
document.addEventListener('mapMoved', async e => {
	if (e.detail?.currentElement?.matches('#secret-location-picker .map-container'))
	{
		// map inserted

		const modes = MapboxDrawGeodesic.enable(MapboxDraw.modes);
		const draw = new MapboxDraw({
			displayControlsDefault: false,
			controls: {
				trash: true
			},
			modes
		});

		tlz.mapDrawBar = new extendDrawBar({
			draw: draw,
			buttons: [
				{
					on: 'click',
					action: function() {
						draw.changeMode('draw_circle');
					},
					classes: ['text-black', 'add-circle'],
					html: `
						<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
							stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
							class="icon icon-tabler icons-tabler-outline icon-tabler-circle-plus-2">
							<path stroke="none" d="M0 0h24v24H0z" fill="none" />
							<path d="M20.985 12.522a9 9 0 1 0 -8.475 8.464" />
							<path d="M16 19h6" />
							<path d="M19 16v6" />
						</svg>
					`
				}
			]
		});

		// When a circle is created, add its corresponding element to the list
		tlz.map.on('draw.create', settingsMapLocObfuscationDrawCreate);

		// When a circle is changed, update its corresponding element in the list
		tlz.map.on('draw.update', settingsMapLocObfuscationUpdate);

		// When a circle is deleted, remove its corresponding element from the list
		tlz.map.on('draw.delete', settingsMapLocObfuscationDelete);

		// When a circle is selected, highlight its corresponding element in the list
		tlz.map.on('draw.selectionchange', settingsMapLocObfuscationSelChange);

		// addControl takes an optional second argument to set the position of the control.
		// If no position is specified the control defaults to `top-right`. See the docs
		// for more details: https://docs.mapbox.com/mapbox-gl-js/api/#map#addcontrol
		tlz.map.addControl(tlz.mapDrawBar);

		// next, load settings and populate fields

		const settings = await app.GetSettings();
		console.log("SETTINGS:", settings);

		// general
		$('#mapbox-api-key').value = settings?.application?.mapbox_api_key || "";

		// demo mode (obfuscation)
		const obfs = settings?.application?.obfuscation;
		$('#demo-mode-enabled').checked = obfs?.enabled == true;
		$('#data-file-names').checked = obfs?.data_files == true;
		if (obfs?.locations) {
			for (const loc of settings.application.obfuscation.locations) {
				const circle = MapboxDrawGeodesic.createCircle([loc.lon, loc.lat], loc.radius_meters/1000);
				circle.properties.name = loc.description;
				const featureIDs = draw.add(circle);
				settingsMapLocObfuscationDrawCreate({features: [circle]});			
			}
		}

		// advanced
		$('#website-dir').value = settings?.application?.website_dir || "";
	}
	// when the element is no longer in the DOM, we can't use descendency selectors to match it
	else if (e.detail?.previousElement?.matches('.secret-location-picker.map-container'))
	{
		// map removed
		if (tlz.mapDrawBar) {
			tlz.map.removeControl(tlz.mapDrawBar);
			delete(tlz.mapDrawBar);
			tlz.map.off('draw.create', settingsMapLocObfuscationDrawCreate);
			tlz.map.off('draw.update', settingsMapLocObfuscationUpdate);
			tlz.map.off('draw.delete', settingsMapLocObfuscationDelete);
			tlz.map.off('draw.selectionchange', settingsMapLocObfuscationSelChange);
		}
	}
});


function settingsMapLocObfuscationDrawCreate(e) {
	const geojson = e.features[0];

	const elem = cloneTemplate('#tpl-secret-location');
	elem.id = "secret-location-"+geojson.id;

	if (MapboxDrawGeodesic.isCircle(geojson)) {
		elem._center = MapboxDrawGeodesic.getCircleCenter(geojson); // [lon, lat]
		elem._radius = MapboxDrawGeodesic.getCircleRadius(geojson); // kilometers
		$('.secret-location-coords', elem).innerText = `${elem._center[1].toFixed(4)}, ${elem._center[0].toFixed(4)}`;
		$('.secret-location-radius', elem).innerText = `${elem._radius.toFixed(2)} km`;
		$('.secret-location-name', elem).value = geojson.properties.name || "";
	}

	$('#secret-location-list').append(elem);
}

function settingsMapLocObfuscationUpdate(e) {
	const geojson = e.features[0];

	// this event fires if a circle point (the center or on the circumference, like while editing
	// the feature) is selected when the feature is deleted, but the coordinates are empty; avoid crashing
	if (MapboxDrawGeodesic.isCircle(geojson) && geojson.geometry.coordinates.length > 0) {
		const elem = $('#secret-location-'+geojson.id);
		elem._center = MapboxDrawGeodesic.getCircleCenter(geojson); // [lon, lat]
		elem._radius = MapboxDrawGeodesic.getCircleRadius(geojson); // kilometers
		$('.secret-location-coords', elem).innerText = `${elem._center[1].toFixed(4)}, ${elem._center[0].toFixed(4)}`;
		$('.secret-location-radius', elem).innerText = `${elem._radius.toFixed(2)} km`;
	}
}

function settingsMapLocObfuscationDelete(e) {
	const geojson = e.features[0];
	if (MapboxDrawGeodesic.isCircle(geojson)) {
		$('#secret-location-'+geojson.id).remove();
	}
}

function settingsMapLocObfuscationSelChange(e) {
	$$('.secret-location.selected-location').forEach(elem => {
		elem.classList.remove('selected-location');
	});
	e.features.forEach(feature => {
		$('#secret-location-'+feature.id).classList.add('selected-location');
	});
}

// allows us to extend the control bar for Mapbox-gl-draw with our own buttons.
class extendDrawBar {
	constructor(opt) {
		let ctrl = this;
		ctrl.draw = opt.draw;
		ctrl.buttons = opt.buttons || [];
		ctrl.onAddOrig = opt.draw.onAdd;
		ctrl.onRemoveOrig = opt.draw.onRemove;
	}
	onAdd(map) {
		let ctrl = this;
		ctrl.map = map;
		ctrl.elContainer = ctrl.onAddOrig(map);
		ctrl.buttons.forEach((b) => {
			ctrl.addButton(b);
		});
		return ctrl.elContainer;
	}
	onRemove(map) {
		let ctrl = this;
		ctrl.buttons.forEach((b) => {
			ctrl.removeButton(b);
		});
		ctrl.onRemoveOrig(map);
	}
	addButton(opt) {
		let ctrl = this;
		var elButton = document.createElement('button');
		elButton.className = 'mapbox-gl-draw_ctrl-draw-btn';
		if (opt.classes instanceof Array) {
			opt.classes.forEach((c) => {
				elButton.classList.add(c);
			});
		}
		elButton.addEventListener(opt.on, opt.action);
		elButton.innerHTML = opt.html || "";
		ctrl.elContainer.prepend(elButton);
		opt.elButton = elButton;
	}
	removeButton(opt) {
		opt.elButton.removeEventListener(opt.on, opt.action);
		opt.elButton.remove();
	}
}


// change tabs
on('click', '.settings-nav a', e => {
	// prevent popstate (yes, really: apparently since pushState is used elsewhere)
	// from triggering and invoking navigateSPA -- I don't fully understand this;
	// I guess popstate is the "default" action when a link is clicked in that case
	e.preventDefault();

	// get the href literally, not expanded as e.target.href is
	changeSettingsTab(e.target.getAttribute('href'));

	return false;
});

// save settings!
on('click', '#submit-settings', async event => {
	// build array of obfuscated locations
	const locations = [];
	$$('.secret-location').forEach(el => {
		locations.push({
			description: $('.secret-location-name', el).value,
			lat: el._center[1],
			lon: el._center[0],
			radius: el._radius*1000
		});
	});

	const mutatedSettings = {
		application: {
			"app.obfuscation.enabled":  $('#demo-mode-enabled').checked,
			"app.obfuscation.data_files": $('#data-file-names').checked,
			"app.obfuscation.locations": locations,
			"app.mapbox_api_key": $('#mapbox-api-key').value,
			"app.website_dir": $('#website-dir').value
		}
	};

	const newSettings = await app.ChangeSettings(mutatedSettings);
	console.log("SAVE SETTINGS RESULT:", newSettings);
});