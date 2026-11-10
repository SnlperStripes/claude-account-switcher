package main

import "testing"

func TestHoldAwakeReleases(t *testing.T) {
	for range 2 {
		free, err := holdAwake("claude-account-switcher test")
		if err != nil {
			t.Fatal(err)
		}
		free()
	}
}

func TestStayAwakeFreesOnce(t *testing.T) {
	free := stayAwake()
	free()
	free()
}
