// When .map-container or .loader-container elems are added to the DOM, observe them for scrolling into view.
const mutationObs = new MutationObserver(function(mutations) {
	for (const mut of mutations) {
		for (const node of mut.addedNodes) {
			if (!(node instanceof HTMLElement)) continue; // skip text/whitespace nodes
			
			if (node.matches('.map-container')) {
				tlz.mapIntersectionObs.observe(node);
			}
			$$('.map-container', node).forEach(el => {
				tlz.mapIntersectionObs.observe(el);
			});
		}
		// don't unobserve removedNodes, because the intersection observer
		// removes map containers from a set when they go out of view,
		// and if we unobserve it can't do that, causing a memory leak;
		// but we can fire mapMoved events for special-use maps that may
		// need to reset some state or initialization, such as the map on
		// the Demo Mode Settings page, which adds draw controls (it needs
		// to remove them when navigating away).
		for (const node of mut.removedNodes) {
			if (!(node instanceof HTMLElement)) continue; // skip text/whitespace nodes
			
			if (node.matches('.map-container')) {
				document.dispatchEvent(new CustomEvent("mapMoved", { detail: { previousElement: node } }));
			}
			$$('.map-container', node).forEach(el => {
				document.dispatchEvent(new CustomEvent("mapMoved", { detail: { previousElement: el } }));
			});
		}
	}
 });
 mutationObs.observe(document, {childList: true, subtree:true});


function addrBarPathToPagePath(userFriendlyPath) {
	if (userFriendlyPath == "/") {
		userFriendlyPath = "/dashboard"
	}
	return `/pages${userFriendlyPath}.html`;
}

function splitPathAndQueryString(uri) {
	const qsStart = uri.indexOf('?');
	if (qsStart > -1) {
		uri.substring(0, qsStart);
		return {
			path: uri.substring(0, qsStart),
			query: uri.substring(qsStart)
		};
	}
	return {path: uri};
}

function currentURI() {
	return window.location.pathname + window.location.search;
}

// navigateSPA changes the page. The argument should be the URI (not including scheme/host; i.e. just
// path and optional query string) to show in the address bar (the user-friendly URI). If omitted,
// or not a string (sometimes it is an event handler) it defaults to the path and query currently
// in the address bar.
async function navigateSPA(addrBarDestination) {
	$('#page-content').classList.add('opacity0');

	// when used as an event handler, the first argument may be an event object,
	// or we might call it without any argument; default to navigating to what the
	// address bar shows when a URI string isn't explicitly passed in
	if (typeof addrBarDestination !== 'string') {
		addrBarDestination = currentURI();
	}

	// redirect to setup (or away from it) accordingly
	if (!tlz.openRepos.length) {
		console.log("no repositories are open; redirecting to setup");
		addrBarDestination = "/setup";
	} else if (!await updateRepoOwners()) {
		// no owner yet, maybe the repo is still being set up
		console.log(`repository ${tlz.openRepos[0].instance_id} has no owner; redirecting to setup`);
		addrBarDestination = "/setup";
	} else if (addrBarDestination.startsWith("/setup")) {
		console.log("repository is already open and non-empty; redirecting to dashboard");
		addrBarDestination = "/";
	}
	
	// don't change history if we aren't changing the address bar state
	// (this happens on initial page load, or when the back button is used as the URL has already been changed)
	const skipPushState = addrBarDestination == currentURI();
	
	// split the user-facing URI into its path and query parts
	const addrBarDestinationParts = splitPathAndQueryString(addrBarDestination);

	// special cases: rewrite when path contains application data
	const matches = addrBarDestinationParts.path.match(/\/(items|entities|jobs)\/[\w-]+\/\d+$/);
	if (matches) {
		const rewriteTo = {"items": "/item", "entities": "/entity", "jobs": "/job"}
		addrBarDestinationParts.path = rewriteTo[matches[1]]
	}

	// craft the true destination (not shown to the user)
	const destPath = addrBarPathToPagePath(addrBarDestinationParts.path);
	const destination = destPath + (addrBarDestinationParts.query || "");

	console.log("NAVIGATING:", destination, addrBarDestinationParts);

	// show a loading indicator if things are going slow
	let slowLoadingHandle;
	if (!$('#app-loader')) {
		slowLoadingHandle = setTimeout(function() {
			const span = document.createElement('span');
			span.classList.add('slow-loader');
			$('#page-content').insertAdjacentElement('beforebegin', span);
		}, 1000);
	}

	// immediately perform the request, but don't start changing the page until it has faded out
	// TODO: if URL not found or something, replaceState(null, null, "/") maybe?
	const promise = fetch(destination).then((response) => response.text());

	// wait for page to finish fading out before
	setTimeout(async function() {
		promise.then(async (data) => {
			if (tlz.map) {
				tlz.map.tl_containers = new Map();
				tlz.map.tl_clear();
			}
			for (const dateInputEl of $$('.date-input')) {
				// it seems like a good idea to clean up our AirDatepickers, but
				// I haven't confirmed whether this is truly necessary
				dateInputEl.datepicker.destroy();
			}

			// run any code needed to help the page unload
			if (tlz.currentPageController?.unload) {
				// TODO: await?
				tlz.currentPageController.unload();
			}

			// update URL bar and history
			if (!skipPushState) {
				history.pushState(null, null, addrBarDestination);
			}

			// replace page content
			$('#page-content').innerHTML = data;

			// adjust page title
			const newTitleEl = $('body title');
			if (newTitleEl) {
				$('head title').innerText = newTitleEl.innerText;
				newTitleEl.remove();
			}

			// set up the page, and store a reference to the current page's
			// controller, since it will be used later like when unloading it
			tlz.currentPageController = tlz.pageControllers[destPath];
			if (tlz.currentPageController?.load) {
				await tlz.currentPageController.load();
			}

			// Render data source filter inputs (we don't use forEach here because it does not await async functions!)
			for (const e of $$('.filter select.tl-data-source')) {
				await newDataSourceSelect(e);
			};

			// Render item classification filter dropdowns
			$$('.filter .tl-item-class-dropdown').forEach(e => {
				renderFilterDropdown(e, "Types", 'item_classes');
			});

			await queryStringToFilter();

			// set up the page, and store a reference to the current page's
			// controller, since it will be used later like when unloading it
			if (tlz.currentPageController?.render) {
				await tlz.currentPageController.render();
			}

			// activate custom/Bootstrap tooltips on the page
			const tooltipList = [...$$('[data-bs-toggle="tooltip"]')].map(tooltipTriggerEl => new bootstrap.Tooltip(tooltipTriggerEl))


			// hide any loading indicator (or prevent it from appearing in the first place)
			if (slowLoadingHandle) {
				clearTimeout(slowLoadingHandle);
				$('.slow-loader')?.remove();
			}

			// fade the content in, but wait a little bit for the page to have a chance to render
			setTimeout(function() {
				$('#page-content').classList.remove('opacity0');
			}, 250);

			// if the full page app loader is still showing (initial page load), fade it out gracefully
			// (these timings are estimates; maybe advanced browser APIs could help us know when the page
			// is done painting, but sounds like a lot of work)
			if ($('#app-loader') && !$('#app-loader').classList.contains('fade-out')) {
				// first start to fade out the loader itself (fading out only its container looks weird)
				setTimeout(function() {
					$('#app-loader .app-loader').classList.add('fade-out');

					// then fade out its backdrop/container
					setTimeout(function() {
						$('#app-loader').classList.add('fade-out');

						// once index has initially loaded, app loader no longer needed
						setTimeout(function() {
							$('#app-loader').remove();
						}, 1000);
					}, 100);
				}, 100);
			}

		});

	}, 250);
}


