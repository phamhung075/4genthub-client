package clientteam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// `team project` is the CLI path to a cloud project. `team apply` creates modules, room, seats, links and
// overlays and nothing else, so before this verb a room's project could only be made by a seat calling the
// MCP tool `manage_project` (measured 2026-10-25: room aa2-rs existed, its project did not).
//
// THERE IS NO ROOM LINK TO WRITE. The cloud's room record carries id, slug, name and team id, and no
// project id, so a room and a project are tied by name only; this verb says so instead of pretending a
// link was made.
const projectsPath = apiPrefix + "/projects/"

type cloudProject struct {
	ID   string
	Name string
}

func projectFromPayload(raw any) (cloudProject, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return cloudProject{}, false
	}
	project := cloudProject{ID: stringField(object, "id"), Name: stringField(object, "name")}
	return project, project.ID != ""
}

// listProjects reads the caller's projects: `{"success": true, "projects": [{"id", "name", ...}]}`.
func (c *cloudClient) listProjects() ([]cloudProject, error) {
	status, payload, err := c.getJSON(projectsPath)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, remoteError(fmt.Sprintf("GET %s answered %d", projectsPath, status))
	}
	object, _ := payload.(map[string]any)
	items, ok := object["projects"].([]any)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s answered without a projects list", projectsPath))
	}
	out := make([]cloudProject, 0, len(items))
	for _, item := range items {
		if project, ok := projectFromPayload(item); ok {
			out = append(out, project)
		}
	}
	return out, nil
}

// createProject posts the form the route reads (`name`, `description`) and returns the stored project.
func (c *cloudClient) createProject(name, description string) (cloudProject, error) {
	form := url.Values{"name": {name}, "description": {description}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.url(projectsPath),
		strings.NewReader(form.Encode()))
	if err != nil {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s failed: %v", projectsPath, err))
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s failed: %v", projectsPath, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s failed: %v", projectsPath, err))
	}
	if response.StatusCode >= 400 {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s answered %d: %s", projectsPath, response.StatusCode,
			strings.TrimSpace(string(raw))))
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s answered a body that is not JSON", projectsPath))
	}
	project, ok := projectFromPayload(payload["project"])
	if !ok {
		return cloudProject{}, remoteError(fmt.Sprintf("POST %s answered without a project id", projectsPath))
	}
	return project, nil
}

func printProjectUsage(w io.Writer) {
	fmt.Fprint(w, `usage: 4genteam team project list
       4genteam team project ensure --name NAME [--description TEXT] [--dry-run]

list    print the cloud projects of this client's account, one "ID NAME" per line
ensure  print the project named NAME, creating it first when none has that name; idempotent

The cloud's room record has no project field, so a room is tied to its project by name only: name the
project after the room (e.g. --name aa2-rs). Uses AGENTHUB_URL and AGENTHUB_TOKEN, as team apply does.
`)
}

// runProject is `team project list|ensure`.
func runProject(args []string, stdout, stderr io.Writer) error {
	usage := func(w io.Writer) { printProjectUsage(w) }
	if len(args) == 0 {
		return usageError("team project: pick list or ensure")
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(stdout)
		return errHelpShown
	case "list":
		flags := newVerbFlags("project list")
		if err := parseVerb(flags, args[1:], usage, stdout); err != nil {
			return err
		}
		cloud, err := credentials()
		if err != nil {
			return err
		}
		projects, err := cloud.listProjects()
		if err != nil {
			return err
		}
		for _, project := range projects {
			fmt.Fprintf(stdout, "%s %s\n", project.ID, project.Name)
		}
		return nil
	case "ensure":
		flags := newVerbFlags("project ensure")
		name := flags.String("name", "", "project name")
		description := flags.String("description", "", "project description")
		dryRun := flags.Bool("dry-run", false, "read the list, create nothing")
		if err := parseVerb(flags, args[1:], usage, stdout); err != nil {
			return err
		}
		wanted := strings.TrimSpace(*name)
		if wanted == "" {
			return usageError("team project ensure: --name NAME is required")
		}
		cloud, err := credentials()
		if err != nil {
			return err
		}
		projects, err := cloud.listProjects()
		if err != nil {
			return err
		}
		for _, project := range projects {
			if project.Name == wanted {
				fmt.Fprintf(stdout, "exists: %s id=%s\n", project.Name, project.ID)
				return nil
			}
		}
		if *dryRun {
			fmt.Fprintf(stdout, "would create: %s\n", wanted)
			return nil
		}
		project, err := cloud.createProject(wanted, *description)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created: %s id=%s\n", project.Name, project.ID)
		return nil
	default:
		return usageError(fmt.Sprintf("team project: unknown action %q; pick list or ensure", args[0]))
	}
}
