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

package tlzapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

func (a *App) registerCommands() {
	// TODO: register flags with flag package... and command help... these will probably need to know the payload structure...
	// TODO: make endpoint URIs consistent with App methods and frontend function names
	a.commands = map[string]Endpoint{
		"add-entity": {
			Handler: a.server.handleAddEntity,
			Method:  http.MethodPost,
			Payload: addEntityPayload{},
			Help:    "Creates a new entity.",
		},
		"build-info": {
			Handler: a.server.handleBuildInfo,
			Method:  http.MethodGet,
			Help:    "Displays information about this build.",
		},
		"cancel-jobs": {
			Handler: a.server.handleCancelJobs,
			Method:  http.MethodPost,
			Payload: jobsPayload{},
			Help:    "Cancels active jobs.",
		},
		"change-settings": {
			Handler: a.server.handleChangeSettings,
			Method:  http.MethodPost,
			Payload: changeSettingsPayload{},
			Help:    "Changes settings.",
		},
		"close-repository": {
			Handler: a.server.handleCloseRepo,
			Method:  http.MethodPost,
			Payload: "",
			Help:    "Close a timeline repository.",
		},
		"conversation": {
			Handler: a.server.handleConversation,
			Method:  http.MethodPost,
			Payload: timeline.ItemSearchParams{},
			Help:    "Loads a conversation.",
		},
		"data-sources": {
			Handler: a.server.handleGetDataSources,
			Method:  http.MethodGet,
			Help:    "Returns the supported data sources.",
		},
		"delete-items": {
			Handler: a.server.handleDeleteItems,
			Method:  http.MethodDelete,
			Payload: deleteItemsPayload{},
			Help:    "Deletes items from a timeline.",
		},
		"file-stat": {
			Handler: a.server.handleFileStat,
			Method:  http.MethodPost,
			Payload: "",
			Help:    "Returns basic information about a file or directory.",
		},
		"file-listing": {
			Handler: a.server.handleFileListing,
			Method:  http.MethodPost,
			Payload: payloadFileListing{},
			Help:    "Returns the list of files for a given path.",
		},
		"file-selector-roots": {
			Handler: a.server.handleFileSelectorRoots,
			Method:  http.MethodGet,
			Help:    "Returns a list of root paths for a file picker.",
		},
		"get-entity": {
			Handler: a.server.handleGetEntity,
			Method:  http.MethodPost,
			Payload: getEntityPayload{},
			Help:    "Returns information about the given entity.",
		},
		"import": {
			Handler: a.server.handleImport,
			Method:  http.MethodPost,
			Payload: ImportParameters{},
			Help:    "Starts an import job.",
		},
		"item-classifications": {
			Handler: a.server.handleItemClassifications,
			Method:  http.MethodPost,
			Payload: "",
			Help:    "Returns the item classifications for the given timeline.",
		},
		"jobs": {
			Handler:     a.server.handleJobs,
			Method:      methodQuery,
			Payload:     jobsPayload{},
			ContentType: JSON,
			Help:        "Gets current information about jobs.",
		},
		"logs": {
			Handler: a.server.handleLogs,
			Method:  http.MethodGet,
			Help:    "Initiates a WebSocket connection to send logs.",
		},
		"merge-entities": {
			Handler: a.server.handleMergeEntities,
			Method:  http.MethodPost,
			Payload: mergeEntitiesPayload{},
			Help:    "Merge two entities together.",
		},
		"next-graph": {
			Handler: a.server.handleNextGraph,
			Method:  http.MethodGet,
			Help:    "Gets the next graph from an interactive import.",
		},
		"open-repositories": {
			Handler: a.server.handleRepos,
			Method:  http.MethodGet,
			Help:    "Returns the list of open timelines.",
		},
		"open-repository": {
			Handler: a.server.handleOpenRepo,
			Method:  http.MethodPost,
			Payload: openRepoPayload{},
			Help:    "Open a timeline repository.",
		},
		"pause-job": {
			Handler: a.server.handlePauseJob,
			Method:  http.MethodPost,
			Payload: jobPayload{},
			Help:    "Pauses an active job.",
		},
		"plan-import": {
			Handler: a.server.handlePlanImport,
			Method:  http.MethodPost,
			Payload: PlannerOptions{},
			Help:    "Proposes an import plan in preparation for performing a data import.",
		},
		"recent-conversations": {
			Handler: a.server.handleRecentConversations,
			Method:  http.MethodPost,
			Payload: timeline.ItemSearchParams{},
			Help:    "Loads recent conversations.",
		},
		"repository-empty": {
			Handler: a.server.handleRepositoryEmpty,
			Method:  http.MethodPost,
			Payload: "",
			Help:    "Returns whether the repository is empty or not.",
		},
		"settings": {
			Handler: a.server.handleSettings,
			Method:  http.MethodGet,
			Help:    "Returns settings for the application and opened timelines.",
		},
		"submit-graph": {
			Handler: a.server.handleSubmitGraph,
			Method:  http.MethodPost,
			Payload: submitGraphPayload{},
			Help:    "Submits a graph for processing during an interactive import.",
		},
		"search-entities": {
			Handler: a.server.handleSearchEntities,
			Method:  http.MethodPost,
			Payload: timeline.EntitySearchParams{},
			Help:    "Finds and filters entities in a timeline.",
		},
		"search-items": {
			Handler: a.server.handleSearchItems,
			Method:  http.MethodPost,
			Payload: timeline.ItemSearchParams{},
			Help:    "Finds and filters items in a timeline.",
		},
		"start-job": {
			Handler: a.server.handleStartJob,
			Method:  http.MethodPost,
			Payload: jobPayload{},
			Help:    "Starts a job.",
		},
		"charts": {
			Handler: a.server.handleCharts,
			Method:  http.MethodGet,
			Help:    "Returns statistics about the timeline for use in charts.",
		},
		"unpause-job": {
			Handler: a.server.handleUnpauseJob,
			Method:  http.MethodPost,
			Payload: jobPayload{},
			Help:    "Unpauses a paused job.",
		},
	}
}

