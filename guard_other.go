//go:build !linux && !darwin && !windows

package main

func memoryCritical() (bool, string) { return false, "" }

func lastResort(pid int) {}
