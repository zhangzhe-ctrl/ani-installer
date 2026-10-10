package ani

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var anonymousBearerField = regexp.MustCompile(`(?:^|,)\s*([a-z_]+)="([^"\r\n]*)"`)

// Public OCI registries can require a short-lived anonymous pull token even
// for published chart blobs. Only a same-authority HTTPS /token challenge for
// the exact requested repository is accepted. No user login or stored registry
// credential participates in material preparation.
func getHTTPSMaterial(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		return response, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(request.URL.Path, "/v2/"), "/blobs/", 2)
	if !strings.HasPrefix(request.URL.Path, "/v2/") || len(parts) != 2 || parts[0] == "" {
		return response, nil
	}
	fields := map[string]string{}
	for _, m := range anonymousBearerField.FindAllStringSubmatch(strings.TrimPrefix(challenge, "Bearer "), -1) {
		if _, exists := fields[m[1]]; exists {
			response.Body.Close()
			return nil, fmt.Errorf("OCI material challenge repeats a field")
		}
		fields[m[1]] = m[2]
	}
	realm, parseErr := url.Parse(fields["realm"])
	if parseErr != nil || request.URL.Scheme != "https" || realm.Scheme != "https" || realm.Host != request.URL.Host || realm.Path != "/token" || realm.User != nil || realm.RawQuery != "" || realm.Fragment != "" || fields["service"] != request.URL.Host || fields["scope"] != "repository:"+parts[0]+":pull" {
		response.Body.Close()
		return nil, fmt.Errorf("OCI material challenge does not name this HTTPS authority and exact repository pull scope")
	}
	response.Body.Close()
	query := url.Values{"service": {fields["service"]}, "scope": {fields["scope"]}}
	realm.RawQuery = query.Encode()
	tokenRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, realm.String(), nil)
	if err != nil {
		return nil, err
	}
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		return nil, err
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anonymous OCI token request returned %s", tokenResponse.Status)
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&token); err != nil {
		return nil, fmt.Errorf("anonymous OCI token response is invalid")
	}
	value := token.Token
	if value == "" {
		value = token.AccessToken
	}
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("anonymous OCI token response has no valid token")
	}
	retry := request.Clone(request.Context())
	retry.Header.Set("Authorization", "Bearer "+value)
	// Authorization is scoped to the registry host; blob CDNs receive no token.
	scopedClient := *client
	priorRedirect := client.CheckRedirect
	scopedClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Host != request.URL.Host {
			next.Header.Del("Authorization")
		}
		if priorRedirect != nil {
			return priorRedirect(next, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("OCI material exceeded redirect limit")
		}
		return nil
	}
	return scopedClient.Do(retry)
}