type Endpoint struct {
	Method      string
	ContentType ContentType
	Payload     any
	Handler     handlerFunc
	Help        string
}

// GetContentType returns the Content-Type of the endpoint
// considering its default of JSON if method is POST, PUT, PATCH, or DELETE.
func (e Endpoint) GetContentType() ContentType {
	if e.ContentType == None && e.Payload != nil &&
		(e.Method == http.MethodPost || e.Method == http.MethodPut ||
			e.Method == http.MethodPatch || e.Method == http.MethodDelete ||
			e.Method == methodQuery) {
		return JSON
	}
	return e.ContentType
}

// GET but officially supports a request body.
const methodQuery = "QUERY"

type ctxKey string

var ctxKeyPayload ctxKey = "payload"

func (e Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	switch e.GetContentType() {
	case JSON:
		payload := reflect.New(reflect.TypeOf(e.Payload)).Interface()
		if r.ContentLength > 0 {
			err := json.NewDecoder(r.Body).Decode(&payload)
			if err != nil {
				return Error{
					Err:        err,
					HTTPStatus: http.StatusBadRequest,
					Log:        "decoding request body as JSON",
					Message:    "Invalid JSON in request body.",
				}
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyPayload, payload))
	case Form, None:
	}

	return e.Handler(w, r)
}

func (a *App) CommandLineHelp() string {
	// alphabetize the commands list
	type commandEndpoint struct {
		command  string
		endpoint Endpoint
	}
	commands := make([]commandEndpoint, 0, len(a.commands))
	for command, endpoint := range a.commands {
		commands = append(commands, commandEndpoint{command, endpoint})
	}
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].command < commands[j].command
	})

	var sb strings.Builder

	sb.WriteString(`Timelinize is an application to curate your portion of the global digital record by organizing
your own data on your own computer.

It consists of a server, command line client, and web client. Timelinize can be used via a web
page / GUI, command line (CLI), or HTTP JSON API. The CLI and API have symmetric commands
(inputs and outputs).

Usage:
  timelinize [command] [args...]

Examples:
  $ timelinize
  $ timelinize serve
  $ timelinize search-items --repo ... --data-text ...

Available Commands:`)

	for _, pair := range commands {
		sb.WriteString("\n  ")
		sb.WriteString(pair.command)

		if pair.endpoint.Payload != nil {
			val := reflect.ValueOf(pair.endpoint.Payload)
			kind := val.Kind()

			switch kind { //nolint:exhaustive
			case reflect.Slice:
				sb.WriteString(" <")
				sb.WriteString(val.Type().Elem().String())
				sb.WriteString("...>")
			case reflect.Struct:
				fields := nestedFields(pair.endpoint.Payload)

				for i, field := range fields {
					jsonStructTag := field.Tag.Get("json")
					if jsonStructTag == "" {
						continue
					}
					dataType := field.Type
					argName, omitEmpty, cut := strings.Cut(jsonStructTag, ",")
					if argName == "-" {
						continue
					}
					if argName != "" {
						argName = strings.ReplaceAll(argName, "_", "-")
					}
					if i > 0 && i%3 == 0 {
						sb.WriteString("\n\t\t")
					}
					optional := cut && omitEmpty == "omitempty"
					if optional {
						sb.WriteString(fmt.Sprintf(" [--%s <%s>]", argName, dataType))
					} else {
						sb.WriteString(fmt.Sprintf(" --%s <%s>", argName, dataType))
					}
				}
			default:
				sb.WriteString(" <")
				sb.WriteString(kind.String())
				sb.WriteRune('>')
			}
		}

		sb.WriteString("\n      ")
		sb.WriteString(pair.endpoint.Help)
		sb.WriteRune('\n')
	}

	return sb.String()
}

// nestedFields flattens the struct fields from embedded structs of thing,
// which must be a struct.
func nestedFields(thing any) []reflect.StructField {
	val := reflect.ValueOf(thing)
	typ := reflect.TypeOf(thing)

	var fields []reflect.StructField

	for i := range typ.NumField() {
		typf := typ.Field(i)
		valf := val.Field(i)

		if valf.Kind() == reflect.Struct && typf.Anonymous {
			fields = append(fields, nestedFields(valf.Interface())...)
		} else {
			fields = append(fields, typf)
		}
	}

	return fields
}

// ContentType is an HTTP Content-Type value.
type ContentType string

// Content types that are supported.
const (
	JSON ContentType = "application/json"
	Form ContentType = "application/x-www-form-urlencoded"
	None ContentType = ""
)

const apiBasePath = "/api/"
