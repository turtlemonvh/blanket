package command

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"github.com/turtlemonvh/blanket/lib/auth"
	boltlib "github.com/turtlemonvh/blanket/lib/bolt"
)

// `blanket auth init|disable|status` (turtlemonvh/blanket#101, A.5).
//
// Both init and disable work with the server running (through its API) or
// stopped (by opening the database directly -- bolt's lock means only one
// of the two can be happening). The stopped path needs no credential:
// being able to open the database file is already full control of the
// install. It is also the recovery path for a lost admin token.

var authInitCode string
var authDisableYes bool

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Turn API authentication on or off",
}

var authInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Turn authentication on and write an admin token for this host's CLI",
	Long: `Turn authentication on. Run it on the server's host.

With the server running, this reads the one-time setup code the server
wrote beside its database (or takes --code, from the server log) and trades
it for an admin token. With the server stopped, it opens the database
directly. Either way the token is written to the credentials file beside the
database, readable only by you, and printed once.

On this host the CLI, and workers, use that file automatically. Elsewhere,
pass a token with BLANKET_TOKEN. Mint narrower ones with ` + "`blanket token create`" + `.`,
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		port := viper.GetInt("port")
		dataDir := auth.DataDir(viper.GetString("database"))

		var secret, credPath string
		if serverAnswers(port) {
			code := authInitCode
			if code == "" {
				var err error
				code, err = auth.ReadSecretFile(auth.SetupCodePath(dataDir))
				if err != nil {
					fail(err.Error())
				}
			}
			if code == "" {
				fail(fmt.Sprintf("no setup code found at %s. Is auth already on (`blanket auth status`)? Otherwise pass --code from the server log.", auth.SetupCodePath(dataDir)))
			}
			res, err := client.AuthInit(code, port)
			if err != nil {
				printAPIError(err)
				os.Exit(1)
			}
			secret, credPath = res.Secret, res.CredentialsPath
		} else {
			secret, credPath = offlineAuthInit(dataDir)
		}

		fmt.Fprintln(os.Stderr, "authentication is on.")
		if credPath != "" {
			fmt.Fprintf(os.Stderr, "admin token written to %s (owner-only); the CLI and workers on this host use it automatically,\n", credPath)
			fmt.Fprintln(os.Stderr, "including workers that are already running.")
		} else {
			fmt.Fprintln(os.Stderr, "warning: the credentials file could not be written; pass the token below with BLANKET_TOKEN.")
		}
		fmt.Fprintln(os.Stderr, "the admin token, shown once (for MCP clients or other hosts, prefer `blanket token create`):")
		fmt.Println(secret)
	},
}

var authDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Turn authentication off by revoking every token (run on the server's host)",
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		if !authDisableYes && !confirm("This revokes every API token and turns authentication off, so anyone who can reach the port can run tasks again. Type 'yes' to continue: ") {
			fail("not confirmed")
		}
		port := viper.GetInt("port")
		var n int
		if serverAnswers(port) {
			var err error
			n, err = client.AuthDisable(port)
			if err != nil {
				printAPIError(err)
				os.Exit(1)
			}
		} else {
			n = offlineAuthDisable(auth.DataDir(viper.GetString("database")))
		}
		fmt.Printf("authentication is off; revoked %d token(s)\n", n)
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether authentication is on",
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		st, err := client.AuthStatus(viper.GetInt("port"))
		if err != nil {
			printAPIError(err)
			os.Exit(1)
		}
		switch {
		case st.Enabled:
			fmt.Println("authentication: on")
		case st.SetupPending:
			fmt.Println("authentication: off (run `blanket auth init` on the server's host to turn it on)")
		default:
			fmt.Println("authentication: off")
		}
	},
}

func init() {
	authInitCmd.Flags().StringVar(&authInitCode, "code", "", "the setup code from the server log (default: read it from the setup-code file beside the database)")
	authDisableCmd.Flags().BoolVar(&authDisableYes, "yes", false, "don't ask for confirmation")
	authCmd.AddCommand(authInitCmd, authDisableCmd, authStatusCmd)
	RootCmd.AddCommand(authCmd)
}

func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "yes"
}

// openTokenStore opens the database directly, for when no server holds it.
func openTokenStore() (auth.Store, func()) {
	db := boltlib.MustOpenBoltDatabase()
	DB, err := boltlib.OpenBlanketBoltDB(db, prepareOptionsFromConfig())
	if err != nil {
		db.Close()
		fail(err.Error())
	}
	store, ok := DB.(auth.Store)
	if !ok {
		db.Close()
		fail("this database has no token store")
	}
	return store, func() { db.Close() }
}

func offlineAuthInit(dataDir string) (string, string) {
	store, closeDB := openTokenStore()
	defer closeDB()
	ts, err := store.ListTokens()
	if err != nil {
		fail(err.Error())
	}
	if auth.AnyEnablesAuth(ts) {
		fail("authentication is already on")
	}
	secret, _, err := auth.IssueToken(store, auth.InitAdminToken())
	if err != nil {
		fail(err.Error())
	}
	store.SetSetupCodeHash("")
	store.SetAuthEnabledTs(time.Now().Unix())
	auth.RemoveIfExists(auth.SetupCodePath(dataDir))
	path := auth.CredentialsPath(dataDir)
	if err := auth.WriteSecretFile(path, secret+"\n"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", path, err)
		path = ""
	}
	return secret, path
}

func offlineAuthDisable(dataDir string) int {
	store, closeDB := openTokenStore()
	defer closeDB()
	n, err := store.DeleteTokensWhere(func(t auth.Token) bool { return t.EnablesAuth() })
	if err != nil {
		fail(err.Error())
	}
	store.SetAuthEnabledTs(0)
	if err := auth.RemoveIfExists(auth.CredentialsPath(dataDir)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove the credentials file: %v\n", err)
	}
	return n
}
