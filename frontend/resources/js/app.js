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

// AJQuery: https://github.com/coolaj86/ajquery.js (modified slightly by me)
function $(sel, el) { return typeof sel === 'string' ? ((typeof el === 'string' ? $(el) : el) || document).querySelector(sel) : sel; }
function $$(sel, el) { return (el || document).querySelectorAll(sel); }









async function get(endpoint, data, signal) {
	if (typeof data == 'object') {
		const qs = new URLSearchParams();
		for (const key in data) {
			qs.append(key, data[key]);
		}
		endpoint += '?'+qs.toString();
	}
	return myFetch(signal, endpoint, undefined, 'GET');
}

async function post(endpoint, data, method = 'POST') {
	return myFetch(null, endpoint, data, method);
}

async function abortablePost(signal, endpoint, data, method = 'POST') {
	return myFetch(signal, endpoint, data, method)
}

async function myFetch(signal, endpoint, data, method) {
	// disable form inputs if no modal is visible
	const formFields = $$('form input, form button, form textarea');
	if ($('#modal-preview')?.offsetParent === null) {
		formFields.forEach((elem) => { elem.disabled = true; });
	}

	let headers = {};
	if (data) {
		data = JSON.stringify(data);
		headers['Content-Type'] = 'application/json'
	}

	return fetch(endpoint, {
		method: method,
		headers: headers,
		body: data,
		signal: signal
	}).then(async (response) => {
		if (response.ok) {
			if (response.headers.get('Content-Type') == 'application/json')
				return response.json();
			else
				return response;
		}

		if (response.headers.get('Content-Type') == 'application/json') {
			throw {
				response: response,
				error: await response.json()
			}
		}

		throw {response: response};
	}).finally(function() {
		// re-enable form inputs
		if ($('#modal-preview')?.offsetParent === null) {
			formFields.forEach((elem) => { elem.disabled = false; });
		}
	});
}




const app = {
	AddEntity(repo_id, entity) {
		return post("/api/add-entity", { repo_id, entity });
	},
	CancelJobs(repo_id, job_ids) {
		return post("/api/cancel-jobs", { repo_id, job_ids });
	},
	ChangeSettings(settings) {
		return post("/api/change-settings", settings);
	},
	CloseRepository(repo_id) {
		return post("/api/close-repository", repo_id);
	},
	DataSources() {
		return get("/api/data-sources");
	},
	FileStat(filename) {
		return post("/api/file-stat", filename);
	},
	FileListing(path, params) {
		params.path = path;
		return post("/api/file-listing", params);
	},
	FileSelectorRoots() {
		return get("/api/file-selector-roots");
	},
	GetEntity(repo_id, entity_id) {
		return post("/api/get-entity", { repo_id, entity_id });
	},
	Import(params) {
		return post("/api/import", params);
	},
	ItemClassifications(repoID) {
		return post("/api/item-classifications", repoID);
	},
	Jobs(params) {
		return post("/api/jobs", params, "QUERY");
	},
	LoadConversation(params) {
		return post("/api/conversation", params);
	},
	ChartStats(name, repo_id, data) {
		return get("/api/charts", { name, repo_id, ...data });
	},
	LoadRecentConversations(params) {
		return post("/api/recent-conversations", params);
	},
	MergeEntities(repo_id, base_entity_id, other_entity_ids) {
		return post("/api/merge-entities", { repo_id, base_entity_id, other_entity_ids });
	},
	NextGraph(repo_id, job_id) {
		return get("/api/next-graph", { repo_id, job_id });
	},
	OpenRepository(repo_path, create) {
		return post("/api/open-repository", { repo_path, create });
	},
	OpenRepositories() {
		return get("/api/open-repositories");
	},
	PauseJob(repo_id, job_id) {
		return post("/api/pause-job", { repo_id, job_id });
	},
	PlanImport(params) {
		return post("/api/plan-import", params);
	},
	Recognize(filename) {
		return post("/api/recognize", { filename });
	},
	RepositoryIsEmpty(repoID) {
		 return post("/api/repository-empty", repoID);
	},
	SearchItems(params) {
		return post("/api/search-items", params);
	},
	SearchEntities(params) {
		return post("/api/search-entities", params);
	},
	GetSettings() {
		return get("/api/settings");
	},
	SubmitGraph(repo_id, job_id, graph, skip) {
		return post("/api/submit-graph", { repo_id, job_id, graph, skip });
	},
	StartJob(repo_id, job_id, start_over) {
		return post("/api/start-job", { repo_id, job_id, start_over });
	},
	UnpauseJob(repo_id, job_id) {
		return post("/api/unpause-job", { repo_id, job_id });
	},
};






