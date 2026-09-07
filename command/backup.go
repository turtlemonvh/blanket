package command

/*

`blanket backup` (turtlemonvh/blanket#23 phase 4).

The interesting decision here is that this command has **two** ways to take
a backup and picks between them at runtime, rather than one way with a
caveat in the docs.

It has to. bolt takes an exclusive flock, so:

  - while the server is running, the CLI cannot open the database at all,
    and the only process that can take a backup is the server — hence
    POST /ops/backup;
  - while the server is stopped, there is no server to ask, and requiring
    one would mean "you must start the thing you are about to upgrade in
    order to back it up before upgrading it".

So: try the ops endpoint first, and fall back to opening the database
directly when nothing answers. The ordering matters — the *other* order
would take the lock first and thereby stop a running server from serving,
which is a spectacular way to fail an operation whose entire purpose is
safety.

The fallback is only taken on a transport failure (connection refused, no
route). An HTTP error from a server that *did* answer is reported as-is: if
the server is up and refused, opening the database behind its back is the
last thing to do.

*/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	boltlib "github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/server"
)

var backupConf struct {
	Dir string
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Write a consistent copy of the database to the backups directory",
	Long: `Write a consistent point-in-time copy of the blanket database.

Works whether or not the server is running: with a server up the backup is
taken by the server itself (bolt's MVCC makes it consistent without pausing
anything), and with the server down it is taken directly.

Backups land in <database dir>/backups/ unless --dir or the
database.backupDir config key says otherwise, and the newest 3 are kept.`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()
		os.Exit(runBackup())
	},
}

func init() {
	backupCmd.Flags().StringVar(&backupConf.Dir, "dir", "", "Directory to write the backup into (default: <database dir>/backups)")
	RootCmd.AddCommand(backupCmd)
}

func runBackup() int {
	dir := backupConf.Dir
	if dir == "" {
		dir = viper.GetString("database.backupDir")
	}

	// Path 1: ask the running server.
	path, status, err := backupViaServer(dir)
	switch {
	case err == nil:
		fmt.Printf("Wrote %s (via the running server on port %d).\n", path, viper.GetInt("port"))
		return 0
	case status != 0:
		// The server answered and said no. Do not go behind its back.
		fmt.Fprintf(os.Stderr, "error: the server refused the backup (%d): %v\n", status, err)
		return 1
	}

	// Path 2: nothing answered, so take the lock ourselves.
	db := boltlib.MustOpenBoltDatabase()
	defer db.Close()

	DB, oerr := boltlib.OpenBlanketBoltDB(db, prepareOptionsFromConfig())
	if oerr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", oerr)
		return 1
	}
	written, berr := DB.Backup(dir)
	if berr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", berr)
		return 1
	}
	fmt.Printf("Wrote %s (the server is not running; taken directly).\n", written)
	return 0
}

// backupViaServer POSTs to /ops/backup. Returns (path, 0, err) when the
// server could not be reached at all, and (path, status, err) when it
// answered with a failure — the caller distinguishes the two, since only
// the first justifies falling back.
func backupViaServer(dir string) (string, int, error) {
	reqURL := fmt.Sprintf("http://localhost:%d/ops/backup", viper.GetInt("port"))
	if dir != "" {
		// A directory is a filesystem path: it can contain spaces, and on
		// Windows a drive letter's colon and backslashes. Escaping is not
		// optional.
		reqURL += "?dir=" + url.QueryEscape(dir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), httpx.DefaultRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
	if err != nil {
		return "", 0, err
	}
	// Required by the ops guard; see server/serve_ops.go for why presence
	// rather than value is what matters.
	req.Header.Set(server.OpsHeader, "cli")

	res, err := httpx.Client().Do(req)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		return "", res.StatusCode, fmt.Errorf("%s", string(body))
	}

	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", res.StatusCode, err
	}
	return payload.Path, 0, nil
}
