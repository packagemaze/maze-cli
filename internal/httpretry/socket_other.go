//go:build !windows

package httpretry

func platformConnectionDropped(error) bool { return false }
