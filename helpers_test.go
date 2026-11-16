package main

import "time"

func timeAt(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, time.Local) }
