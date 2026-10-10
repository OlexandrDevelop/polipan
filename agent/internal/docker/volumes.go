// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
)

type volumeJSON struct {
	Name       string            `json:"Name"`
	Mountpoint string            `json:"Mountpoint"`
	Labels     map[string]string `json:"Labels"`
}

func (v volumeJSON) volume() *Volume {
	return &Volume{Name: v.Name, Mountpoint: v.Mountpoint, Labels: v.Labels}
}

// VolumeCreate creates a named local volume. Docker returns the existing one when the name is used.
func (c *HTTPClient) VolumeCreate(ctx context.Context, name string, labels map[string]string) (*Volume, error) {
	resp, err := c.do(ctx, http.MethodPost, "/volumes/create", nil,
		map[string]any{"Name": name, "Driver": "local", "Labels": labels}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var v volumeJSON
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return v.volume(), nil
}

// VolumeInspect returns one volume.
func (c *HTTPClient) VolumeInspect(ctx context.Context, name string) (*Volume, error) {
	var v volumeJSON
	if err := c.getJSON(ctx, "/volumes/"+url.PathEscape(name), nil, &v); err != nil {
		return nil, err
	}
	return v.volume(), nil
}

// VolumeList returns the volumes with all the labels.
func (c *HTTPClient) VolumeList(ctx context.Context, labels map[string]string) ([]Volume, error) {
	var lf []string
	for k, v := range labels {
		lf = append(lf, k+"="+v)
	}
	filters, _ := json.Marshal(map[string][]string{"label": lf})
	var r struct {
		Volumes []volumeJSON `json:"Volumes"`
	}
	if err := c.getJSON(ctx, "/volumes", url.Values{"filters": {string(filters)}}, &r); err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(r.Volumes))
	for _, v := range r.Volumes {
		out = append(out, *v.volume())
	}
	return out, nil
}

// VolumeRemove removes a volume. A volume that a container uses gives an error.
func (c *HTTPClient) VolumeRemove(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// RegistryAuth returns the X-Registry-Auth header value for a pull with a user name and a password.
// The value holds the password: never log it.
func RegistryAuth(server, username, password string) string {
	b, _ := json.Marshal(map[string]string{"username": username, "password": password, "serveraddress": server})
	return base64.URLEncoding.EncodeToString(b)
}
