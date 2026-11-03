package cloud

import (
	"context"
	"fmt"
	"net/http"
)

// RevokeIdentity logs this device out across every organization, not one org.
func RevokeIdentity(ctx context.Context, ep Endpoint, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL+"/oauth/revoke", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := NewClient(ep, Credential{}).httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 401 {
		return fmt.Errorf("could not revoke CLI identity (HTTP %d); local credential retained", resp.StatusCode)
	}
	return nil
}
