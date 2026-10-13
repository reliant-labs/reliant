// Copyright (c) 2025 Reliant Labs
package dockerengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	// callTimeout bounds one engine API call. /system/df sizes every volume,
	// which is the slow part.
	callTimeout = 20 * time.Second
	// pruneTimeout bounds a build cache prune, which can take minutes on a
	// large cache.
	pruneTimeout = 10 * time.Minute
)

// engine is the Service over one unix socket.
type engine struct{ socket string }

// New returns the adapter for the engine listening on deps.SocketPath.
//
// forge:constructor
func New(deps Deps) Service {
	return &engine{socket: deps.SocketPath}
}

// client dials the engine's socket whatever host a request names. Requests
// use the placeholder host "docker", as the docker CLI does.
func (e *engine) client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", e.socket)
		}},
	}
}

// systemDF is the part of GET /system/df this adapter reads.
type systemDF struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		Containers int64 `json:"Containers"`
		Size       int64 `json:"Size"`
		SharedSize int64 `json:"SharedSize"`
	} `json:"Images"`
	Containers []struct {
		SizeRw int64  `json:"SizeRw"`
		State  string `json:"State"`
	} `json:"Containers"`
	Volumes []struct {
		UsageData *struct {
			Size     int64 `json:"Size"`
			RefCount int64 `json:"RefCount"`
		} `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		Size   int64 `json:"Size"`
		InUse  bool  `json:"InUse"`
		Shared bool  `json:"Shared"`
	} `json:"BuildCache"`
}

func (e *engine) DiskUsage(ctx context.Context) (Usage, error) {
	if _, err := os.Stat(e.socket); err != nil {
		return Usage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/system/df", nil)
	if err != nil {
		return Usage{}, err
	}
	resp, err := e.client(callTimeout).Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Usage{}, fmt.Errorf("docker /system/df: %s", resp.Status)
	}
	var df systemDF
	if err := json.NewDecoder(resp.Body).Decode(&df); err != nil {
		return Usage{}, err
	}
	return df.usage(), nil
}

// usage applies the docker CLI's own accounting (`docker system df`): images
// reclaimable are the layers no container uses; containers, the writable
// layers of stopped ones; volumes, those no container references; build
// cache, records neither in use nor shared with an image.
func (df systemDF) usage() Usage {
	var usedByContainers int64
	for _, im := range df.Images {
		if im.Containers > 0 && im.Size >= 0 && im.SharedSize >= 0 {
			usedByContainers += im.Size - im.SharedSize
		}
	}
	var ctrSize, ctrReclaim int64
	for _, c := range df.Containers {
		ctrSize += c.SizeRw
		if c.State != "running" {
			ctrReclaim += c.SizeRw
		}
	}
	var volSize, volReclaim int64
	for _, v := range df.Volumes {
		if v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		volSize += v.UsageData.Size
		if v.UsageData.RefCount == 0 {
			volReclaim += v.UsageData.Size
		}
	}
	var bcSize, bcReclaim int64
	for _, b := range df.BuildCache {
		bcSize += b.Size
		if !b.InUse && !b.Shared {
			bcReclaim += b.Size
		}
	}
	return Usage{
		Images:     category(df.LayersSize, df.LayersSize-usedByContainers),
		Containers: category(ctrSize, ctrReclaim),
		Volumes:    category(volSize, volReclaim),
		BuildCache: category(bcSize, bcReclaim),
	}
}

// category clamps the engine's signed sizes: a negative one counts as
// nothing, and a prune never frees more than the category holds.
func category(size, reclaim int64) Category {
	pos := func(n int64) uint64 {
		if n < 0 {
			return 0
		}
		return uint64(n)
	}
	return Category{Bytes: pos(size), Reclaimable: pos(min(reclaim, size))}
}

func (e *engine) PruneBuildCache(ctx context.Context, olderThan time.Duration) (uint64, error) {
	filters, _ := json.Marshal(map[string][]string{"until": {olderThan.String()}})
	u := "http://docker/build/prune?filters=" + url.QueryEscape(string(filters))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := e.client(pruneTimeout).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("docker /build/prune: %s", resp.Status)
	}
	var out struct {
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.SpaceReclaimed < 0 {
		return 0, nil
	}
	return uint64(out.SpaceReclaimed), nil
}
