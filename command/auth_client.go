package command

import (
	"os"

	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/httpx"
)

// tokenFlag is the persistent --token flag. BLANKET_TOKEN is the better
// way to pass one: a flag shows up in `ps` for every user on the machine.
var tokenFlag string

// configureClientAuth makes every request this process sends to the local
// server carry a token when one is available (turtlemonvh/blanket#101,
// A.5): --token, else BLANKET_TOKEN, else <dataDir>/credentials. With auth
// off the server ignores it, so sending it unconditionally is harmless.
//
// BLANKET_TOKEN is removed from the environment once read, so nothing this
// process starts -- a worker, or a task a worker runs -- inherits it.
func configureClientAuth() {
	explicit := tokenFlag
	if env, ok := os.LookupEnv(auth.TokenEnvVar); ok {
		if explicit == "" {
			explicit = env
		}
		os.Unsetenv(auth.TokenEnvVar)
	}
	src := auth.NewTokenSource(explicit, auth.CredentialsPath(auth.DataDir(viper.GetString("database"))))
	httpx.SetBearerTokenSource(viper.GetInt("port"), src.Token)
}