function store(key, val) {
	if (typeof val === 'object') {
		val = JSON.stringify(val);
	}
	// TODO: I'm not set on whether to use sessionStorage or localStorage.
	sessionStorage.setItem(key, val);
}

function load(key) {
	let val = sessionStorage.getItem(key);
	if (typeof val === 'string') {
		try {
			val = JSON.parse(val);
		}
		catch {
			// just leave as string
		}
	}
	return val;
}


function on(eventName, elemSelector, handler, capture) {
	let events = [eventName];
	if (eventName.indexOf(',') >= 0 || eventName.indexOf(' ') >= 0) {
		eventName = eventName.replace(/,/, ' ');
		events = eventName.split(' ');
	}

	events.forEach(eventName => {
		eventName = eventName.trim()
		if (!eventName) return;

		// from youmightnotneedjquery.com
		document.addEventListener(eventName, function (e) {
			// loop parent nodes from the target to the delegation node
			// TODO: maybe we should also attach the closest element matching the selector, since the event bubbles up...
			for (let target = e.target; target && target != this; target = target.parentNode) {
				if (NodeList.prototype.isPrototypeOf(elemSelector)) {
					for (el of elemSelector) {
						if (el == target) {
							handler.call(target, e);
							return;
						}
					}
				} else if (!elemSelector || (target && target.matches(elemSelector))) {
					handler.call(target, e);
					return;
				}
			}
		}, capture); // I find capture=true helpful when using :not() selectors to exclude one elem of the node tree
	});
}

function trigger(el, eventType) {
	if (typeof el === 'string') {
		el = $(el); // assume it was a selector, for convenience
	}

	// from youmightnotneedjquery.com
	if (typeof eventType === 'string' && typeof el[eventType] === 'function') {
		el[eventType]();
	} else {
		const event =
			typeof eventType === 'string'
				? new Event(eventType, { bubbles: true, cancelable: true })
				: eventType;
		el.dispatchEvent(event);
	}
}

// cloneTemplate does a deep clone of the <template> tag selected by tplSelector.
function cloneTemplate(tplSelector) {
	// Ohhhhhh wow, we need to use firstElementChild when cloning the content of a template tag (!!!!):
	// https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
	// I spent way too long on this.
	const elem = $(tplSelector);
	if (!elem) return;
	return elem.content.firstElementChild.cloneNode(true);
	// return document.importNode(elem.content, true);
}

function fadeOut(sel, duration) {
	const elem = $(sel);
	if (!duration) {
		duration = 250;
	}
	elem.style.transition = `opacity ${duration}ms`;
	elem.style.opacity = 0;
	setTimeout(function () {
		elem.style.display = 'none';
	}, duration);
}

function fadeIn(sel, duration) {
	const elem = $(sel);
	if (!duration) {
		duration = 250;
	}
	elem.style.opacity = 0;
	elem.style.transition = `opacity ${duration}ms`;
	elem.style.display = 'block';

	// For the same unknown reason we have to delay when creating the "show"
	// animation for toasts/alerts/notifications, we delay here too
	setTimeout(function () {
		elem.style.opacity = 1;
	}, 10);
}

function replace(outSel, inSel, duration) {
	if (!duration) {
		duration = 500;
	}
	const halfDuration = duration / 2;
	fadeOut(outSel, halfDuration);
	setTimeout(function () {
		fadeIn(inSel, halfDuration);
	}, halfDuration)
}

