package server

import "time"

func defaultNowSec() int64 { return time.Now().Unix() }
