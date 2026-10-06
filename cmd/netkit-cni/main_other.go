//go:build !linux

package main

import "fmt"

func main() {
	panic(fmt.Sprintf("netkit-cni is Linux-only"))
}
