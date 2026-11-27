// Package assets holds the files the sandbox image is built from, so the
// opencode-sandbox binary needs nothing next to it.
package assets

import "embed"

// Files is the image's build context.
//
//go:embed Dockerfile entrypoint.sh gateway.pl ssh_config
var Files embed.FS
