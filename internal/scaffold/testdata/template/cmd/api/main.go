package main

import (
	"fmt"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/app"
)

func main() {
	fmt.Println(app.Name())
	// vogel:component worker begin
	fmt.Println("worker enabled")
	// vogel:component notification begin
	fmt.Println("notifications enabled")
	// vogel:component notification end
	// vogel:component worker end
	// vogel:component storage begin
	fmt.Println("storage enabled")
	// vogel:component storage end
}
