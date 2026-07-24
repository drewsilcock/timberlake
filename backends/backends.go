// Package backends wires endpoint URIs to concrete Source/Destination
// implementations. It is the one place that imports every backend, keeping the
// transfer interfaces and the UI free of backend-specific dependencies.
package backends

import (
	"context"
	"fmt"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/transfer/localfs"
	"timberlake/transfer/s3backend"
	"timberlake/transfer/sftpbackend"
)

// NewSource builds a Source for the given source URI.
func NewSource(ctx context.Context, uri string, cfg *config.AppConfig) (transfer.Source, error) {
	ep, err := transfer.ParseEndpoint(uri)
	if err != nil {
		return nil, err
	}
	switch ep.Scheme {
	case transfer.SchemeLocal:
		return localfs.New(ep.Root)
	case transfer.SchemeS3:
		return s3backend.New(ctx, ep, cfg)
	case transfer.SchemeSFTP:
		return sftpbackend.New(ctx, ep, cfg)
	default:
		return nil, fmt.Errorf("unsupported source scheme %q", ep.Scheme)
	}
}

// NewDestination builds a Destination for the given destination URI.
func NewDestination(ctx context.Context, uri string, cfg *config.AppConfig) (transfer.Destination, error) {
	ep, err := transfer.ParseEndpoint(uri)
	if err != nil {
		return nil, err
	}
	switch ep.Scheme {
	case transfer.SchemeLocal:
		return localfs.New(ep.Root)
	case transfer.SchemeS3:
		return s3backend.New(ctx, ep, cfg)
	case transfer.SchemeSFTP:
		return sftpbackend.New(ctx, ep, cfg)
	default:
		return nil, fmt.Errorf("unsupported destination scheme %q", ep.Scheme)
	}
}
