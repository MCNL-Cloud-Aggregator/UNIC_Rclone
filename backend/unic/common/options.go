package common

import "github.com/rclone/rclone/fs"

// Options defines the configuration for this backend
type Options struct {
	Upstreams fs.SpaceSepList `config:"upstreams"`
	UserID    string          `config:"userid"`
}