// when links to pages within the app are clicked, fake-navigate
on('click', '[href^="/"]:not([download])', async e => {
	e.preventDefault();
	const destination = e.target.closest(':not(use)[href]').getAttribute('href'); // can't use .href because that returns a fully-qualified URL, which actually breaks in Wails dev; and we only accept path+query
	await navigateSPA(destination);
	return false;
});

// this is for filter changes
on('click', `.filter [href^="?"], .pagination [href^="?"]`, async event => {
	event.preventDefault();
	
	// update URL bar so the filter will read the updated page number
	const destination = event.target.closest(':not(use)[href]').getAttribute('href'); // can't use .href because that returns a fully-qualified URL, which actually breaks in Wails dev; and we only accept path+query
	history.pushState(null, null, destination);

	updateFilterResults();

	return false;
});


async function updateRepoOwners() {
	let anyUpdated = false;
	for (const repo of tlz.openRepos) {
		if (repo.owner) {
			continue; // avoid re-checking on _every single page load_
		}
		if (await app.RepositoryIsEmpty(repo.instance_id)) {
			return false;
		} else {
			repo.owner = await app.GetEntity(repo.instance_id, 1);
			anyUpdated = true;
		}
	}
	if (anyUpdated) {
		store('open_repos', tlz.openRepos);

		const repoOwner = tlz.openRepos[0].owner;
		
		let birthPlace = "";
		if (repoOwner.attributes) {
			for (const attr of repoOwner.attributes) {
				if (attr.name == "birth_place") {
					birthPlace = attr.value;
					break;
				}
			}
		}

		$('#repo-owner-name').innerText = repoOwner.name;
		$('#repo-owner-title').innerText = birthPlace;
		$('#repo-owner-avatar').innerHTML = avatar(false, repoOwner, "avatar-sm");
		$('header .profile-link').href = `/entities/${tlz.openRepos[0].instance_id}/${repoOwner.id}`;
	}
	return true;
}

async function updateItemClasses() {
	const clArray = await app.ItemClassifications(tlz.openRepos[0].instance_id);
	const classes = {};
	for (const cl of clArray) {
		classes[cl.name] = cl;
	}
	store('item_classes', classes);
}


async function initialize() {
	// classifications are stored in the database, so get open repos first and attach owner info
	tlz.openRepos = await app.OpenRepositories();

	await updateRepoOwners();

	// if there's an open repo, load classifications
	if (tlz.openRepos.length && !load('item_classes')) {
		await updateItemClasses();
	}

	// load app settings
	tlz.settings = await app.GetSettings();

	// now that settings are loaded, initialize the map
	initMapSingleton();

	// perform initial page load
	if (document.readyState === 'loading') {
		window.addEventListener('DOMContentLoaded', navigateSPA);
	} else {
		navigateSPA();
	}

	// when back button is pressed, also do SPA nav (TODO: test this works)
	window.addEventListener('popstate', navigateSPA);
}

initialize();
