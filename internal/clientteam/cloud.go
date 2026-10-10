package clientteam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// httpClient is the ONE seam every request in this package goes through, and it is what makes the
// tests offline: a test replaces it with a client whose Transport answers from memory, so no test
// dials anything - not the cloud, not a loopback server. The Python tests ran a
// BaseHTTPRequestHandler on 127.0.0.1; a fake transport is strictly less than that (no socket is
// opened at all), and it is the same shape clientcmd already uses for its LookPath and GOOS seams.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// cloudClient is the pair every request needs. It is a value, not a package global, so a verb's
// credentials cannot leak into another verb's run.
type cloudClient struct {
	baseURL string
	token   string
}

// credentials reads the one credential pair from the environment. The client's one env file has
// already been loaded into the environment by the dispatcher, so an operator's own export wins.
// A missing half is a usage error naming that half, not a generic failure.
func credentials() (*cloudClient, error) {
	baseURL := os.Getenv("AGENTHUB_URL")
	token := os.Getenv("AGENTHUB_TOKEN")
	if baseURL == "" {
		return nil, usageError("AGENTHUB_URL is not set")
	}
	if token == "" {
		return nil, usageError("AGENTHUB_TOKEN is not set")
	}
	return &cloudClient{baseURL: baseURL, token: token}, nil
}

// optionalCredentials is the same pair when BOTH are present, and (nil, false) otherwise - never an
// error. apply and import-project still plan without a server; only sending needs one.
func optionalCredentials() (*cloudClient, bool) {
	baseURL := os.Getenv("AGENTHUB_URL")
	token := os.Getenv("AGENTHUB_TOKEN")
	if baseURL == "" || token == "" {
		return nil, false
	}
	return &cloudClient{baseURL: baseURL, token: token}, true
}

func (c *cloudClient) url(path string) string {
	return strings.TrimRight(c.baseURL, "/") + path
}

// getJSON GETs path and returns (status, decoded body). An HTTP error status comes back as that
// status, with no body, which is how the Python's urllib HTTPError branch behaves. A transport
// failure is a remote failure, so it carries ExitRemote.
func (c *cloudClient) getJSON(path string) (int, any, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.url(path), nil)
	if err != nil {
		return 0, nil, remoteError(fmt.Sprintf("GET %s failed: %v", path, err))
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, nil, remoteError(fmt.Sprintf("GET %s failed: %v", path, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, remoteError(fmt.Sprintf("GET %s failed: %v", path, err))
	}
	if response.StatusCode >= 400 {
		return response.StatusCode, nil, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		// An undecodable 2xx body is not JSON the Python's callers could use either; they treat a
		// missing shape as an unexpected body, which is what a nil payload produces here.
		return response.StatusCode, nil, nil
	}
	return response.StatusCode, payload, nil
}

// send performs one write and returns (status, response text). An HTTP error status is a normal
// return, because the caller's outcome() decides what it means.
func (c *cloudClient) send(method, path string, body any) (int, string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, "", remoteError(fmt.Sprintf("%s %s failed: %v", method, path, err))
	}
	request, err := http.NewRequestWithContext(context.Background(), method, c.url(path), strings.NewReader(string(encoded)))
	if err != nil {
		return 0, "", remoteError(fmt.Sprintf("%s %s failed: %v", method, path, err))
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, "", remoteError(fmt.Sprintf("%s %s failed: %v", method, path, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", remoteError(fmt.Sprintf("%s %s failed: %v", method, path, err))
	}
	return response.StatusCode, string(raw), nil
}

// storedModule is one module version the cloud holds.
type storedModule struct {
	slug    string
	version string
	kind    string
	content string
}

func moduleFromPayload(payload any) (*storedModule, bool) {
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	inner, ok := object["module"].(map[string]any)
	if !ok {
		return nil, false
	}
	return &storedModule{
		slug:    stringField(inner, "slug"),
		version: stringField(inner, "version"),
		kind:    stringField(inner, "kind"),
		content: stringField(inner, "content"),
	}, true
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// getModuleVersion returns the stored module-version body, or nil when the version does not exist.
func (c *cloudClient) getModuleVersion(slug, version string) (*storedModule, error) {
	path := fmt.Sprintf("%s/modules/%s/versions/%s", apiPrefix, slug, version)
	status, payload, err := c.getJSON(path)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, remoteError(fmt.Sprintf("GET %s failed: HTTP %d", path, status))
	}
	module, ok := moduleFromPayload(payload)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
	}
	return module, nil
}

// listModuleVersions returns the newest stored version of every module (GET /modules).
func (c *cloudClient) listModuleVersions() ([]storedModule, error) {
	path := apiPrefix + "/modules"
	status, payload, err := c.getJSON(path)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, remoteError(fmt.Sprintf("GET %s failed: HTTP %d", path, status))
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
	}
	list, ok := object["modules"].([]any)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
	}
	modules := make([]storedModule, 0, len(list))
	for _, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
		}
		modules = append(modules, storedModule{
			slug:    stringField(object, "slug"),
			version: stringField(object, "version"),
			kind:    stringField(object, "kind"),
			content: stringField(object, "content"),
		})
	}
	return modules, nil
}

