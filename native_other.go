//go:build !darwin

package main

import "net"

func runNativeUI(*switcher, net.Listener) bool { return false }
