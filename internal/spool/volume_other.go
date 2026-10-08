//go:build !linux && !darwin

package spool

// HostVolumes is nil here: there is no volume implementation for this host, and the quota watcher
// bounds a run's folder.
func HostVolumes() Volumes { return nil }
