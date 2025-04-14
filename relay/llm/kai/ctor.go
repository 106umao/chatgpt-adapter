package kai

import (
	"chatgpt-adapter/core/gin/inter"
	"chatgpt-adapter/core/logger"

	"github.com/iocgo/sdk/env"

	_ "github.com/iocgo/sdk"
)

// @Inject(name = "kai-adapter")
func New(env *env.Environment) inter.Adapter {
	logger.Info("new kai adapter")
	return &api{env: env}
}
