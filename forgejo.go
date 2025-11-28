package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type GetRepoEndpointResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Private     bool   `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
		Pull  bool `json:"pull"`
		Push  bool `json:"push"`
	} `json:"permissions"`
}

func ForgeGetRepoWithClient(repo string, client *http.Client) (*GetRepoEndpointResponse, error) {
	resp, err := client.Get(config.ForgeURL + "/api/v1/repos/" + repo)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 {
		return nil, errors.New("not found")
	}

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed GetRepoEndpointResponse
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func ForgeGetRepoWithAccessToken(repo, accessToken string) (*GetRepoEndpointResponse, error) {
	req, err := http.NewRequest("GET", config.ForgeURL+"/api/v1/repos/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 {
		return nil, errors.New("not found")
	}

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed GetRepoEndpointResponse
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func ForgeCheckRepoWritableWithClient(repo string, client *http.Client) (bool, error) {
	resp, err := ForgeGetRepoWithClient(repo, client)
	if err != nil {
		if err.Error() == "not found" {
			return false, nil
		}
		return false, err
	}
	return resp.Permissions.Admin || resp.Permissions.Push, nil
}

func ForgeCheckRepoWritableWithAccessToken(repo, accessToken string) (bool, error) {
	resp, err := ForgeGetRepoWithAccessToken(repo, accessToken)
	if err != nil {
		if err.Error() == "not found" {
			return false, nil
		}
		return false, err
	}
	return resp.Permissions.Admin || resp.Permissions.Push, nil
}

func ForgeCheckRepoReadableWithClient(repo string, client *http.Client) (bool, error) {
	resp, err := ForgeGetRepoWithClient(repo, client)
	if err != nil {
		if err.Error() == "not found" {
			return false, nil
		}
		return false, err
	}
	return resp.Permissions.Admin || resp.Permissions.Push || resp.Permissions.Pull, nil
}

func ForgeCheckRepoReadableWithAccessToken(repo, accessToken string) (bool, error) {
	resp, err := ForgeGetRepoWithAccessToken(repo, accessToken)
	if err != nil {
		if err.Error() == "not found" {
			return false, nil
		}
		return false, err
	}
	return resp.Permissions.Admin || resp.Permissions.Push || resp.Permissions.Pull, nil
}
