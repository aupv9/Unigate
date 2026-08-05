package store

import (
	_ "embed"

	"github.com/redis/go-redis/v9"
)

//go:embed sliding_window.lua
var slidingWindowSrc string

//go:embed gcra.lua
var gcraSrc string

//go:embed lockout.lua
var lockoutSrc string

var (
	slidingWindowScript = redis.NewScript(slidingWindowSrc)
	gcraScript          = redis.NewScript(gcraSrc)
	lockoutScript       = redis.NewScript(lockoutSrc)
)