// storedSeatTypeSlugs returns the slug of every seat type the cloud already stores (GET /seat-types).
func (c *cloudClient) storedSeatTypeSlugs() (map[string]bool, error) {
	path := apiPathSeatTypes
	status, payload, err := c.getJSON(path)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, remoteError(fmt.Sprintf("GET %s failed: HTTP %d", path, status))
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
	}
	list, ok := object["seat_types"].([]any)
	if !ok {
		return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
	}
	slugs := map[string]bool{}
	for _, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, remoteError(fmt.Sprintf("GET %s returned an unexpected body", path))
		}
		slugs[stringField(object, "slug")] = true
	}
	return slugs, nil
}

// resolveModules classifies each module against the stored versions before anything is pushed.
//
// A version that does not exist is PUSH; one whose stored content is identical is SKIP (no request is
// ever sent for it); one whose stored content differs moves to the next patch and is NEW-VERSION, so
// the change lands as a new immutable version.
func (c *cloudClient) resolveModules(modules []module, version string) ([]resolvedEntry, error) {
	resolved := make([]resolvedEntry, 0, len(modules))
	for _, m := range modules {
		target := version
		for {
			stored, err := c.getModuleVersion(m.slug, target)
			if err != nil {
				return nil, err
			}
			if stored == nil {
				action := actionPush
				if target != version {
					action = actionNewVersion
				}
				resolved = append(resolved, resolvedEntry{module: m, action: action, version: target, requested: version})
				break
			}
			if stored.content == m.content {
				resolved = append(resolved, resolvedEntry{module: m, action: actionSkip, version: target, requested: version})
				break
			}
			next, err := nextPatch(target)
			if err != nil {
				return nil, err
			}
			target = next
		}
	}
	return resolved, nil
}

// storedSkillBlocks is the full body of every stored skill module in a listing, because the listing
// itself carries no content.
func (c *cloudClient) storedSkillBlocks(summaries []storedModule) ([]storedModule, error) {
	blocks := make([]storedModule, 0, len(summaries))
	for _, summary := range summaries {
		if summary.kind != skillKind {
			continue
		}
		module, err := c.getModuleVersion(summary.slug, summary.version)
		if err != nil {
			return nil, err
		}
		if module == nil {
			return nil, remoteError(fmt.Sprintf(
				"stored module %s@%s disappeared between listing and read", summary.slug, summary.version))
		}
		blocks = append(blocks, *module)
	}
	return blocks, nil
}

// remoteError is a failure talking to the cloud: the verb ran and the remote did not answer as it should.
func remoteError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: ExitRemote}
}
