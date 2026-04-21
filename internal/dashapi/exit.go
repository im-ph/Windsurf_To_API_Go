package dashapi

import "os"

// osExit is indirected so the self-update path can exit cleanly (PM2 autorestart).
var osExit = func(code int) { os.Exit(code) }