function notify(params) {
	const icons = {
		success: `<svg xmlns="http://www.w3.org/2000/svg" class="icon alert-icon" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round"><path stroke="none" d="M0 0h24v24H0z" fill="none" /><path d="M5 12l5 5l10 -10" /></svg>`,
		info: `<svg xmlns="http://www.w3.org/2000/svg" class="icon alert-icon" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round"><path stroke="none" d="M0 0h24v24H0z" fill="none"/><circle cx="12" cy="12" r="9" /><line x1="12" y1="8" x2="12.01" y2="8" /><polyline points="11 12 12 12 12 16 13 16" /></svg>`,
		warning: `<svg xmlns="http://www.w3.org/2000/svg" class="icon alert-icon" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round"><path stroke="none" d="M0 0h24v24H0z" fill="none"/><path d="M12 9v2m0 4v.01" /><path d="M5 19h14a2 2 0 0 0 1.84 -2.75l-7.1 -12.25a2 2 0 0 0 -3.5 0l-7.1 12.25a2 2 0 0 0 1.75 2.75" /></svg>`,
		danger: `<svg xmlns="http://www.w3.org/2000/svg" class="icon alert-icon" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round"><path stroke="none" d="M0 0h24v24H0z" fill="none"/><circle cx="12" cy="12" r="9" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" /></svg>`
	};

	const notifElem = cloneTemplate('#tpl-alert');

	notifElem.classList.add(`alert-${params.type}`);
	if (params.requireDismiss) {
		notifElem.classList.add(`alert-dismissable`);
	}

	if (params.title) {
		$('.alert-heading', notifElem).innerText = params.title;
	} else {
		$('.alert-heading', notifElem).remove();
	}
	if (params.message) {
		$('.alert-description', notifElem).innerText = params.message;
	} else {
		$('.alert-description', notifElem).remove();
	}
	$('.alert-icon', notifElem).innerHTML = icons[params.type];

	$('#alert-container').append(notifElem);

	if (!params.requireDismiss) {
		const duration = params.duration || 5000;

		// For some reason, we have to wait a brief moment before applying the override to the width property,
		// even though the element has already been appended to the DOM. (This is also how SweetAlert2 works.)
		setTimeout(function () {
			$('.alert-timer-progress-bar', notifElem).style.transition = `width ${duration}ms linear`;
			$('.alert-timer-progress-bar', notifElem).style.width = '0%';

			// expire notification automatically
			setTimeout(function () {
				notifElem.classList.remove('alert-show');
				notifElem.classList.add('alert-hide');
				setTimeout(function () {
					notifElem.remove();
				}, 250); // should be at least the same duration as the hide animation
			}, duration);
		}, 10);
	}
}

// isVisible returns true if elem (an element or selector) is visible.
function isVisible(elem) {
	if (typeof elem === 'string') {
		elem = $(elem);
	}
	return elem.offsetParent !== null;
}

// queryParam returns the named query string parameter's value(s).
function queryParam(name) {
	const urlSearchParams = new URLSearchParams(window.location.search);
	const params = Object.fromEntries(urlSearchParams.entries());
	return params[name];
}

function updatedQueryString(k, v) {
	const urlSearchParams = new URLSearchParams(window.location.search);
	urlSearchParams.set(k, v);
	return urlSearchParams.toString();
}

// baseFilename returns the filename at the end of the path.
// It counts the number of / and \ to determine which path separator to use. (TODO: this might not be necessary if we always use / and it works...)
function baseFilename(filepath) {
	if (!filepath) return "";
	const numSlashes = (filepath.match(/\//g) || []).length;
	const numBackslashes = (filepath.match(/\\/g) || []).length;
	const lastPathSep = filepath.lastIndexOf(numSlashes > numBackslashes ? '/' : '\\');
	return filepath.substring(lastPathSep + 1);
}


async function openRepository(repoPath, createIfNotExist) {
	const repo = await app.OpenRepository(repoPath, createIfNotExist);
	tlz.openRepos.push(repo);
	await updateRepoOwners();
	await updateItemClasses();
	return repo;
}

async function closeRepository() {
	await app.CloseRepository(tlz.openRepos[0].instance_id);
	tlz.openRepos = await app.OpenRepositories();
	store('open_repos', tlz.openRepos);
	if (!tlz.openRepos.length) {
		await navigateSPA('/setup', true);
		notify({
			type: 'success',
			title: "Logged out",
			message: "Timeline closed"
		});
		// navigate('/setup', {
		// 	type: 'success',
		// 	title: "Logged out",
		// 	message: "Timeline closed"
		// });
	}
}
