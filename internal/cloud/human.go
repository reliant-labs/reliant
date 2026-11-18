package cloud

import (
	"errors"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// HumanClientID is a separate storage namespace from delegated forge-cli
// capabilities. Storing an automation token never replaces a human identity.
const HumanClientID = "forge-cli-human"

func lookupCredential(path, endpoint string) (credentials.Credential, error) {
	human, err := credentials.Lookup(path, endpoint, HumanClientID)
	if err == nil {
		if human.Kind != "human" {
			return credentials.Credential{}, errors.New("human credential entry contains a delegated capability; run forge login")
		}
		return human, nil
	}
	if !errors.Is(err, credentials.ErrNotFound) {
		return credentials.Credential{}, err
	}
	return credentials.Lookup(path, endpoint, ClientID)
}
