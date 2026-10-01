//go:build !linux

package xkeen

func syncParent(path string) error { return nil }
