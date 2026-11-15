//go:build !windows && !darwin && !linux

package main

func holdAwake(string) (func(), error) { return func() {}, nil }
