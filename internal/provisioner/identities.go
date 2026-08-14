package provisioner

import (
	"context"
	"fmt"
	"log/slog"

	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/s3config"
)

func (p *Provisioner) ensureIdentity(ctx context.Context, identity config.Identity) error {
	slog.Info("Writing identity", "name", identity.Name)

	creds := make([]s3config.Credential, len(identity.Credentials))
	for i, c := range identity.Credentials {
		creds[i] = s3config.Credential{
			AccessKey: c.AccessKey,
			SecretKey: c.SecretKey,
		}
	}

	storeIdentity := s3config.Identity{
		Name:        identity.Name,
		Credentials: creds,
		Actions:     identity.Actions,
	}

	if err := p.identities.PutIdentity(ctx, storeIdentity); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}

	slog.Info("Identity written", "name", identity.Name, "actions", len(identity.Actions))
	return nil
}
