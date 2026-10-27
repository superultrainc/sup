package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
