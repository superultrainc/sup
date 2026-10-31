package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// GitHub calls go straight to the API with the token from `gh auth token`
// instead of spawning gh, which costs ~200-400ms of process startup each.

// githubAPI is the API root; tests point it at a local server.
var githubAPI = "https://api.github.com"

// githubREST calls the REST API, retrying once if the cached token expired.
func githubREST(method, path string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	do := func() (*http.Response, error) {
		req, err := http.NewRequest(method, githubAPI+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+ghToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return httpClient.Do(req)
	}
	resp, err := do()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		invalidateGHToken()
		if err := refreshGHToken(); err != nil {
			return nil, fmt.Errorf("auth refresh failed: %w", err)
		}
		if resp, err = do(); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	data = stripJSONControls(data)
	if resp.StatusCode >= 400 {
		return data, apiError(resp.StatusCode, data)
	}
	return data, nil
}

// apiError turns GitHub's {"message", "errors": [...]} into one readable line.
func apiError(status int, data []byte) error {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &e) != nil || e.Message == "" {
		return fmt.Errorf("GitHub API %d", status)
	}
	parts := []string{e.Message}
	for _, d := range e.Errors {
		if d.Message != "" {
			parts = append(parts, d.Message)
		}
	}
	return fmt.Errorf("%s", strings.Join(parts, ": "))
}

func repoPath(pr PR) string {
	return "/repos/" + pr.Repository.Owner.Login + "/" + pr.Repository.Name
}

// graphqlErrors reports the first error a GraphQL response carries, if any.
func graphqlErrors(data []byte) error {
	var r struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &r) == nil && len(r.Errors) > 0 {
		return fmt.Errorf("%s", r.Errors[0].Message)
	}
	return nil
}

// fetchViewer returns the signed-in user and their organizations in one request.
func fetchViewer() (string, []string, error) {
	out, err := graphqlPOST(`{ viewer { login organizations(first: 100) { nodes { login } } } }`)
	if err != nil {
		return "", nil, err
	}
	if err := graphqlErrors(out); err != nil {
		return "", nil, err
	}
	var r struct {
		Data struct {
			Viewer struct {
				Login         string `json:"login"`
				Organizations struct {
					Nodes []struct {
						Login string `json:"login"`
					} `json:"nodes"`
				} `json:"organizations"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", nil, err
	}
	var orgs []string
	for _, o := range r.Data.Viewer.Organizations.Nodes {
		orgs = append(orgs, o.Login)
	}
	return r.Data.Viewer.Login, orgs, nil
}

// stripJSONControls drops terminal control characters (ESC, BEL, C1...) from a
// JSON reply, so nothing GitHub returns can drive the terminal. \n, \r and \t stay.
func stripJSONControls(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == '\\' && i+1 < len(b):
			if b[i+1] == 'u' && i+6 <= len(b) {
				if v, err := strconv.ParseUint(string(b[i+2:i+6]), 16, 16); err == nil && isControl(rune(v)) {
					i += 6
					continue
				}
			}
			out = append(out, c, b[i+1]) // keeps escaped backslashes intact
			i += 2
		case c == 0x7f:
			i++
		case c == 0xc2 && i+1 < len(b) && b[i+1] >= 0x80 && b[i+1] <= 0x9f: // raw U+0080-U+009F
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || (r >= 0x7f && r <= 0x9f)
}

// stripControls drops terminal control characters from plain text such as a patch.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}
