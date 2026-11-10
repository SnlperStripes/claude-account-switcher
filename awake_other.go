//go:build !windows && !darwin

package main

func holdAwake(string) (func(), error) { return func() {}, nil }
