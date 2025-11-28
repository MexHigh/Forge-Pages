package main

import "encoding/json"

type GetRepoEndpointResponse struct {
	Private     bool `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
		Pull  bool `json:"pull"`
		Push  bool `json:"push"`
	} `json:"permissions"`
}

func ParseGetRepoEndpointResponse(body []byte) (*GetRepoEndpointResponse, error) {
	var resp GetRepoEndpointResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
