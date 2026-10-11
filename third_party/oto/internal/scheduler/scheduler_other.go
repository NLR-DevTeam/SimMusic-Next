//go:build !windows

package scheduler

func Begin(label string) func() { return func() {} }
