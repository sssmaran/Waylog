package ingest

import "github.com/sssmaran/WaylogCLI/internal/sampler"

var Sampler = sampler.New(sampler.LoadConfigFromEnv())
