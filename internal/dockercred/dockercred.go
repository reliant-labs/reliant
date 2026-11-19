// Package dockercred reads the docker credential store the way the docker CLI
// reads it, for forge's in-process registry clients (oras).
//
// WHY THIS EXISTS. forge pushes to one registry over two transports: `docker
// push` for images, and oras in-process for static-site releases, source maps,
// bundles and the cut journal. One `docker login` is meant to serve all of
// them, which holds only if both transports pick the SAME credential for a
// host. oras's docker-config store and the docker CLI disagree on one input:
// a `credHelpers` entry whose value is "".
//
//   - docker: an entry that EXISTS pins the host to that helper, and "" means
//     no helper — the credential lives in config.json's `auths`.
//   - oras: "" is indistinguishable from "no entry", so it falls through to
//     `credsStore` (osxkeychain, desktop, wincred, …).
//
// forge writes exactly that entry for an anonymous (--claimable) deploy, so
// the deploy's login lands in a throwaway config.json and never in the user's
// keychain. docker then pushed the API image with that login, and oras read
// whatever the keychain happened to hold for the registry instead: a stale or
// revoked credential, refused by the realm with "the supplied credential is
// not valid". The image push succeeded and every site and source-map push
// after it 401'd.
//
// So this package resolves a host the docker CLI's way, and every oras client
// forge builds takes its credential hook from here.
package dockercred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Credential is the oras credential hook for the ambient docker config:
// $DOCKER_CONFIG/config.json, else ~/.docker/config.json — the file `docker
// login` and `docker push` in this process's environment use.
func Credential() (auth.CredentialFunc, error) {
	ds, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, err
	}
	return pin(ds)
}

// CredentialFrom is Credential for the docker config file at configPath.
func CredentialFrom(configPath string) (auth.CredentialFunc, error) {
	ds, err := credentials.NewStore(configPath, credentials.StoreOptions{})
	if err != nil {
		return nil, err
	}
	return pin(ds)
}

// pin is ds's credential hook, except that a host with an empty credHelpers
// entry reads the config file, as docker does. For every other host the two
// already agree, so a config with no such entry gets ds's hook unchanged.
func pin(ds *credentials.DynamicStore) (auth.CredentialFunc, error) {
	path := ds.ConfigPath()
	hosts, err := fileHelperHosts(path)
	if err != nil {
		return nil, fmt.Errorf("read docker config %s: %w", path, err)
	}
	dynamic := credentials.Credential(ds)
	if len(hosts) == 0 {
		return dynamic, nil
	}
	file, err := credentials.NewFileStore(path)
	if err != nil {
		return nil, err
	}
	fromFile := credentials.Credential(file)
	return func(ctx context.Context, hostport string) (auth.Credential, error) {
		if hosts[serverAddress(hostport)] {
			return fromFile(ctx, hostport)
		}
		return dynamic(ctx, hostport)
	}, nil
}

// serverAddress is the key docker files hostport's credential under — the
// same mapping credentials.Credential applies before it asks a store.
func serverAddress(hostport string) string {
	if hostport == "docker.io" {
		return "https://index.docker.io/v1/"
	}
	return hostport
}

// fileHelperHosts are the hosts configPath pins to the config file itself: a
// credHelpers entry that is present and empty. A missing file pins nothing.
func fileHelperHosts(configPath string) (map[string]bool, error) {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	hosts := map[string]bool{}
	for host, helper := range cfg.CredHelpers {
		if helper == "" {
			hosts[host] = true
		}
	}
	return hosts, nil
}
