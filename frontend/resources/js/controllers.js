// render functions should be synchronous (async should be awaited) so that the page load isn't janky.
tlz.pageControllers = {
	"/pages/conversations.html": {
		async load() {
			await conversationsPageMain();
		},
		async render() {
			await renderConversationsPage();
		}
	},

	"/pages/dashboard.html": {
		load() {
			$('.navbar').classList.add('navbar-overlap');
			$('.navbar').dataset.bsTheme = "dark";

			const ctaButton = $('.nav-item .btn-outline-primary');
			ctaButton.classList.remove('btn-outline-primary');
			ctaButton.classList.add('btn-outline-light');
		},
		async render() {
			await renderDashboard();
		},
		unload() {
			$('.navbar').classList.remove('navbar-overlap');
			delete($('.navbar').dataset.bsTheme);

			const ctaButton = $('.nav-item .btn-outline-light');
			ctaButton.classList.remove('btn-outline-light');
			ctaButton.classList.add('btn-outline-primary');
		}

		// TODO: build a real dashboard, you fool
		// async render() {
		// 	async function loadAllGroups2(maxGroups = 10) {
		// 		const limitPerBatch = 100;

		// 		async function loadBatch(params) {
		// 			const results = await app.SearchItems(params);
		// 			// console.log("BATCH:", results.items);
		// 			return results.items;
		// 		}

		// 		let items = [];
		// 		const start = DateTime.now();

		// 		do {
		// 			const batch = await loadBatch({
		// 				related: 1,
		// 				end_timestamp: items?.[items.length-1]?.timestamp,
		// 				order_by: "stored",
		// 				limit: limitPerBatch
		// 			});
		// 			items = items.concat(batch);
		// 			const groups = timelineGroups(items);

		// 			if (groups.length > maxGroups || batch?.length < limitPerBatch) {
		// 				return groups;
		// 			}
		// 		} while (-start.diffNow().as('seconds') < 5 && items.length < 500);

		// 		return timelineGroups(items);
		// 	}

			
		// 	const groups = await loadAllGroups2();

		// 	console.log("ALL GROUPS:", groups)

		// 	// $('.filter-results').replaceChildren(renderTimelineGroups(groups));

		// 	options = {};

		// 	groups.forEach((group, i) => {
		// 		if (!group.length) return;
				
		// 		const display = itemMiniDisplay(group, options);
				
		// 		if (display.element) {
		// 			// display.element.style.flex = '1';
		// 			// display.element.style.minWidth = '400px';
		// 			$('.timeline-container-grid').append(display.element);
		// 		}
		// 	});
		// }
	},

	"/pages/setup.html": {
		async load() {
			// fill out year picker
			const startYear = new Date().getFullYear() - 10;
			for (let year = startYear; year > startYear - 110; year--) {
				$('select[name=dob-year]').innerHTML += `<option value="${year}">${year}</option>`;
			}

			for (let i = 0; i < tlz.openRepos.length; i++) {
				if (await app.RepositoryIsEmpty(tlz.openRepos[i].instance_id)) {
					notify({
						type: "info",
						title: "Timeline is empty",
						message: "It needs your profile"
					});
					emptyRepo = tlz.openRepos[i];
					advanceToPersonForm();
					return;
				}
			}
		}
	},

	"/pages/settings.html": {
		async load() {
			changeSettingsTab(window.location.hash || "#general");

			// next, load settings and populate fields

			tlz.settings = await app.GetSettings();
			console.log("SETTINGS:", tlz.settings);

			// general
			$('#mapbox-api-key').value = tlz.settings?.application?.mapbox_api_key || "";

			// demo mode (obfuscation)
			const obfs = tlz.settings?.application?.obfuscation;
			$('#demo-mode-enabled').checked = obfs?.enabled == true;
			$('#data-file-names').checked = obfs?.data_files == true;

			// advanced
			$('#website-dir').value = tlz.settings?.application?.website_dir || "";
		}
	},

	"/pages/entities.html": {
		async render() {
			await entitiesPageMain();
		}
	},

	"/pages/entity.html": {
		async render() {
			await entityPageMain();
		}
	},
	
	"/pages/gallery.html": {
		load() {
			$('body').classList.add('layout-fluid');
			$('header').classList.add('sticky-top', 'translucent');
			
			$('#content-column').prepend(cloneTemplate('#tpl-pagination'));
			$('#content-column').append(cloneTemplate('#tpl-pagination'));
			
			newEntitySelect($('.entity-input'), 1);
			
			$('.filter').prepend(newDatePicker({
				passthru: {
					range: true,
				},
				time: false,
				timeToggle: true,
				proximity: true,
				vertical: true
			}));
			$('.filter .date-sort').value = "DESC"; // makes sense in a gallery view, to start with most recent
		},
		async render() {
			await galleryPageMain();
		},
		unload() {
			$('body').classList.remove('layout-fluid');
			$('header').classList.remove('sticky-top', 'translucent');
		}
	},

	"/pages/import.html": {
		load() {
			$('#timeframe').append(newDatePicker({
				passthru: {
					range: true
				},
				sort: false,
				timeToggle: true,
				noApply: true
			}));
		}
	},

	"/pages/input.html": {
		async render() {
			inputPageMain();
		},
	},

	"/pages/item.html": {
		async render() {
			await itemPageMain();
		}
	},

	"/pages/items.html": {
		load() {
			$('.pagination-container').append(cloneTemplate('#tpl-pagination'));

			$('.tl-date-picker').append(newDatePicker({
				passThru: {
					range: true
				},
				rangeToggle: true,
				time: false,
				timeToggle: true,
				proximity: true,
				vertical: true
			}));

			newEntitySelect($('.entity-input'), 5, true);
		},
		async render() {
			await itemsMain();
		}
	},

	"/pages/map.html": {
		load() {
			$('body').classList.add('layout-fluid');

			$('.tl-date-picker').prepend(newDatePicker({
				passthru: {
					range: false
				},
				rangeToggle: true,
				time: false,
				timeToggle: true,
				proximity: false,
				vertical: false
			}));
			newEntitySelect($('#select-person'), 1, true);
		},
		async render() {
			await loadAndRenderMapData();
		},
		unload() {
			$('body').classList.remove('layout-fluid');
		}
	},

	"/pages/timeline.html": {
		load() {
			newEntitySelect($('.entity-input'), 1);

			$('.filter').prepend(newDatePicker({
				passthru: {
					range: false
				},
				rangeToggle: true,
				time: false,
				timeToggle: true,
				proximity: false,
				vertical: true
			}));
		},
		async render() {
			// figure out the most recent day with content, and use that as the default/initial timeline
			const presearchParams = {sort: "DESC"};
			commonFilterSearchParams(presearchParams);
			if (!presearchParams.start_timestamp && !presearchParams.end_timestamp && !presearchParams.timestamp) {
				const presearch = await app.SearchItems({
					...presearchParams,
					limit: 1,
					flat: true
				});
				if (presearch?.items?.length) {
					$('.filter .date-input').datepicker.selectDate(presearch.items[0].timestamp);
				}
			}

			const groups = await loadAllGroups();

			console.log("ALL GROUPS:", groups)

			$('.filter-results').replaceChildren(renderTimelineGroups(groups));
		}
	},

	"/pages/job.html": {
		load() {
			$('.navbar').classList.add('navbar-overlap');
		},
		async render() {
			jobPageMain();
		},
		unload() {
			$('.navbar').classList.remove('navbar-overlap');
			clearInterval(jobThroughputInterval); // stop trying to update the chart
		}
	},
}